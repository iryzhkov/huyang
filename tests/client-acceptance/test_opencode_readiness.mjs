import assert from "node:assert/strict";
import test from "node:test";
import { installReadiness } from "./opencode_readiness.mjs";

const LEAN = ["edit_apply", "read", "search"];
const FULL = [
  "change_plan", "diagnostics", "edit_apply", "evidence_get",
  "language_server_setup", "language_server_status", "navigate", "read",
  "revision_diff", "search", "symbol_find", "workspace_inspect", "workspace_open",
];
function definitions() {
  return Object.entries({ huyang_lean: LEAN, huyang_full: FULL }).flatMap(([namespace, names]) =>
    names.map((name) => Object.freeze({
      id: namespace + "_" + name, name, description: "native fake definition",
      options: Object.freeze({ namespace, codemode: false }),
      input: Object.freeze({ type: "object" }),
      execute() { assert.fail("readiness must never execute tools"); },
    })));
}
const drain = async () => {
  // Drain bounded Promise continuations only; there are no real timers or IO.
  for (let i = 0; i < 12; i++) await Promise.resolve();
};
function fakeSDK(initial = []) {
  let registry = Object.freeze(initial);
  let transforming = false;
  let now = 0;
  let serial = 0;
  const timers = new Map();
  const hooks = new Map();
  const transforms = new Set();
  const log = [];
  const state = { reads: 0, snapshots: [], afterRead: null, transformError: null };
  // Even editor.list() is forbidden: the callback must only signal.
  const editor = new Proxy({}, {
    get() { assert.fail("readiness accessed the transform editor"); },
  });
  const rebuild = (tools) => {
    transforming = true;
    try {
      for (const callback of transforms) callback(editor);
      registry = Object.freeze(tools);
    } finally {
      transforming = false;
    }
  };
  const ctx = {
    tool: {
      async transform(callback) {
        if (state.transformError) throw state.transformError;
        transforms.add(callback);
        rebuild(registry);
        return { async dispose() { transforms.delete(callback); } };
      },
      async list() {
        assert.equal(transforming, false, "recursive registry read during rebuild");
        state.reads++;
        const observed = registry;
        if (state.afterRead) await state.afterRead();
        return observed;
      },
      reload() { assert.fail("readiness must not reload the registry"); },
    },
    session: {
      async hook(name, callback) {
        assert.equal(name, "prompt");
        assert.equal(arguments.length, 2, "no unsupported hook options");
        hooks.set(name, callback);
        return { async dispose() { hooks.delete(name); } };
      },
    },
  };
  const time = {
    now: () => now,
    setTimeout(callback, ms) {
      const id = ++serial;
      timers.set(id, { at: now + ms, callback });
      return id;
    },
    clearTimeout(id) { timers.delete(id); },
  };
  return {
    ctx, time, log, state, timers,
    publish: rebuild,
    tick(ms) {
      now += ms;
      for (const [id, timer] of timers) {
        if (timer.at <= now) {
          timers.delete(id);
          timer.callback();
        }
      }
    },
    async install() {
      return installReadiness(ctx, (entry) => log.push(entry), { deadlineMs: 50, time });
    },
    async prompt(sessionID = "session-a", messageID = "message-1") {
      // Model and signal are intentionally absent, as in pinned SessionPrompt.
      const event = Object.freeze({
        sessionID, messageID, prompt: Object.freeze({ text: "not provider metadata" }),
        delivery: "steer",
      });
      assert.ok(hooks.has("prompt"));
      await hooks.get("prompt")(event);
      // Models native admission -> snapshot -> inference after the awaited hook.
      state.snapshots.push({ sessionID, messageID, tools: registry });
      return registry;
    },
  };
}
const kind = (f, name) => f.log.filter((entry) => entry.kind === name);

test("setup finishes empty; prompt waits before snapshot until native publication", async () => {
  const f = fakeSDK();
  const dispose = await f.install();
  try {
    assert.equal(f.state.reads, 0, "setup must not await or read readiness");
    const turn = f.prompt();
    await drain();
    assert.equal(f.state.snapshots.length, 0);
    assert.equal(f.state.reads, 1);
    f.publish(definitions().filter((tool) => tool.options.namespace === "huyang_lean"));
    await drain();
    assert.equal(f.state.snapshots.length, 0, "one profile is insufficient");
    const native = definitions();
    f.publish(native);
    assert.equal(await turn, native, "native snapshot identity is preserved");
    assert.equal(kind(f, "readiness-ready").length, 1);
    assert.equal(f.timers.size, 0);
  } finally {
    await dispose();
  }
});

