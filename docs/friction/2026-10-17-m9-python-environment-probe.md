# M9 opt-in Python environment probe — source preparation

Parent: clean integrated M9 `d2a90e1add0981166e8e55a8ddb7402f16736bf5`.
This adds test/provisioning/report source only. It changes no production code and
does not replace `TestLangprobeABCD` or the existing Go/Python lifecycle entrypoints.
No test, build, manager command, provisioning, job, push or installation ran during
source preparation. Independent review and root-owned agent-a runtime are next.

## Concrete entrypoint and isolation

`cmd/huyang/python_environment_probe_test.go` contains
`TestPythonEnvironmentAcceptance` (Linux). It skips only when
`HUYANG_M9_PYTHON_PROBE=1` is absent. When enabled, missing executables, wrong version
pins, unavailable tracing, missing server publications, wrong interpreter selection,
missing findings, protocol errors and deadlines fail the test. This is not a
prerequisite-check-only pass.

The test requires a prebuilt private binary; it never builds one or contacts the
installed service. It reuses the existing package's `probeMCP` JSON transport/fixture
helpers, with a deadline-bound adapter constructor for JSON and plain connections.
Every case owns a separate daemon, socket, state directory, Neovim init, config/XDG
and cache paths. Subprocess environments are allowlisted, with the original HOME
value preserved and no credential-bearing environment or inherited service endpoints.
Git user/system configs, netrc and Poetry keyring access are disabled for fixtures.
Neovim uses the generated init and pinned lspconfig. No Ollama/eval interaction exists.

A real strace `execve` trace covers the private service and descendants. Untrusted,
local-venv and missing-manager cases assert manager nonexecution from that trace,
not from an empty manager result. Real Pyright publications and progress events are
observed without modifying findings, versions, timing, handlers' results or progress.
The attached client's effective settings are recorded; supplied explicit selection is
not claimed to prove dependencies are installed.

## Runnable matrix

1. `local_venv`: a real `.venv` resolves a synthetic module placed in site-packages.
2. `explicit_wins`: the local venv lacks that module, an explicit external interpreter
   has it, and actual attached `pythonPath` must name the external environment.
3. `sandbox_canonical`: prepared Python source keeps the canonical environment path
   in the actual sandbox client's settings and does not report the installed dependency
   missing. No preparation is applied.
4. `inherited_virtual_env`: the private daemon's own inherited environment supplies
   its interpreter, preserving the agreed precedence.
5. `unknown` and `cold_unknown_baseline`: real Pyright publishes multiple missing
   imports and a real assignment error. The normalized current report must have one
   stable `python_environment_unresolved` warning, no raw missing-import bodies,
   the genuine assignment error and provisional confidence. The cold case omits
   explicit server warmup and refuses a silently clean first edit.
6. `uv_local_wins`: real uv is present and a uv project marker exists, but the local
   venv wins without invoking uv during discovery.
7. `uv_base_rejected`: actual uv fallback selects a managed base interpreter;
   Huyang must reject it with `environment_manager_not_project_environment`.
8. `poetry_external`: real pinned Poetry creates a job-local external environment;
   its Python must be a symlink, its site-packages contain the synthetic dependency,
   the real manager must run, and Pyright must receive that selected path.
9. `untrusted_uv` / `untrusted_poetry`: discovery reports
   `environment_manager_untrusted`, preserves real findings, and the process trace
   must contain no uv/Poetry execution.
10. `missing_manager` / `manager_failure`: an isolated PATH omits uv, or an
    unavailable Python version makes real offline uv fail. Expected unresolved
    reasons and provisional import grouping are asserted.
11. `missing_configured_venv`: explicit project configuration still owns selection.
    Real missing-import bodies and the type error must survive; the test does not
    artificially demand grouping contrary to explicit-config precedence.
12. `sandbox_relative_venvPath`: exercises explicit relative environment configuration
    in canonical and staged source. The actual outcome is recorded. Since the current
    sandbox copier includes the local venv, this does not pretend to be an omitted-venv
    sandbox. Explicit missing-venv behavior is covered by the preceding case.

Every case performs a real source edit introducing an assignment error, then an
unrelated edit. Unknown-environment cases also exercise JSON acknowledgement/current
body recovery, stable note identity across edits and later calls, and guarded plain
read/overwrite/diagnostics. Both styles must preserve the environment note and real
type error; plain replies must retain exact document/workspace guard tokens.

Known-environment cases fix the type error and require current authoritative clean
evidence with no stale bodies. Recovered raw LSP evidence is checked for version
proof, progress veto and grouped-evidence incompleteness. For actual complete empty
pushes, the recorded server publication and Go observation timestamps must establish
150 ms quiet. Pulls are not falsely subjected to empty-push rules.

## Timing coverage and limits

There are no fixed warmup sleeps. Collection uses bounded MCP requests and inotify
events from real LSP telemetry; a 200 ms quiet-observation timeout lets the existing
150 ms quiet rule mature without inventing a producer event. Each case has a
120-second context deadline; fixture commands have 30-second limits; adapter process
exit closes a stalled read. Cleanup terminates the private traced process group and
the adapters.

