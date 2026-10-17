# M1 diagnostics implementation evidence

Date: 2026-10-17. Worker worktree:
`/home/igor/.t3/worktrees/huyang/t3-91b05e3c`.
Base: `6188357f91e3bf5fc548d85d1330a3653f054b70`.
Scope: M1 items 1–9 and CI friction isolation. The lead owns integration,
review/CI/PRs; separate workers own provider/server caps, README/systemd and
job-local language-probe setup. No installed service, host installation,
service restart, remote push, PR, merge or shared-roadmap change occurred.

## Requirement status

| M1 requirement | Implementation / validation |
| --- | --- |
| 1. Late completeness and coalescing | Implemented. Immediate and late pushes share version, empty-push quiet and progress checks. Pending observations are retained across collection until proof completes. The latest observation per producer/document replaces superseded observations before ledger insertion, including when newer proof is weaker. Lua and Go regressions pass. |
| 2. Exact pre-edit baseline revision | Implemented. Baseline lookup requires authoritative evidence naming the exact native before-image revision. Native range/literal/whole-file edits retain the before-image object metadata. External-change and exact-stage tests pass; unknown preimages remain baseline_missing. |
| 3. Ledger version / downgrade safety | Implemented. Version 2 covers persisting notices and per-document dimensions. Version 1 migrates and saves the new header before returning; unknown future versions are refused without rewriting. Tests verify persisted migration, notice retention and the version-1 loader's rejection condition. An old binary was not built/run. |
| 4. Staged counts | Implemented. Staged document totals accumulate; canonical workspace totals remain snapshots. Two staged documents report current_count=2, preexisting_count=0 without changing the canonical ledger. |
| 5. Every intermediate probe reply | Implemented. Every mutation and diagnostics collection checks transitions, false resolution, repeat announcements, coordinates, first-touch labels and silent clean mutations. Cursor acknowledgement separates transitions from historical snapshots. Synthetic validator regression passes. Live nine-language acceptance is pending lead integration. |
| 6. Total baseline overhead | Measured below, including real kernel RPC and ledger persistence against populated state. |
| 7. Checkout and mixed endings | Implemented and validated in Lua. Repeated Git checkouts reload in place with increasing changedticks. Mixed LF/CRLF bytes and three physical line coordinates survive reload. Fixed a discovered mixed-ending split bug in transaction.set_buffer_bytes. |
| 8. Bodies after ack / reconnect | Documented full:true recovery in agent-guide and execution contract. Compact defaults retain notice-delta semantics. Unix and HTTP transport tests acknowledge notices, reconnect, and recover current finding bodies with full:true without new announcements. |
| 9. Rust pending / late updates | Implemented. Incomplete rust-analyzer evidence includes rust_diagnostics_pending; starting/incomplete Rust edit summaries explicitly say pending and direct agents to diagnostic_updates. Telemetry recognizes the fixed reason code. Unit coverage and existing late-delivery tests pass. Live Rust acceptance remains pending. |
| CI friction isolation | ci.yml job env sets HUYANG_FRICTION: '0', covering ordinary, race and live Go test commands. |

The compact diagnostics wire shape is unchanged. No frozen tool-catalog fixture
needed regeneration; the fixed Rust reason-code allowlist and its transport
expectations changed together. Default current-body recovery is explicitly
`diagnostics {root, full: true}`.

## Focused acceptance

All commands ran via `chaldea exec --host agent-a` from this worktree.
Every Go test inherited `HUYANG_FRICTION=0`; intentionally enabled spool tests
use their own temporary spool. Job-local scratch is under
`$HOME/.cache/huyang-friction/m1-worker`, never host /tmp. Umask is 022.
Neovim 0.11.5 was downloaded only into job scratch and checked against SHA-256
`b2f91117be5b5ea39edd7297156dc2a4a8df4add6c95a90809a8df19e7ab6f52`.

Passing job: `3b032c68-5f1f-437c-ba96-c740867ebf24`.

Exact job command (the local caller also sets
`TMPDIR=/home/igor/.cache/huyang-friction/m1-worker`):

