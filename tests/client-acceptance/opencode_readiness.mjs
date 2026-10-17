// Acceptance instrumentation for OpenCode 2.0.30; never edits native tools.
const PROFILES = {
  huyang_lean: ["edit_apply", "read", "search"],
  huyang_full: [
    "change_plan", "diagnostics", "edit_apply", "evidence_get",
    "language_server_setup", "language_server_status", "navigate", "read",
    "revision_diff", "search", "symbol_find", "workspace_inspect", "workspace_open",
  ],
};
const NAMESPACES = Object.keys(PROFILES);
const EXPECTED = new Map(Object.entries(PROFILES).flatMap(([namespace, names]) =>
  names.map((name) => [namespace + "_" + name, { namespace, name }])));
const clock = {
  now: () => performance.now(),
  setTimeout: (callback, ms) => setTimeout(callback, ms),
  clearTimeout: (timer) => clearTimeout(timer),
};

// Info.name is the unqualified name; Info.id is the native effective name.
// Only project metadata into the witness. Never retain or invoke executors.
function observe(registry) {
  const tools = registry.filter((tool) =>
    Object.hasOwn(PROFILES, tool.options?.namespace)
      || NAMESPACES.some((ns) => tool.id.startsWith(ns + "_")))
    .map((tool) => ({
      id: tool.id,
      name: tool.name,
      namespace: tool.options?.namespace ?? null,
      codemode: tool.options?.codemode ?? null,
      hasInput: tool.input !== null && typeof tool.input === "object",
      hasExecute: typeof tool.execute === "function",
    })).sort((a, b) => a.id.localeCompare(b.id));
  const ids = new Set(tools.map((tool) => tool.id));
  const ready = tools.length === EXPECTED.size && ids.size === EXPECTED.size
    && tools.every((tool) => {
      const expected = EXPECTED.get(tool.id);
      return expected && tool.name === expected.name
        && tool.namespace === expected.namespace && tool.codemode === false
        && tool.hasInput && tool.hasExecute;
    });
  return { ready, tools };
}

export async function installReadiness(ctx, log, {
  deadlineMs = 30000,
  time = clock,
} = {}) {
  if (!Number.isFinite(deadlineMs) || deadlineMs <= 0) {
    throw new Error("readiness deadline must be positive");
  }
  let generation = 0;
  let sequence = 0;
  let disposed = false;
  const pending = new Set();
  const registrations = [];

  // This callback runs within State's synchronous rebuild. Resolve signals only:
  // no editor access, registry reads, reloads, logging, or tool mutations here.
  const signal = () => {
    generation++;
    for (const waiter of pending) {
      const wake = waiter.wake;
      waiter.wake = undefined;
      wake?.();
    }
  };

  const prompt = async (event) => {
    const identity = {
      sessionID: event.sessionID, messageID: event.messageID, promptSequence: ++sequence,
    };
    const started = time.now();
    const deadline = started + deadlineMs;
    let timer;
    let lastObservation = null;
    let failure;
    let rejectStop;
    const stopped = new Promise((_, reject) => { rejectStop = reject; });
    const waiter = {
      wake: undefined,
      stop(reason) {
        failure ??= new Error("OpenCode native registry readiness: " + reason);
        rejectStop(failure);
      },
    };
    const check = () => {
      if (disposed) throw new Error("OpenCode native registry readiness: disposed");
      if (failure) throw failure;
      if (time.now() >= deadline) throw new Error("OpenCode native registry readiness: deadline");
    };
    pending.add(waiter);
    try {
      check();
      log({ kind: "readiness-start", ...identity, generation, deadlineMs });
      timer = time.setTimeout(() => waiter.stop("deadline"), deadlineMs);
      while (true) {
        check();
        // Arm before reading. A rebuild during list() cannot be lost between
        // the predicate and the wait, even if list() returns an older value.
        const changed = new Promise((resolve) => { waiter.wake = resolve; });
        const checkedGeneration = generation;
        const registry = await Promise.race([ctx.tool.list(), stopped]);
        check();
        lastObservation = observe(registry);
        log({
          kind: "registry-observation", ...identity,
          checkedGeneration, generation, ...lastObservation,
        });
        // A notification during the asynchronous read requires a new read.
        if (generation !== checkedGeneration) continue;
        if (lastObservation.ready) {
          log({ kind: "readiness-ready", ...identity, generation,
            elapsedMs: time.now() - started, tools: lastObservation.tools });
          return;
        }
        await Promise.race([changed, stopped]);
      }
    } catch (error) {
      log({ kind: "readiness-failure", ...identity, generation,
        elapsedMs: time.now() - started, error: String(error), lastObservation });
      throw error;
    } finally {
      time.clearTimeout(timer);
      pending.delete(waiter);
      waiter.wake = undefined;
    }
  };

  // Install the hook first so a failed observer registration leaves a rejecting
  // hook, rather than disabling the witness and letting a prompt pass.
  let installationFailure;
  registrations.push(await ctx.session.hook("prompt", async (event) => {
    if (installationFailure) {
      log({ kind: "readiness-failure", sessionID: event.sessionID,
        messageID: event.messageID, generation, error: String(installationFailure) });
      throw installationFailure;
    }
    await prompt(event);
  }));
  try {
    registrations.push(await ctx.tool.transform(signal));
    log({ kind: "readiness-installed", generation, deadlineMs });
  } catch (error) {
    installationFailure = error;
    log({ kind: "readiness-installation-failure", generation, error: String(error) });
  }

  // Setup must finish without waiting for readiness: MCP activation needs it.
  return async () => {
    disposed = true;
    for (const waiter of pending) waiter.stop("disposed");
    try {
      await registrations[0].dispose();
    } finally {
      if (registrations[1]) await registrations[1].dispose();
    }
  };
}