test("initially ready registry admits the first request and preserves unrelated tools", async () => {
  const unrelated = Object.freeze({
    id: "read", name: "read", options: Object.freeze({}), input: {}, execute() {},
  });
  const native = [...definitions(), unrelated];
  const f = fakeSDK(native);
  const dispose = await f.install();
  try {
    assert.equal(await f.prompt(), native);
    assert.equal(f.state.reads, 1);
    assert.equal(kind(f, "registry-observation")[0].tools.length, 16);
    assert.equal(kind(f, "readiness-ready")[0].sessionID, "session-a");
  } finally {
    await dispose();
  }
});

test("publication during an asynchronous stale read cannot lose its wakeup", async () => {
  const f = fakeSDK();
  const dispose = await f.install();
  try {
    const native = definitions();
    f.state.afterRead = () => {
      f.state.afterRead = null;
      f.publish(native);
    };
    assert.equal(await f.prompt(), native);
    assert.equal(f.state.reads, 2);
    const observations = kind(f, "registry-observation");
    assert.equal(observations[0].ready, false);
    assert.ok(observations[0].generation > observations[0].checkedGeneration);
    assert.equal(observations[1].ready, true);
  } finally {
    await dispose();
  }
});

test("bursts coalesce and simultaneous prompt witnesses stay session-specific", async () => {
  const f = fakeSDK();
  const dispose = await f.install();
  try {
    const a = f.prompt("a", "a-1");
    const b = f.prompt("b", "b-1");
    await drain();
    assert.equal(f.state.snapshots.length, 0);
    assert.equal(f.state.reads, 2);
    for (let i = 0; i < 20; i++) f.publish([]);
    const native = definitions();
    f.publish(native);
    await Promise.all([a, b]);
    assert.equal(f.state.reads, 4, "one recheck per waiter, not per rebuild");
    const ready = kind(f, "readiness-ready");
    assert.deepEqual(ready.map((entry) => entry.sessionID).sort(), ["a", "b"]);
    assert.equal(new Set(ready.map((entry) => entry.promptSequence)).size, 2);
    assert.ok(ready.every((entry) => entry.generation === ready[0].generation));
    assert.equal(f.timers.size, 0);
  } finally {
    await dispose();
  }
});

test("each later prompt rechecks; prior success cannot admit an incomplete registry", async () => {
  const f = fakeSDK(definitions());
  const dispose = await f.install();
  try {
    await f.prompt("a", "a-1");
    f.publish([]);
    const rejected = assert.rejects(f.prompt("a", "a-2"), /deadline/);
    await drain();
    assert.equal(f.state.snapshots.length, 1);
    f.tick(50);
    await rejected;
    assert.equal(kind(f, "readiness-ready").length, 1);
    assert.equal(kind(f, "readiness-failure")[0].messageID, "a-2");
    assert.equal(f.state.snapshots.length, 1);
  } finally {
    await dispose();
  }
});

test("one prompt deadline cannot cancel or admit a concurrent prompt", async () => {
  const f = fakeSDK();
  const dispose = await f.install();
  try {
    const a = assert.rejects(f.prompt("a", "a-1"), /deadline/);
    await drain();
    f.tick(25);
    const b = f.prompt("b", "b-1");
    await drain();
    f.tick(25);
    await a;
    assert.equal(f.state.snapshots.length, 0);
    f.publish(definitions());
    await b;
    assert.deepEqual(f.state.snapshots.map((snapshot) => snapshot.sessionID), ["b"]);
    assert.deepEqual(kind(f, "readiness-failure").map((entry) => entry.sessionID), ["a"]);
    assert.deepEqual(kind(f, "readiness-ready").map((entry) => entry.sessionID), ["b"]);
    assert.equal(f.timers.size, 0);
  } finally {
    await dispose();
  }
});

test("a native registry read failure rejects before snapshot creation", async () => {
  const f = fakeSDK(definitions());
  const dispose = await f.install();
  try {
    f.state.afterRead = () => { throw new Error("native list failed"); };
    await assert.rejects(f.prompt(), /native list failed/);
    assert.equal(f.state.snapshots.length, 0);
    assert.equal(kind(f, "readiness-failure").length, 1);
    assert.equal(kind(f, "readiness-ready").length, 0);
    assert.equal(f.timers.size, 0);
  } finally {
    await dispose();
  }
});

