# M9 opt-in Python environment probe — source preparation

Original probe parent: integrated M9 `d2a90e1add0981166e8e55a8ddb7402f16736bf5`.
This combined first probe review fix is based on immutable
`9062353cfcf1e7595a8012172cecca5bc74de2a1`.
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
and cache paths. Before any source-owned write or subprocess, both the test and setup
require a canonical scratch HOME, a non-initial Linux UID namespace, and write roots
strictly under resolved HOME. Default HOME must be exactly `/home/job`; an explicit
`HUYANG_M9_EXPECTED_HOME` must equal HOME and cannot name `/root`, `/tmp`, or
another user's `/home/...` tree. HOME must be owned by the current UID and not
writable by others. The run parent, run directory and TMPDIR must be owned and 0700.
Tools, the resolved private binary and TMPDIR must be inside the private run parent.
The root wrapper's check is additional protection, not the sole guard.

Subprocess environments are allowlisted, preserving this verified scratch HOME,
with no credential-bearing environment or inherited service endpoints.
Git user/system configs, netrc and Poetry keyring access are disabled for fixtures.
Neovim uses the generated init and pinned lspconfig. No Ollama/eval interaction exists.

A real strace `execve,execveat` trace covers the private service and descendants. Untrusted,
local-venv and missing-manager cases assert manager nonexecution from that trace,
not from an empty manager result. Negative matching decodes quoted path/argv strings
and recognizes uv/Poetry basenames through any path and Python `-m poetry`
(including module subpaths). Positive fallback matching still requires the exact
resolved manager executable. Tracing is required and includes `--kill-on-exit`;
an unsupported strace or denied ptrace is a failure. Real Pyright publications and progress events are
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
evidence with no stale bodies. Raw-complete push evidence must prove the document
version and absence of pending progress. A raw-complete pull is allowed to lack that
proof: effective proof requires complete, selected, current document version,
no progress/timeout, and no environment downgrade. A coverage dimension citing only
vetoed pulls cannot be authoritative; a separate valid batch may supersede an older
veto. Progress counters count actual observed batches, including complete vetoed pulls.
`unproven_pull_batches_observed` and `pull_veto_dimension_checks` distinguish raw
veto observations from dimensions where the non-authority assertion was exercised.
A superseding proof must be at least as recent and name the same document/revision.
Grouped evidence remains incomplete. Pure regressions for manager trace spellings
and pull proof are included in the documented job test selection. For actual complete empty
pushes, the recorded server publication and Go observation timestamps must establish
150 ms quiet. Pulls are not falsely subjected to empty-push rules.

## Timing coverage and limits

There are no fixed warmup sleeps. Collection uses bounded MCP requests and inotify
events from real LSP telemetry; a 200 ms quiet-observation timeout lets the existing
150 ms quiet rule mature without inventing a producer event. Each case has a
120-second context deadline; fixture commands have 30-second limits; adapter process
exit closes a stalled read. Fixture commands and adapters also own process groups.
Cleanup tracks descendant identities (PID plus kernel start time), including separate
provider groups, sends TERM and polls for disappearance, escalates to KILL at three
seconds and fails if any tracked identity or the original group persists at six.
It does not equate strace leader exit with daemon-tree exit. Strace kill-on-exit also
prevents its tracees surviving a tracer exit between process observations. The
fixture commands are the fixed, trusted tool invocations in this test, not arbitrary
project scripts. Kernel/process-tree enforcement still needs runtime verification.

Natural late publications and later-call notifications are separately recorded.
`natural_late_publication_observed` means a non-empty same-version raw publish
arrived after the edit reply returned; it does not prove Huyang's late-capture path,
a provisional reply, or late finding delivery. The telemetry handler runs inside
Huyang's per-client wrapper, after Huyang records the publication. Its microsecond
timestamp skew makes the empty-push quiet check stricter.
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
- Full descendant strace adds overhead near the manager's existing two-second deadline.
  Seccomp-assisted tracing is not enabled or assumed; root must assess tracing cost and
  ptrace permissions in the actual private job. Tool absence/timeout fails honestly.
- No source pass establishes runtime acceptance or historical Go host attribution.
- The Linux-only probe and setup refuse unsupported prerequisites; this adds no portable
  non-Linux acceptance runner.

## Provisioning pins and provenance

`tests/m9-language-probe/setup.sh` provisions only a caller-supplied new or empty
job-local prefix. It refuses reuse before writing, preventing stale child symlinks
from redirecting provisioning writes outside that prefix.
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
the resulting package-lock and freeze manifest are retained. Download hashes cover
the explicit Neovim, lspconfig and Node archives (plus the downloaded vendor
manifest), not every uv wheel, managed-Python distribution or Poetry dependency.
Before/after installed-tool tree SHA-256 maps record actual installed bytes and modes.
This is version/cutoff pinning plus recorded resolution, not a pre-authored,
fully hash-locked transitive Python dependency set.

