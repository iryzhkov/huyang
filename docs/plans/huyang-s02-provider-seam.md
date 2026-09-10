# Huyang S02 provider-seam record

Captured: 2026-09-10
Stage: S02 — Provider seam with socket reference backend
Starting commit: `5a1a0f74271b29a5d8a1412e56e907b24fe18412`

## Boundary

`internal/provider` now owns the transport-independent contract:

- stable provider descriptors and deterministic IDs;
- request context, operation arguments, results, health, save, close, and death notification;
- explicit deadline and cancellation-support fields;
- semantic capability, role, language, and named analysis-profile routing;
- one primary hot-path route, complementary providers, and declared fallbacks.

The workspace core holds only `provider.Provider` and `provider.AnalysisProfile`. A small
composition factory selects the reference backend. Core routing, lifecycle, calls, and
autosave no longer know how a provider process is started or how requests travel.

## Socket reference backend

`internal/provider/socket` contains the complete previous transport behavior:

- `nvim --headless --listen` process creation and parent-death behavior;
- legacy socket naming, foreign-owner detection, and stale endpoint cleanup;
- `Agent99RpcStart` / `Agent99RpcPoll` calls through `nvim --server --remote-expr`;
- existing operation-specific timeouts and response decoding;
- exact `agent99.lsp.save_all()` autosave and debugger shutdown expressions;
- graceful close with the existing bounded force-kill fallback.

The compatibility `open_workspace` result still renders `socket` and `pid` from generic
provider descriptor metadata. Tool names, schemas, response text, environment selection,
timeouts, and filesystem effects remain unchanged.

## Profiles and cancellation

The default runtime profile registers the socket backend as the primary provider for all
languages and current capabilities. Profile tests freeze deterministic primary,
complementary, and fallback selection without adding per-call provider choice to the public
API.

Calls now carry a provider request ID, actor, deadline, and cancellation-support state.
The socket backend reports `unsupported`: abandoning a `nvim --server` helper cannot
prove that the already-started Lua coroutine stopped. This stage therefore preserves the
old bounded timeout behavior and does not claim in-flight cancellation. S03 will test direct
embedded cancellation and completion behavior behind the same contract.

## Compatibility and risk

- Provider IDs derive from backend kind and canonical root, not PID or ephemeral endpoint.
- The legacy SHA-1 socket filename prefix is intentionally preserved for cross-process
  duplicate-root detection.
- Complementary providers are modeled and discoverable but not invoked by legacy tool calls;
  aggregation belongs to later evidence stages.
- Declared fallbacks are returned by profile routing but are not silently selected after a
  primary failure.
- The reference backend remains the behavioral oracle for S03 and S04.