const invalid = {
  missing: (tools) => tools.slice(1),
  extra: (tools) => [...tools, { ...tools[0], id: "huyang_lean_extra", name: "extra" }],
  namespace: (tools) => [{ ...tools[0], options: { namespace: "other", codemode: false } }, ...tools.slice(1)],
  id: (tools) => [{ ...tools[0], id: "incorrect" }, ...tools.slice(1)],
  name: (tools) => [{ ...tools[0], name: "incorrect" }, ...tools.slice(1)],
  codemode: (tools) => [{ ...tools[0], options: { namespace: "huyang_lean", codemode: true } }, ...tools.slice(1)],
  "absent codemode": (tools) => [{ ...tools[0], options: { namespace: "huyang_lean" } }, ...tools.slice(1)],
  duplicate: (tools) => [tools[1], ...tools.slice(1)],
  "missing definition": (tools) => [{ ...tools[0], input: undefined }, ...tools.slice(1)],
  "missing executor": (tools) => [{ ...tools[0], execute: undefined }, ...tools.slice(1)],
};
for (const [name, corrupt] of Object.entries(invalid)) {
  test("rejects " + name + " without native inference or tool mutation", async () => {
    const native = corrupt(definitions());
    const f = fakeSDK(native);
    const dispose = await f.install();
    try {
      const rejected = assert.rejects(f.prompt(), /deadline/);
      await drain();
      f.tick(50);
      await rejected;
      assert.equal(f.state.snapshots.length, 0);
      assert.equal(kind(f, "readiness-ready").length, 0);
      const failure = kind(f, "readiness-failure")[0];
      assert.equal(failure.sessionID, "session-a");
      assert.equal(failure.lastObservation.ready, false);
      assert.equal(f.timers.size, 0);
    } finally {
      await dispose();
    }
  });
}

test("deadline bounds an unresolved native list; late readiness cannot admit inference", async () => {
  const f = fakeSDK(definitions());
  const dispose = await f.install();
  let release;
  f.state.afterRead = () => new Promise((resolve) => { release = resolve; });
  try {
    const rejected = assert.rejects(f.prompt(), /deadline/);
    await drain();
    f.tick(50);
    await rejected;
    release();
    await drain();
    assert.equal(f.state.snapshots.length, 0);
    assert.equal(kind(f, "readiness-ready").length, 0);
    assert.equal(kind(f, "readiness-failure").length, 1);
    assert.equal(f.timers.size, 0);
  } finally {
    await dispose();
  }
});

test("clock deadline fails closed even before the timeout callback runs", async () => {
  const f = fakeSDK(definitions());
  let now = 0;
  const time = { ...f.time, now: () => now };
  const dispose = await installReadiness(f.ctx, (entry) => f.log.push(entry), { deadlineMs: 50, time });
  try {
    f.state.afterRead = () => { now = 51; };
    await assert.rejects(f.prompt(), /deadline/);
    assert.equal(f.state.snapshots.length, 0);
    assert.equal(kind(f, "readiness-ready").length, 0);
    assert.equal(f.timers.size, 0);
  } finally {
    await dispose();
  }
});

test("disposal cancels all pending prompts and clears their deadlines", async () => {
  const f = fakeSDK();
  const dispose = await f.install();
  const a = assert.rejects(f.prompt("a", "a-1"), /disposed/);
  const b = assert.rejects(f.prompt("b", "b-1"), /disposed/);
  await drain();
  await dispose();
  await Promise.all([a, b]);
  f.publish(definitions());
  await drain();
  assert.equal(f.state.snapshots.length, 0);
  assert.equal(kind(f, "readiness-failure").length, 2);
  assert.equal(kind(f, "readiness-ready").length, 0);
  assert.equal(f.timers.size, 0);
});

test("failed observer registration leaves a fail-closed prompt hook", async () => {
  const f = fakeSDK(definitions());
  f.state.transformError = new Error("observer registration failed");
  const dispose = await f.install();
  try {
    await assert.rejects(f.prompt(), /observer registration failed/);
    assert.equal(f.state.snapshots.length, 0);
    assert.equal(kind(f, "readiness-installation-failure").length, 1);
    assert.equal(kind(f, "readiness-failure").length, 1);
  } finally {
    await dispose();
  }
});
