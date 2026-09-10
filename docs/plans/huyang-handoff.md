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

- Selected stage: S02 — Provider seam with socket reference backend
- Starting commit: `5a1a0f74271b29a5d8a1412e56e907b24fe18412`
- Exit achieved:
  - provider request/result, health, lifecycle, identity, capability, role, and
    analysis-profile contracts wrap the existing reference implementation;
  - deterministic provider IDs and profile routing select one primary on the hot path while
    modeling complementary providers and declared fallbacks;
  - the current runtime and tests route through the provider interface without changing public
    tool names, schemas, responses, or socket behavior;
  - request context includes cancellation and deadline fields, and the reference backend
    explicitly reports in-flight cancellation as unsupported;
  - direct socket/start-poll assumptions are confined to `internal/provider/socket`;
  - targeted provider tests, `make smoke`, `go test ./...`, `go vet ./...`, and
    `git diff --check` pass.
- Status: complete pending the final S02 commit and clean-tree confirmation.
- Next stage after successful S02: S03 — Embedded provider decision spike

## Predecessor artifacts

S02 reconciled and consumed these committed predecessor artifacts completely:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/huyang-s01-package-boundary.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

The S01 commit is `5a1a0f74271b29a5d8a1412e56e907b24fe18412`. Its package-boundary checks,
frozen-fixture comparison, history, and clean checkpoint reconcile with the current tree.

## S02 changes

- Added `internal/provider` contracts for provider descriptors, request context, results,
  health, lifecycle, capabilities, roles, registrations, and named analysis profiles.
- Added deterministic primary/complementary/fallback routing by language and capability;
  ordinary legacy calls route through the configured primary rather than choosing a provider.
- Extracted the complete Neovim socket/start-poll implementation into
  `internal/provider/socket`, including process ownership, RPC, autosave, shutdown, foreign
  instance detection, stale endpoint cleanup, timeouts, and Linux parent-death behavior.
- Added a small backend composition factory so workspace, lifecycle, and routing code depend
  only on provider contracts.
- Routed embedded-editor, one-shot tool, agent, and standalone MCP calls through the same
  provider request path. Legacy client injection, result rendering, autosave, socket/PID
  compatibility fields, and timeout wording remain intact.
- Added `docs/plans/huyang-s02-provider-seam.md` and provider/profile/backend tests.
- Added no embedded MessagePack client, provider decision, public API, service, workspace
  identity, transaction, dependency, Lua, installation, or deployment change.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test ./internal/provider/... ./internal/bridge` — exit 0;
  `ok agent99/internal/provider`, `ok agent99/internal/provider/socket`, and
  `ok agent99/internal/bridge`.
- `agent99 grep(pattern="exec\\.Command.*nvim|--server|--remote-expr|Agent99Rpc(Start|Poll)|remoteExpr|\\.Socket", glob="internal/**/*.go")`
  — every implementation hit is under `internal/provider/socket/socket.go`; no workspace-core
  transport primitive remains.
- `git diff --exit-code 5a1a0f74271b29a5d8a1412e56e907b24fe18412 -- docs/plans/fixtures/huyang-v1alpha1 docs/plans/huyang-tools-v1alpha1.md docs/plans/huyang-s00-baseline.md docs/plans/huyang-s00-model-selection.md docs/plans/huyang-s01-package-boundary.md`
  — exit 0; all frozen predecessor artifacts are byte-identical.
- First `make smoke` attempt — exit 2: one timing-sensitive delayed-diagnostic assertion
  observed its verdict 139 ms earlier than expected; every other suite case passed.
- Two diagnostic-only reproductions, `python3 tests/drive_headless.py verdict`, each exited 1
  because lua_ls did not attach in the isolated group. No source or configuration was changed
  in response to those environment/timing outcomes.
- Final `make smoke` rerun — exit 0; ended with `debug: OK` and `smoke: OK`, including the
  complete headless verdict, multi-workspace, and debugger suites.
- `go test ./...` — exit 0; command package compiled, and bridge/provider/socket packages
  passed.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0; no output.

The final commit and clean-tree checks must be performed after this handoff is saved. The next
task must reconcile them rather than trusting this sentence.

## Decisions and risks

- The provider contract is transport-independent and carries stable descriptor, health,
  request/result, save, close, and death-notification semantics. Later stages can add evidence
  fields without changing the call boundary.
- Provider IDs derive from backend kind and canonical root; PID and ephemeral endpoint are
  compatibility metadata and never identity.
- The default profile has the reference provider as primary for all current capabilities.
  Complementary and fallback providers are modeled and deterministically ordered, but legacy
  calls neither aggregate them nor silently fail over.
- Request deadlines are populated at the bridge boundary. The socket backend advertises
  `cancellation: unsupported` because killing a polling helper cannot prove the Lua coroutine
  stopped; S03 must test direct embedded cancellation rather than strengthening this claim.
- The legacy SHA-1 socket filename prefix, process arguments, RPC payload, polling intervals,
  tool timeouts, save expression, shutdown behavior, and response fields are preserved.
- The first smoke run exposed an existing timing-sensitive delayed-verdict check; the complete
  rerun passed without code changes. S03 should continue treating delayed diagnostic timing as
  evidence-sensitive, not as transport completion.
- The user-approved single-host serial bootstrap remains in effect. No install, deployment,
  live MCP/config/state mutation, push, or pull request occurred.
- Exact next stage: S03 — Embedded provider decision spike.
