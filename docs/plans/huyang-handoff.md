# Huyang implementation handoff

## Authority and invariant

- Repository: `https://github.com/iryzhkov/agent99`
- Standalone checkout: `/home/igor/Work/huyang`
- Branch: `feature/huyang`
- Reviewed base: `1c5302efe9ca51e1701e73df72d903f225fefe04`
- Base ancestry check: passed with
  `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`.
- Deployment boundary: no install, deployment, installed-plugin update, live MCP restart,
  live configuration/state mutation, push, or pull request is authorized.
- Dependency protocol: clean branch, committed checklist in the implementation plan, and this
  handoff. Exactly one ungated successor is queued after a successful stage.

## Current checkpoint

- Completed stage: S04 — Embedded provider production path.
- Starting commit: `bdd753e565e52323e73db99775c424364ac36f9f`.
- Completion commit: the `feature/huyang` commit containing this handoff.
- Predecessor reconciliation:
  - S03 is committed at `bdd753e565e52323e73db99775c424364ac36f9f`;
  - the reviewed base remains an ancestor and the worktree was clean after fetching origin;
  - the pinned library, spike tests, decision record, and recorded S03 gates reconcile with the
    committed tree. The prior “pending final commit” wording was stale documentation only.
- S04 exit gates achieved:
  - production embedded provider implements bootstrap/capability handshake, request-ID
    completion notifications, health checks, cancellation/deadlines, epoch-changing restart,
    bounded stderr, root ownership, and classified failures;
  - socket remains selectable as the default comparison and rollback backend;
  - headless, multi-workspace, debug, and injected provider-fault smoke suites pass against
    both selectors;
  - production trace coverage proves the embedded path starts no `nvim --server`,
    `--remote-expr`, or `--listen` polling subprocess;
  - targeted race tests, `make smoke`, `go test ./...`, `go vet ./...`, and
    `git diff --check` pass.
- Checklist: only S04 was marked complete in this stage.
- Exact next stage: S05 — Explicit workspaces and revisions.

## Predecessor artifacts

S04 reconciled and consumed these committed predecessor artifacts completely:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/huyang-s01-package-boundary.md`
- `docs/plans/huyang-s02-provider-seam.md`
- `docs/plans/huyang-s03-embedded-provider-spike.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

## S04 changes

- Added the production `internal/provider/embed` backend. It owns
  `nvim --embed --headless`, bootstraps the shipped Lua kernel, performs a version and
  capability handshake, dispatches completion notifications by request ID, reports health,
  retains bounded stderr, and exposes PID and epoch descriptors.
- Context cancellation and deadline expiry restart the whole provider generation before
  returning. Unexpected death fails all concurrent calls and is recovered lazily on the next
  call. Stable `provider.Failure` codes distinguish launch, bootstrap, compatibility,
  death, cancellation, deadline, and protocol failures.
- Added Linux cross-process root ownership with `flock`; other-platform files preserve the
  package build boundary without claiming an unsupported lock implementation.
- Added `AGENT99_PROVIDER_BACKEND=embed`; unset or `socket` remains the default rollback
  path, and explicit `AGENT99_NVIM` attachment remains socket-only.
- Extended the Lua kernel with protocol-v1 handshake and asynchronous
  `agent99/result` completion notification while retaining its socket entry points.
- Added provider backend/epoch workspace-open evidence and propagated backend cancellation
  semantics through the bridge.
- Added `internal/provider/embed/embed_test.go` coverage for handshake, health, trace,
  reordered completions, malformed completions, cancellation/deadline restarts, epochs,
  provider death, lazy recovery, bounded stderr, launch failures, and root locks.
- Updated smoke assertions for either backend and changed deliberately killed socket cleanup
  to terminate its owned PID directly. The former fresh `--remote-expr` cleanup client could
  abort in libuv when the server socket had already disappeared.
- Added `docs/plans/huyang-s04-embedded-provider.md` and marked only S04 complete.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test ./internal/provider/... ./internal/bridge` — exit 0 during implementation.
- `go test ./internal/provider/embed -count=1 -v` initially exposed a typed-nil bootstrap
  return in the new backend. After correction, all focused lifecycle/protocol tests passed.
- `go test -race ./internal/provider/embed ./internal/bridge -count=1` — final exit 0:
  `internal/provider/embed` passed in 2.799s and `internal/bridge` in 1.020s.
- `AGENT99_PROVIDER_BACKEND=embed bash tests/smoke.sh headless:workspace` — exit 0 after
  making workspace-open assertions descriptor-aware.
- `AGENT99_PROVIDER_BACKEND=embed bash tests/smoke.sh multi` initially exposed the missing
  cross-process root lock. After adding `flock` ownership and rebuilding the bridge, it
  exited 0.
- `AGENT99_PROVIDER_BACKEND=embed make smoke` — final exit 0; reported
  `headless: OK`, `multi-workspace: OK`, `debug: OK`, and `smoke: OK`.
- `make smoke` — final exit 0 on the default socket backend with the same four OK markers.
- `go test ./...` — exit 0; bridge, provider, embed, embedspike, and socket packages passed;
  the command package has no tests.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0; no output.
- `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`
  — exit 0.

Two intermediate smoke cleanup processes dumped core. `coredumpctl` identified their
command as a fresh `nvim --server <dead-socket> --remote-expr ...` cleanup helper and the
stack as a libuv abort; there was no OOM evidence. Replacing that cleanup helper with direct
PID termination removed the abort, and both final full smoke runs passed.

## Decisions and risks

- Cancellation is honestly advertised as `provider_restart`, not cooperative per-request
  cancellation. The pinned client has no context-aware per-request call API; S04 therefore
  uses provider-wide termination with explicit epoch change and deterministic restart.
- Completion dispatch is keyed by request ID. FIFO completion assumptions remain prohibited.
- Linux root lock files remain after unlock to avoid pathname/inode replacement races; an
  unlocked file is not a foreign owner.
- Production bootstrap accepts Neovim 0.11 and 0.12. S03 proved the transport on 0.11.4,
  0.12.1, and host 0.12.5; the S04 full runtime/LSP/DAP smoke matrix ran on host 0.12.5.
- The socket backend remains the default comparison oracle and immediate rollback path.
- S04 adds no document identity, revision, state-sequence, scheduler, or successor-stage
  behavior. Those remain gated by S05 and later stages.
- No install, deployment, live MCP/config/state mutation, push, or pull request occurred.
- Exact next stage: S05 — Explicit workspaces and revisions.
