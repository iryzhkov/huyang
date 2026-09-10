# Huyang implementation handoff

## Authority and invariant

- Repository: `https://github.com/iryzhkov/agent99`
- Standalone checkout: `/home/igor/Work/huyang`
- Branch: `feature/huyang`
- Reviewed base: `1c5302efe9ca51e1701e73df72d903f225fefe04`
- Base ancestry check: passed; `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`
- Deployment boundary: no install, deployment, live MCP restart, live configuration/state
  mutation, push, or pull request is authorized.
- Dependency protocol: clean branch, committed checklist in the implementation plan, and this
  handoff. Exactly one ungated successor is queued after a successful stage.

## Current checkpoint

- Selected stage: S03 — Embedded provider decision spike
- Starting commit: `943dde3705d855f82b4d3e682fd1e2fbbe49f475`
- Exit achieved:
  - `github.com/neovim/go-client/nvim` v1.2.1 is pinned and exercised from a
    disposable test package with `nvim --embed --headless`;
  - hermetic spike coverage proves inbound requests, unsolicited notifications, cancellation,
    out-of-order completions, provider death, and bounded stderr handling;
  - Neovim 0.11.4, 0.12.1, and host 0.12.5 pass, with startup, warm-call overhead, memory,
    and stderr behavior measured against the socket reference topology;
  - the committed S03 decision record chooses the low-level library transport with
    Huyang-owned lifecycle; no disposable binary remains in the repository;
  - targeted S03 tests, `make smoke`, `go test ./...`, `go vet ./...`, and
    `git diff --check` pass.
- Status: complete pending the final S03 commit and clean-tree confirmation.
- Next stage after successful S03: S04 — Embedded provider production path.

## Predecessor artifacts

S03 reconciled and consumed these committed predecessor artifacts completely:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/huyang-s01-package-boundary.md`
- `docs/plans/huyang-s02-provider-seam.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

The S02 commit is `943dde3705d855f82b4d3e682fd1e2fbbe49f475`. Its provider boundary,
socket confinement, frozen-fixture comparison, history, full gates, and clean checkpoint
reconcile with the current tree.

## S03 changes

- Pinned `github.com/neovim/go-client` v1.2.1 in `go.mod` and `go.sum`; the
  selected tag resolves to commit `37f6413db894a93ee7aa43ceafe771a8c13e9222`.
- Added the test-only `internal/provider/embedspike` package. It owns no production code and
  exercises `nvim.New` over an explicitly owned `nvim --embed --headless` child.
- Covered headless startup, embedder client identification, inbound blocking requests,
  unsolicited notifications, reordered completion notifications, provider-wide cancellation,
  concurrent-call failure on provider death, and bounded stderr draining.
- Added an opt-in bounded comparison for startup, trivial warm-call overhead, Linux RSS, and
  stderr retention against the socket reference topology.
- Added `docs/plans/huyang-s03-embedded-provider-spike.md`, choosing the pinned library's
  low-level transport while retaining process lifecycle and cancellation policy in Huyang.
- Downloaded release archives only into `/tmp/huyang-s03.ZKlt9D` for the version matrix. No
  binary, production embedded backend, Lua protocol, installation, deployment, live
  configuration/state, push, or pull request was added or performed.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test ./internal/provider/embedspike -v` initially failed to compile because the pinned
  library names the constant `nvim.EmbedderClientType`; after correction it reached one
  runtime failure because `nvim_set_client_info` requires non-nil dictionaries. Both were
  corrected without changing the chosen boundary.
- `go test -race ./internal/provider/embedspike -count=1` initially found one test-harness
  race between `Cmd.Wait` and reading `Cmd.ProcessState`. The harness now observes its
  completion channel instead; the rerun exited 0 with `ok`.
- `go test ./internal/provider/embedspike -count=1 -v` — exit 0 on host Neovim 0.12.5;
  all six protocol/lifecycle tests passed and the opt-in measurement test skipped.
- `HUYANG_NVIM=/tmp/huyang-s03.ZKlt9D/v011/bin/nvim go test
  ./internal/provider/embedspike -count=1 -v` — exit 0 on Neovim 0.11.4.
- `HUYANG_NVIM=/tmp/huyang-s03.ZKlt9D/v012/bin/nvim go test
  ./internal/provider/embedspike -count=1 -v` — exit 0 on Neovim 0.12.1.
- `HUYANG_NVIM=<0.11.4 path> HUYANG_SPIKE_MEASURE=1 go test
  ./internal/provider/embedspike -run TestMeasureTransports -count=1 -v` — exit 0;
  embed/socket startup 6.871/7.529 ms, warm call 0.096/2.870 ms, RSS
  13,640/13,052 KiB.
- The equivalent 0.12.1 measurement — exit 0; startup 6.984/8.033 ms, warm call
  0.100/2.983 ms, RSS 13,800/13,180 KiB.
- The equivalent host 0.12.5 measurement — exit 0; startup 7.632/8.501 ms, warm call
  0.094/3.661 ms, RSS 13,608/12,572 KiB.
- `make smoke` — exit 0; ended with `debug: OK` and `smoke: OK`. The shell printed its
  expected post-SIGKILL job notification after the success line.
- `go test ./...` — exit 0; command package had no tests and bridge/provider/socket/spike
  packages passed.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0; no output.

Release archives were fetched from official Neovim GitHub release URLs and extracted beneath
`/tmp`; the repository contains no downloaded or generated binary.

## Decisions and risks

- S03 chooses `github.com/neovim/go-client/nvim` v1.2.1; no blocking protocol
  incompatibility or reason to write a MessagePack implementation was found.
- S04 must use `nvim.New` with an explicitly owned child rather than
  `nvim.NewChildProcess`: Huyang needs the process handle and an independently drained,
  bounded stderr stream for health, cancellation, parent death, and restart evidence.
- v1.2.1 has no context-aware per-request API. Until the Lua kernel implements and
  acknowledges request-specific cancellation, cancellation must terminate the provider,
  fail all in-flight requests explicitly, increment the epoch, and restart.
- Completion dispatch is keyed by request ID. Tests intentionally observe a later-submitted
  completion arriving first; FIFO assumptions are prohibited.
- The 0.11–0.12 transport matrix uses `--clean -u NONE` and does not prove runtimepath,
  user-config, LSP, DAP, or agent99-kernel parity. Those are S04 gates, not missing S03 work.
- Measurements are small local medians and single RSS samples. They justify the transport
  decision but do not replace S04 tracing or production p95 telemetry.
- The socket backend remains the comparison oracle and rollback path.
- The user-approved single-host serial bootstrap remains in effect. No install, deployment,
  live MCP/config/state mutation, push, or pull request occurred.
- Exact next stage: S04 — Embedded provider production path.