```sh
chaldea exec --host agent-a -- sh -ec '
umask 022
mkdir -p "$HOME/.cache/huyang-friction/m1-worker"
export TMPDIR="$HOME/.cache/huyang-friction/m1-worker" HUYANG_FRICTION=0
curl -fsSL https://github.com/neovim/neovim/releases/download/v0.11.5/nvim-linux-x86_64.tar.gz -o "$TMPDIR/nvim.tar.gz"
echo "b2f91117be5b5ea39edd7297156dc2a4a8df4add6c95a90809a8df19e7ab6f52  $TMPDIR/nvim.tar.gz" | sha256sum -c -
tar -xzf "$TMPDIR/nvim.tar.gz" -C "$TMPDIR"
export PATH="$TMPDIR/nvim-linux-x86_64/bin:$PATH"
go test ./internal/workspace ./internal/providerpool ./internal/handlers ./internal/mcpapi ./cmd/huyang -run "Diagnostic|Baseline|ProbeRejects|FirstTouch|PreEdit|LegacyProducer|OldAuthoritative|Rust|RecordDiagnostics|MissingEdited|StagedMulti|LateEvidence|PassingProject|ProjectCheck" -count=1 -timeout=4m
go test ./internal/service -run "TestDiagnosticRecovery|TestModernEditSpoolsDiagnosticState|TestDiagnosticsReportIsCompact|TestDiagnosticUpdatesAreDelivered" -count=1 -timeout=2m
nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_edit.lua
HUYANG_BASELINE_MEASURE=1 go test ./internal/providerpool -run TestDiagnosticBaselineTotalLatencyPopulatedLedger -count=1 -v -timeout=2m
go vet ./...
test -z "$(gofmt -l cmd internal)"
'
```

Go package results: workspace 2.690 s, providerpool 2.043 s, handlers 0.127 s,
mcpapi 0.011 s, cmd/huyang 0.009 s, service 1.457 s. Lua:
`unit_edit: OK`. Vet and formatting exited 0. Local `git diff --check`
also passed.

A final native-literal identity regression was added after that job snapshot.
Follow-up job `7ebd3e4d-cf4e-43fb-b8bf-a638d6dcf7b9` runs:

```sh
TMPDIR=/home/igor/.cache/huyang-friction/m1-worker \
chaldea exec --host agent-a -- sh -ec '
umask 022
mkdir -p "$HOME/.cache/huyang-friction/m1-worker"
export TMPDIR="$HOME/.cache/huyang-friction/m1-worker" HUYANG_FRICTION=0
go test ./internal/workspace -run "TestLiteralStageRetainsExactBaselineRevision|TestDiagnosticStateUpgrade|TestOldAuthoritative" -count=1
go test ./cmd/huyang -run TestProbeRejects -count=1
test -z "$(gofmt -l cmd internal)"
'
```

Follow-up passed: workspace 0.281 s, cmd/huyang 0.004 s, formatting clean; job exit 0.

## Baseline measurement

`TestDiagnosticBaselineTotalLatencyPopulatedLedger` measures the entire
`RecordDiagnosticBaseline` hook: real embedded Neovim RPC, decoding,
native document snapshot, ledger pruning, JSON serialization and atomic disk
persistence. The private kernel returns a deterministic single-document LSP
payload; no live server or semantic-analysis wait is included. Each condition
takes 30 warm samples. The populated ledger remains populated throughout.

| Retained findings before baseline | Final ledger bytes | Minimum | Median | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: | ---: |
| 0 | 40,499 | 18.873443 ms | 20.203148 ms | 29.337248 ms | 31.441351 ms |
| 1,000 | 1,752,314 | 29.274385 ms | 32.029564 ms | 39.528068 ms | 45.082221 ms |

This supersedes the earlier kernel-only ~0.3 ms claim. It is baseline-hook
overhead, not total edit latency or a production-latency guarantee. Multiple
loaded baseline documents each incur ledger work; that scaling is not measured.
The pre-existing two-second RPC timeout remains a worst-case limitation.

## Integration prerequisites and nine-language outcomes

No live all-nine run was attempted, per the lead's revised scope. Initial
agent-a prerequisite job `3de74d1c-8813-413e-861e-0e7216367e94` found
Neovim 0.10.4 and about 17 GB available memory. None of the seven server
executables was on PATH. The separate probe worker supplies pinned job-local
tools; there is no host-install authorization.

| Language | Outcome in this worker | Required executable(s) |
| --- | --- | --- |
| Go | Pending integrated live run | gopls |
| Rust | Pending integrated live run | rust-analyzer, cargo/rustc |
| Python | Pending integrated live run | pyright-langserver, Node/Python |
| TypeScript | Pending integrated live run | typescript-language-server, TypeScript/Node |
| JavaScript | Pending integrated live run | typescript-language-server, TypeScript/Node |
| Java | Pending integrated live run | jdtls, supported JDK |
| C++ | Pending integrated live run | clangd |
| C | Pending integrated live run | clangd |
| Bash | Pending integrated live run | bash-language-server, shellcheck/Node |

