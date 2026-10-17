# Experimental execution evidence contract

X20 freezes the meanings and resource ceilings below for
`huyang.workspace/v1alpha2`. Existing v1alpha1 tool descriptors and envelope
schemas are unchanged. These are contracts for the following stages, not an
advertisement that a graph or trace operation already exists.

## Answers

| Status | Required evidence |
| --- | --- |
| static_possible | A candidate path supported by static facts; not proof it runs. |
| observed | Recorded events in one named execution. |
| not_observed | A named trace lacks the event; no claim about other executions. |
| statically_unreachable | No path with complete required adapters and no relevant cap, unresolved frontier, cancellation or source ambiguity. |
| unknown | Required coverage is absent, incomplete or ambiguous. |

`observed` and `not_observed` were produced only from debugger traces. The debugger
and its trace stack were removed on 2026-10-17 (see "Removed: debugger traces" below),
so the remaining static operations answer `static_possible`, `statically_unreachable`
or `unknown`.

Divergence is the earliest safely aligned difference between two recordings.
A causal candidate needs both dependency and runtime evidence. Causal is reserved
for exact evidence such as a watched mutation or recorded assignment. Sampling,
temporal order and static reachability alone never establish causality.

## Budgets

`workspace.DefaultGraphBudget` is the executable contract: 16 candidate paths,
64 edges of depth, 4,000 nodes, 20,000 edges, 8 MiB encoded graph bytes,
256 nodes per expanded function, 20 seconds of acquisition, two recursive
expansions and one visit per cycle. The trace ceilings (10,000 events, 128
values, 1 KiB per value, 64 KiB of values) remain budget fields because the
budget takes part in graph identity; no operation records a trace any more.
Requests can lower ceilings, never silently raise them.

Every reached ceiling names its limit and preserves a partial answer. A path
enumeration cap does not erase paths already found; a capped negative answer
is unknown. Cancellation and adapter failure are coverage gaps. Cycles retain
their edges even when further traversal stops.

## Shapes and identity

The result schema and examples in `tests/fixtures/execution/contracts` freeze
the shared evidence header. Stage-specific path, trace and comparison payloads
are added by their owning stages; X20 does not freeze invented payloads for
operations that have not yet been implemented.

X21 adds experimental execution_graph under provider_read scheduling. Its snapshot
extends AnalysisSnapshot with an execution graph, a content-derived revision and
an explicit canonical/prepared domain in the identity. The initial live adapter
records unresolved source boundaries and always reports incomplete call coverage.
The stable catalogs remained byte-identical. The existing bounded analysis
cache now copies on insertion and retrieval; live graph queries currently rebuild
rather than retain snapshots. Graph bytes reserve 256 KiB for bounded coverage and
risk metadata, so a caller lowering the byte ceiling must allow at least 512 KiB.
The path walker is an internal API until X24 adds path_explain. This narrows the original
"schemas frozen" gate to the common header and request budgets.

Each fact names its content revision, revision-bound source handles, producer
and version, confidence, coverage, classification (static, observed or inferred)
and evidence IDs. Graph identity includes workspace ID and epoch, content
revision, canonical/prepared routing, profile, configuration hash, producer
versions and budget. It extends AnalysisSnapshot; it is not a second unrelated
cache. A runtime overlay never mutates the historical static snapshot.

Static queries use provider_read scheduling and never launch a program.
Unresolved and external targets remain explicit; source handles must not leak
sandbox paths.

## Static acquisition (X22)

execution_graph now acquires bounded call-hierarchy facts and method implementation
alternatives, conservative parser candidates, and explicit import/unresolved
boundaries. References remain data dependencies and are excluded from execution
path traversal. Every internal provider location requires a buffer hash matching
the captured source. Source handles are revision-bound in the existing registry.

use_provider=false skips the embedded acquisition batch; native Go parser and
import fallback remain. Non-Go syntax acquisition requires the embedded provider.
All adapters currently report incomplete coverage: dynamic dispatch, callbacks,
reflection and external targets cannot justify a negative reachability proof.
The batch allows 32 files, 128 symbols, 1024 edges and 1024 references within
1 MiB of facts, 16 hierarchy items and 1500 ms per server request. Provider
acquisition uses at most six seconds of the twenty-second graph deadline.

## Selected flow expansion (X23)

expand_functions accepts up to 16 function IDs from an execution graph. The
selection enters snapshot identity. Go expands each selected body into at most
256 flow nodes with branches, exits, call sites and async boundaries. Conditions
include expression hashes and identifiers; constant results require an
identifier-free constant expression. Jump targets, case exhaustiveness, defer
order, recovery, expression evaluation order and synchronization remain partial.

TypeScript/Python/Lua provide bounded syntax slices with explicit incomplete CFG
coverage. Their syntax_next links never count as execution paths. These slices
describe conditions, exits and async syntax without claiming runtime ordering.
Provider buffer hashes and source handles follow the X22 source contract.
The selected-body expansion is broader than a minimal call-site slice and
does not replace the interprocedural call summary.

## Static path explanation (X24)

path_explain accepts function names/IDs or path/name locators and bounded
max_paths/max_depth. It automatically expands selected functions and returns
ordered paths with the supporting snapshot. Ranking applies to the bounded
candidates; it cannot guarantee globally optimal paths after a cap. Path IDs
are stable identities supplied with their evidence, not registry lookup handles.
The combined mode, which overlaid a recorded debugger trace on the static paths, was
removed with the debugger; `mode` and `trace_id` are no longer arguments.