Natural late publications and later-call notifications are separately recorded.
No real publication is deliberately delayed and no work-done progress is synthesized.
A successful matrix therefore does **not** imply that every producer timing permutation
occurred. Review `natural_late_publication_observed`,
`complete_empty_push_quiet_checks` and `progress_pending_batches_observed` before
claiming those paths were exercised. Existing M1 deterministic guard tests and the
general lifecycle probes remain necessary where those counters are zero.

Other limits remain explicit:

- This is a synthetic installed dependency, not a real application's dependency graph.
- Configured interpreter/venv ownership is not dependency verification. Missing explicit
  environments may produce ordinary import findings with the producer's own confidence.
- A provisional unknown-environment batch cannot resolve omitted findings. The probe
  preserves that conservative ledger behavior rather than asserting artificial cleanup.
- Pyright can execute a selected local interpreter, as before M9.
- Bootstrap's bounded manager query still uses its existing non-Close-cancelable context.
- No source pass establishes runtime acceptance or historical Go host attribution.
- The Linux-only probe and setup refuse unsupported prerequisites; this adds no portable
  non-Linux acceptance runner.

## Provisioning pins and provenance

`tests/m9-language-probe/setup.sh` provisions only a caller-supplied job-local prefix.
It submits no jobs, builds no Huyang binary and starts no service. Run it under a
sanitized environment. It preserves HOME, redirects tool/config/cache writers, and
uses `uv python install --no-bin` plus private install directories so Python
executable links cannot land in the host's default bin directory.
See the [upstream uv storage controls](https://docs.astral.sh/uv/reference/environment/)
for these directory and bin-install controls.

Pins: Neovim **0.12.5**, nvim-lspconfig
**19576de1c1b6d1b5e231ea6ef9cba9a811c322bb**, Node **22.14.0**,
Pyright **1.1.413**, uv **0.9.5**, Poetry **2.2.1**, CPython **3.12.8**,
gopls **v0.23.0**. Neovim/lspconfig archive SHA-256 values come from the existing
general probe script. Node uses its versioned vendor checksum manifest.
npm and Poetry dependency resolution use cutoff **2026-10-16T00:00:00Z**;
the resulting package-lock, freeze manifest and downloaded-archive hashes are retained.
This is version/cutoff pinning plus recorded resolution, not a pre-authored,
fully hash-locked transitive Python dependency set.

The job must already have Go compatible with the repository, a bootstrap Python3
with venv/ensurepip, strace, curl, tar, sha256sum, Git and standard shell utilities.
Bootstrap Python/host strace/Git versions are recorded; they are prerequisites,
not tools silently installed on the host. The actual probe interpreter is pinned
CPython 3.12.8. Provisioning is the only step that downloads dependencies.

## Exact root-owned job commands — not executed here

Run from the integrated repository checkout, on agent-a, with a fresh short cache
path. Root owns job submission, runtime scheduling and any future M8 helper/budget merge.

```sh
scratch="$HOME/.cache/huyang-friction/m9"
tools="$scratch/tools"
run="$scratch/r1"                 # must exist, be empty, and never reuse a live run
mkdir -p "$scratch/tmp" "$scratch/bin" "$run"

env -i HOME="$HOME" PATH="$PATH" \
  bash tests/m9-language-probe/setup.sh "$tools"

# Root builds this exact reviewed source, after integrating concurrent helper fixes.
HUYANG_FRICTION=0 TMPDIR="$scratch/tmp" \
  go build -o "$scratch/bin/huyang" ./cmd/huyang

# No downloads/provisioning occur in the opt-in test itself. Its fixture commands
# create real offline venvs and synthetic installed modules under the private run.
HUYANG_FRICTION=0 TMPDIR="$scratch/tmp" \
HUYANG_M9_PYTHON_PROBE=1 HUYANG_M9_TOOLS="$tools" \
HUYANG_M9_RUN="$run" HUYANG_M9_BINARY="$scratch/bin/huyang" \
  go test ./cmd/huyang -run '^TestPythonEnvironmentAcceptance$' -count=1 -v -timeout 40m \
  >"$scratch/python-probe-go-test.log" 2>&1
```

No `go test` invocation may omit `HUYANG_FRICTION=0`. A missing prerequisite is a
failed run requiring a new root decision/provisioning step; it is never a passing skip.
Use a new empty run directory for a rerun.

Artifacts: tool versions/manifest/locks, exact binary SHA-256, per-case setup-command
logs, JSON/plain wire logs, numbered JSON tool replies, actual attached settings,
raw LSP publication/progress telemetry, execve traces, before/after SHA-256 maps of
canonical fixture bytes (excluding Git database), environment maps, and a coverage
record including timing counters. Failure cleanup retains these artifacts. If fixture
provisioning fails early, the preparation hash snapshot and command failure log remain.
State/socket directories are private runtime state; do not publish them as artifacts or
inspect/export correlation keys. Exclude them when collecting the safe report files.

General lifecycle entrypoints remain unchanged:

- `TestLangprobeABCD/(go|python)$` in `cmd/huyang/langprobe_test.go`, using
  `HUYANG_LANGPROBE=1`, a private prebuilt binary and language subset (not strict
  nine-language acceptance).
- `TestLangprobeVersionNegotiationAndBaselineLatency` in
  `internal/provider/embed/diagnostic_version_test.go`.
- `TestRealLanguageDiagnosticBarriersNeverPromoteIncompleteEvidence/(gopls|pyright)$`
  in `internal/providerpool/evidence_test.go`.