Set `HUYANG_LANGPROBE=1 HUYANG_LANGPROBE_STRICT=1 HUYANG_FRICTION=0`
for integrated acceptance. Strict mode fails on missing servers and on a
language filter; no skipped language is passing. Use a private binary,
socket and state directory with the setup worker's PATH. Versionless TS/JS/Java
clean evidence may remain honestly provisional; never count that as proven clean.

## Earlier attempts and limitations

- `e8ad89fc-a2f8-4133-9968-3fe8082e6898`: failed scratch bootstrap
  used read-only /root, and system Neovim 0.10.4 is incompatible. No passing
  acceptance claimed.
- `3cfef282-1e16-42f6-879c-96e629896715`: broader test attempt failed on
  Neovim 0.10.4, restrictive default job umask, long Unix socket paths and
  then-unupdated baseline/Rust fixture expectations. Those diagnostic fixture
  expectations were fixed; final focused job uses Neovim 0.11.5 and umask 022.
- `fdc69e6a-da60-4b2b-b5e7-8ad7cba705be`: short symlink TMPDIR attempt
  failed because tests compare lexical absolute paths to Huyang's canonical
  roots; no passing acceptance claimed. Use the canonical scratch directory.
- `0d3db04c-f9f3-4509-8f2e-f6a94e9fd0b0` and
  `991637dd-7ceb-416e-82e7-7b89fe6548a6`: definitive Chaldea busy
  refusals, nothing dispatched.

Exact argv for every attempt, including the early failures, is retained in the
local Chaldea journals under `~/.local/state/chaldea/exec/journal/<job-id>.json`.
The broad attempts used `go test ./internal/workspace ./internal/providerpool
./internal/handlers ./internal/service ./cmd/huyang` with HUYANG_FRICTION=0;
this was not a full `./...` run.

The full suite remains for lead integration. Some existing Unix socket test names
exceed the 108-byte socket-address limit under the required long canonical TMPDIR;
the focused recovery test uses a short test name and passes both transports.
Do not treat the failed broader attempts as a full-suite pass. No actual old-binary
downgrade launch, all-nine live acceptance, race suite, push or CI run is claimed.

## Review round 1 fixes (against aab8743)

Worktree: `/home/igor/.t3/worktrees/huyang/huyang-friction-m1-diagnostics`.
Branch: `huyang-friction/m1-diagnostics`. Clean parent:
`aab8743a11132936f773f35bfcbc16bcf349c6cb`; GitHub base remains `ef16bef`.

- Block A: delayed pulls now enter the same bounded, coalesced observation queue
  as pushes. Collection rechecks the request's retained pending identity, valid
  loaded buffer, path, changedtick, LSP version and disk currency. Superseding
  bytes or plan invalidation drop the observation. Any currently open producer
  progress vetoes late completion; a valid pending pull can complete after
  progress ends without another server response.
- Block B: baselines only seed or refresh findings and never resolve omissions.
  Baseline publication checks veto all currently open producer progress,
  including tokens opened before the call. Exact native pre-edit document
  revision proof is unchanged; insufficient baseline evidence keeps
  `baseline_missing`.
- Regressions cover queued empty and positive pulls superseded after their
  callbacks, valid late delivery, collection-time progress, an already-open
  baseline progress token, empty/partial baseline omissions, and no false
  resolved/new cycle through `RecordDiagnosticBaseline`. The providerpool
  regression also checks that vetoed baseline evidence cannot establish the
  exact pre-edit revision proof.

Static review and `git diff --check` passed for this fix round. Runtime
validation is explicitly deferred. The parent reserved the
sole local Chaldea exec slot for probe bootstrap and has not granted this worker
a slot. No builds, Lua/Go tests, vet, or test jobs were dispatched by this worker;
the passing jobs above describe the earlier implementation only. Huyang's
untrusted-root preparation does not constitute test validation. The parent
should run the diagnostic regressions and Lua suite through
`chaldea exec --host agent-a`, using its pinned Neovim 0.11.5 PATH, canonical
scratch under `~/.cache/huyang-friction/`, and `HUYANG_FRICTION=0` for every
Go test. Focused Go selection:
`go test ./internal/workspace ./internal/providerpool -run 'Baseline|LateEvidence' -count=1 -timeout=2m`.
Lua selection:
`nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_edit.lua`.

Nonblocking follow-ups remain outside this focused fix: pending-late attribution
has no TTL; a late attach with an unknown expected version remains unaccepted;
unknown disk fingerprints and the RPC/native-snapshot interval can weaken
baseline binding during an external write; incomplete immediate publications
are not retained for later completion; Phase C's probe reannouncement exemption
needs separate judgment. No provider-cap/debug-lifecycle files were changed.
No push, PR, merge, installation, service restart, Ollama or evaluation occurred.