A statically_unreachable answer requires the closed Go adapter: an import-free
package containing only nullary functions, direct calls, bare returns and
constant-boolean branches. Values, callbacks, channels, methods, imports,
compiler/line directives, assembly, omitted inputs and all caps refuse proof.
The response names this narrow package scope. Both branch alternatives remain
in the overapproximation. General dynamic absence stays unknown.

revision/plan_id routes execution queries into the held prepared tree using
native Go/import fallback. Prepared psrc_ source handles share normal bounded
retention but require read with that prepared revision; canonical resolution
refuses them. Source hashes, epoch and held preparation are rechecked.
path_unreachable can prove an unexported non-entry target in a closed Go main
package; other cases retain conservative call refutation or unknown.
The frozen v1alpha1 descriptor's older invariant prose is unchanged.

## Removed: debugger traces (X25-X29)

X25-X29 specified observed debugger traces, the static/observed overlay of
`path_explain mode=combined`, branch and absence explanations, native mutation
facts and value origins, and completed-trace comparison. All of them were reached
only through the four debugger tools, which no agent called in the 30 days before
2026-10-17. The tools, the trace stack, the `debug` and `debugger` profiles and the
nvim-dap integration were removed together on that date. Trace records left under
`execution-traces/` in an older state directory are no longer read. The text of
those sections is in the Git history of this file.

## Integrated hardening and release scope (X30)

The integrated live flow acquires a graph, explains candidate paths, prepares a
call removal, proves the narrow path_unreachable invariant and inspects the
semantic change summary without changing canonical source; the closed proof
grammar intentionally excludes runtime input processing.

Execution APIs remain experimental. General dynamic/non-Go unreachability,
complete branch history and causal origins are not graduated by these gates.
Existing resource, source, epoch, preparation, provider-recovery and concurrency
checks remain part of the full suite. The separate read byte-bound and verbose
status defects remain deferred to the planned usability and bug investigation.

## Diagnostic evidence (M1)

Immediate and late push evidence follow the same completeness rule: the
publication must cover the expected document version, an empty push must have
150 ms of quiet, and work-done progress started for that edit vetoes completion.
Late pushes and pulls recheck buffer validity, loaded state, path, changedtick,
LSP version and disk currency at collection. Any currently open producer progress
vetoes late completion. Observations are coalesced by producer/document before
ledger insertion; an intermediate empty set cannot resolve a superseding positive
observation.
Unversioned positives are provisional and unattributed; unversioned emptiness
cannot prove resolution. Rust uses `rust_diagnostics_pending` when semantic proof
is incomplete and delivers late findings via `diagnostic_updates` on later calls.

Baselines only seed or refresh findings; they never resolve omitted findings.
The existing `persisting` notice covers both stale-to-current refreshes and
first-observed preexisting baseline findings. These notices carry finding bodies
through `diagnostic_updates`; already-current repeats are silent. A baseline
finding returning after verified resolution emits the existing `new` notice,
clears historical attribution and has no mutation transaction. Baseline records
contribute zero `DiagnosticReport.New` entries, including recurrences, so the
upcoming edit's introduced count and summary exclude them. A later diagnostics
query can still report a recurrence as a new transition since its cursor.
Any currently open producer progress vetoes a baseline, even if it started before
the call. Baseline validity is bound to the exact pre-edit document revision,
including object identity and content. An older authoritative coverage dimension
alone is insufficient after checkout or external writes. Findings without a matching
baseline retain `baseline_missing`. Refreshing a stale finding produces
`persisting`, never a new-finding transition. Staged multi-document counts sum
staged observations without changing the canonical current set.

Baseline ingestion filters publication/native-proof failures before ledger processing,
then validates and processes the accepted batches under one lock, prunes once and
persists once per RPC. A failed save restores the previous in-memory state and cursor.
Reading native baseline revisions does not publish separate per-buffer observations.
The provider RPC and native disk read remain separate snapshots: the existing narrow
external-write race is not an atomic proof and remains a follow-up.

Deduplication reuses immutable stored proof, never skips lifecycle processing. Its
key covers provider, document, dimension, producer/version, exact native revision,
finding placement and multiplicity, and equivalent publication proof. Canonical and
staged evidence remain separate. Items, dimensions and provider dimensions each
retain at most eight recent references; older unreferenced proof follows the existing
age/count retention. Exact preimage lookup fails closed when old proof is missing;
it never substitutes a newer revision. Retained preparation verification and invariant
IDs protect their exact payloads. Replayed receipts preserve the original envelope
independently of diagnostic retention; an expired evidence ID fails retrieval and
is never rebound to a new observation.

The compact `diagnostics` result is a notice delta. `since` acknowledges notices
workspace-wide; after acknowledgement or reconnect, `full: true` recovers current
finding bodies under `current`. This recovery does not reannounce them as new.
The persisted diagnostic ledger is version 2. Version-1 data is migrated and the
new header is saved before use; version-1 binaries reject it. Back up state before
upgrade if rollback must preserve diagnostic history. Newer unknown ledger
versions are refused without rewriting them. The tool API namespace is unchanged.

## Fixtures

The Go fixture has direct and indirect calls, an interface method, branches,
early return, recursion, a goroutine and channel, external library calls, and
passing/failing executions. Passing prints "4 2"; failing prints "-1 2" and
exits 1. The first input difference is in main, and the distinguishing downstream
branch is guarded's negative-input return.

TypeScript includes a barrel export, callback and promise; Python includes a
generator, exception and async boundary; Lua includes a coroutine and a
deliberately unresolved global. Their presence does not certify that an adapter
covers them. The live gate opens every fixture through the daemon, records
canonical hashes and tool/provider versions, and verifies read-only access.
Later stages must add assertions about the actual graph behavior.
