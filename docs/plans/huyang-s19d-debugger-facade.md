# Huyang S19D compact modern debugger facade

## Scope and result

S19D adds the modern debugger surface after S19's legacy-wrapper migration. It
does not migrate additional wrappers, perform the S20 compatibility evaluation,
or change installation, deployment, or live configuration.

The `full` and `debug` modern profiles now advertise four tools:

| Tool | Closed actions | Proven provider operations |
| --- | --- | --- |
| `debug_session` | `start`, `attach`, `restart`, `stop` | `debug_launch`, `debug_attach`, `debug_stop` |
| `debug_breakpoints` | `list`, `set`, `remove`, `clear` | `debug_breakpoints`, `debug_breakpoint` |
| `debug_control` | `continue`, `pause`, `step_over`, `step_into`, `step_out`, `run_to` | `debug_continue`, `debug_wait`, `debug_step` |
| `debug_inspect` | `threads`, `stack`, `scopes`, `variables`, `evaluate` | existing DAP inspection plus internal `debug_threads` and `debug_scopes` operations |

The modern catalogs do not advertise any other legacy debugger tool. The legacy
MCP remains unchanged for compatibility and still gates its debugger roster
behind `AGENT99_DEBUG`.

## Source identity and stop context

Breakpoint `set`/`remove`, control `run_to`, and session
`initial_breakpoints` accept the shared revision-bound target forms:
opaque handle, exact file-range locator, or symbol locator. Debugger source
locations are converted back to registered workspace handles and exact
file-range locators.

Every provider result is normalized into the common modern envelope. Stopped
session/control results preserve the adapter state and reason, top source
location, compact locals, tracked expressions, newly captured output, and
stale-source data under `changes_since_previous_stop`. Stack results carry a
source location per frame when mapping succeeds. Missing source mappings produce
partial coverage without discarding the usable debugger response.

## Safety and degradation

`debug_inspect/evaluate` defaults to `read_only`. Because DAP cannot prove an
expression is pure, that policy returns `unavailable` with
`approval_required` and does not call the provider. The caller must explicitly
select `allow_side_effects`; successful results then disclose that debuggee
state may have changed.

Missing nvim-dap, adapters, executables, runtime support, or source mappings
return structured `unavailable`/partial coverage and a
`workspace_inspect` repair action. Cancellation and deadline failures retain
their provider failure codes. Modern debugger providers are closed when the MCP
server exits, so debuggee cleanup follows the established provider lifecycle.

## Compactness and call comparison

The frozen unit comparison serializes the actual descriptors used by each
catalog:

- modern facade: 4 tools and 7,505 bytes;
- legacy debugger roster: 13 tools and 8,383 bytes;
- reduction: 9 advertised tools and 878 bytes (10.5%).

A breakpoint-first launch previously required `debug_breakpoint` followed by
`debug_launch`. `debug_session/start` accepts initial breakpoints and returns
the first stop's location, locals, output delta, tracked values, and stale-source
coverage in one call. This reduces the representative setup-and-orient workflow
from two calls to one without dropping evidence.

## Tests

- Go fixtures cover all 19 facade actions, initial breakpoint ordering,
  revision-bound handle resolution, stop enrichment, explicit evaluation
  policy, degraded adapter handling, closed per-action arguments, catalog
  membership, and the descriptor-size comparison.
- The real Delve smoke test starts the modern `debug` profile, verifies the
  four-tool facade and absence of the legacy roster, launches with a symbol
  breakpoint, inspects threads/scopes/variables, checks both evaluation
  policies, steps with stop context, and terminates cleanly.
- Existing legacy debugger smoke coverage remains intact.