The job must already have Go compatible with the repository, a bootstrap Python >=3.9
with venv/ensurepip, strace supporting `--kill-on-exit`, curl, tar, xz, awk,
sha256sum, Git and standard shell utilities.
Bootstrap Python and Git executable paths/versions are captured before PATH replacement;
stdlib venv provenance, ensurepip's bundled pip and the bootstrap venv's actual pip
version are recorded separately from managed CPython. Host strace/Go versions are recorded; they are prerequisites,
not tools silently installed on the host. The actual probe interpreter is pinned
CPython 3.12.8. Provisioning is the only step that downloads dependencies.

## Exact root-owned job commands — not executed here

Run from the integrated repository checkout, on agent-a, with a fresh short cache
path. Root owns job submission, runtime scheduling and any future M8 helper/budget merge.

The job must use its isolated `/home/job` HOME and non-initial UID namespace.
These are **commands for root's later job**, not commands executed during this fix.
Use a pinned job Go installation compatible with this repository; `GOTOOLCHAIN=local`
fails instead of silently downloading another toolchain. The executable hash alone
does not prove the reviewed source: root owns the exact-source build provenance.

```sh
set -eu
umask 077
test "$HOME" = /home/job
job_path=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
scratch=/home/job/.cache/huyang-friction/m9
# The source guard validates the UID namespace and resolved write root BEFORE mkdir.
env -i HOME=/home/job PATH="$job_path" \
  python3 -I -B tests/m9-language-probe/safety.py "$scratch"
tools="$scratch/tools"
run="$scratch/r1"                 # fresh, empty; never reuse a live run
mkdir -p "$scratch/tmp" "$scratch/bin" "$run"
clean() {
  env -i HOME=/home/job HUYANG_M9_EXPECTED_HOME=/home/job PATH="$job_path" LANG=C.UTF-8 \
    TMPDIR="$scratch/tmp" XDG_CONFIG_HOME="$scratch/job-config" \
    XDG_CACHE_HOME="$scratch/job-cache" XDG_DATA_HOME="$scratch/job-data" \
    XDG_STATE_HOME="$scratch/job-state" GOCACHE="$scratch/go-cache" \
    GOPATH="$scratch/go" GOMODCACHE="$scratch/go/pkg/mod" \
    GOENV=off GOFLAGS=-mod=readonly GOTOOLCHAIN=local \
    GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org \
    NETRC=/dev/null GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
    GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_COUNT=0 GIT_TERMINAL_PROMPT=0 \
    HUYANG_FRICTION=0 "$@"
}
clean bash tests/m9-language-probe/setup.sh "$tools"
# Root builds the exact reviewed source; installed service binaries are never used.
clean go build -o "$scratch/bin/huyang" ./cmd/huyang
set +e
clean env HUYANG_M9_PYTHON_PROBE=1 HUYANG_M9_TOOLS="$tools" \
  HUYANG_M9_RUN="$run" HUYANG_M9_BINARY="$scratch/bin/huyang" \
  go test ./cmd/huyang -run '^(TestPythonEnvironmentAcceptance|TestM9ManagerTraceSpellings|TestM9EffectivePullProof) -count=1 -v -timeout 40m \
  >"$scratch/python-probe-go-test.log" 2>&1
test_exit=$?
set -e
# Collect on failure too. A collector error is a failed/incomplete evidence run.
clean python3 -I -B tests/m9-language-probe/collect.py \
  "$run" "$scratch/m9-python-probe.tar.gz" \
  --go-log "$scratch/python-probe-go-test.log" --test-exit "$test_exit"
exit "$test_exit"
```

Every Go test has `HUYANG_FRICTION=0`. Provisioning, build, test and collection use
an explicit empty environment plus job-local configs/caches; no real auth, proxy
credentials, GOENV, inherited GOFLAGS or user Git configuration is forwarded.
A missing prerequisite fails; it is never a passing skip. Use a new empty run and
new archive name for a rerun.

Root should request only `/home/job/.cache/huyang-friction/m9/m9-python-probe.tar.gz`
as the job artifact, never scratch or the entire run. `collect.py` walks only the
run top level and two-digit case directories, and accepts these regular owned files:

- Top: `versions.json`, `binary.json`, `preflight-commands.log`, the three named
  `tool-*` manifest/lock files, before/after tool hashes and HOME metadata.
- Case: `lsp.jsonl`, `wire-*.jsonl`, numbered tool-reply JSON, `exec.<pid>`,
  `coverage.json`, `*hashes*.json`, `fixture-commands.log`, `service.log`,
  `plain-*.txt`, and `raw-publication-summary.json`.
- The explicitly named Go-test log and a generated `collection.json` with every
  collected file's size/SHA-256, missing top evidence, coverage presence and test exit.

It never recurses into tools, bin/huyang, tmp, caches, venvs, .git, state, sockets,
config, data, xdg-state or run directories. Private service state/correlation keys
are never inspected or exported. All actual trace/reply/telemetry/report evidence
matching the allowlist is retained, including failed-run evidence. The collector
rejects symlinks, changing files, more than 20000 evidence files, or payload/archive
size reaching **63 MiB**. The archive writer enforces the cap while writing. It
removes a partial archive and fails clearly instead of dropping required evidence.
`complete_collection` means all allowlisted existing evidence was collected, not
that all cases completed or passed; test exit and coverage remain essential.

Canonical fixture bytes (excluding Git database), selected environments, and the
whole private tool tree are compared before/after, including modes and symlink
targets. The resolved tool executables (including read-only host prerequisites)
and private Huyang binary are also hash-compared. Shared managed Python is made read-only by setup and also hash-checked;
chmod alone is not treated as proof. Hashing streams bounded owned trees (100000
entries, 512 MiB/file, 4 GiB/tree); it records symlinks without following them
while walking trees. Only the explicitly named executable files are additionally
resolved and read, never host directories or unrelated HOME contents. Scratch HOME outside the one allowed mutable run parent receives a
bounded metadata-only inventory (20000 entries), and any change fails. TMPDIR is
case-local, including a separate preflight directory. The run parent deliberately
contains mutable caches/configs/temporary and service state; these are not claimed
immutable. This verifies fixture/environment/tool immutability and detects HOME
fallback writes while preserving the intended job-local runtime writers.

General lifecycle entrypoints remain unchanged:

- `TestLangprobeABCD/(go|python)$` in `cmd/huyang/langprobe_test.go`, using
  `HUYANG_LANGPROBE=1`, a private prebuilt binary and language subset (not strict
  nine-language acceptance).
- `TestLangprobeVersionNegotiationAndBaselineLatency` in
  `internal/provider/embed/diagnostic_version_test.go`.
- `TestRealLanguageDiagnosticBarriersNeverPromoteIncompleteEvidence/(gopls|pyright)$`
  in `internal/providerpool/evidence_test.go`.
 -count=1 -v -timeout 40m \
  >"$scratch/python-probe-go-test.log" 2>&1
test_exit=$?
set -e
# Collect on failure too. A collector error is a failed/incomplete evidence run.
clean python3 -I -B tests/m9-language-probe/collect.py \
  "$run" "$scratch/m9-python-probe.tar.gz" \
  --go-log "$scratch/python-probe-go-test.log" --test-exit "$test_exit"
exit "$test_exit"
```

Every Go test has `HUYANG_FRICTION=0`. Provisioning, build, test and collection use
an explicit empty environment plus job-local configs/caches; no real auth, proxy
credentials, GOENV, inherited GOFLAGS or user Git configuration is forwarded.
A missing prerequisite fails; it is never a passing skip. Use a new empty run and
new archive name for a rerun.

Root should request only `/home/job/.cache/huyang-friction/m9/m9-python-probe.tar.gz`
as the job artifact, never scratch or the entire run. `collect.py` walks only the
run top level and two-digit case directories, and accepts these regular owned files:

- Top: `versions.json`, `binary.json`, `preflight-commands.log`, the three named
  `tool-*` manifest/lock files, before/after tool hashes and HOME metadata.
- Case: `lsp.jsonl`, `wire-*.jsonl`, numbered tool-reply JSON, `exec.<pid>`,
  `coverage.json`, `*hashes*.json`, `fixture-commands.log`, `service.log`,
  `plain-*.txt`, and `raw-publication-summary.json`.
- The explicitly named Go-test log and a generated `collection.json` with every
  collected file's size/SHA-256, missing top evidence, coverage presence and test exit.

It never recurses into tools, bin/huyang, tmp, caches, venvs, .git, state, sockets,
config, data, xdg-state or run directories. Private service state/correlation keys
are never inspected or exported. All actual trace/reply/telemetry/report evidence
matching the allowlist is retained, including failed-run evidence. The collector
rejects symlinks, changing files, more than 20000 evidence files, or payload/archive
size reaching **63 MiB**. The archive writer enforces the cap while writing. It
removes a partial archive and fails clearly instead of dropping required evidence.
`complete_collection` means all allowlisted existing evidence was collected, not
that all cases completed or passed; test exit and coverage remain essential.

Canonical fixture bytes (excluding Git database), selected environments, and the
whole private tool tree are compared before/after, including modes and symlink
targets. Shared managed Python is made read-only by setup and also hash-checked;
chmod alone is not treated as proof. Hashing streams bounded owned trees (100000
entries, 512 MiB/file, 4 GiB/tree), never follows symlinks or reads unrelated HOME
contents. Scratch HOME outside the one allowed mutable run parent receives a
bounded metadata-only inventory (20000 entries), and any change fails. TMPDIR is
case-local, including a separate preflight directory. The run parent deliberately
contains mutable caches/configs/temporary and service state; these are not claimed
immutable. This verifies fixture/environment/tool immutability and detects HOME
fallback writes while preserving the intended job-local runtime writers.

General lifecycle entrypoints remain unchanged:

- `TestLangprobeABCD/(go|python)$` in `cmd/huyang/langprobe_test.go`, using
  `HUYANG_LANGPROBE=1`, a private prebuilt binary and language subset (not strict
  nine-language acceptance).
- `TestLangprobeVersionNegotiationAndBaselineLatency` in
  `internal/provider/embed/diagnostic_version_test.go`.
- `TestRealLanguageDiagnosticBarriersNeverPromoteIncompleteEvidence/(gopls|pyright)$`
  in `internal/providerpool/evidence_test.go`.
