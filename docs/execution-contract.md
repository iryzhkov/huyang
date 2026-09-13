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

Divergence is the earliest safely aligned difference between two recordings.
A causal candidate needs both dependency and runtime evidence. Causal is reserved
for exact evidence such as a watched mutation or recorded assignment. Sampling,
temporal order and static reachability alone never establish causality.

## Budgets

`workspace.DefaultGraphBudget` is the executable contract: 16 candidate paths,
64 edges of depth, 4,000 nodes, 20,000 edges, 8 MiB encoded graph bytes,
256 nodes per expanded function, 20 seconds of acquisition, 10,000 events per
trace, 128 retained values, 1 KiB per value, 64 KiB of values per trace,
two recursive expansions and one visit per cycle. At most 16 traces are retained.
Values are disabled by default. Value-enabled traces require redaction and
mode-0600 storage. Requests can lower ceilings, never silently raise them.

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
The four stable catalogs remain byte-identical. The existing bounded analysis
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
Trace capture uses external_job scheduling and explicit debugger initiation.
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
Combined mode requires an explicit completed trace from matching canonical source and workspace epoch; prepared overlays are refused.

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

## Observed debugger traces (X25)

The experimental four-tool debugger facade accepts trace_policy on start/attach
and debug_inspect action=trace with an optional trace_id. A trace has a random
workspace-scoped identity and, once finalized, an immutable content digest.
The stable catalogs are unchanged. Traced launches and debugger control use
external_job scheduling; trace inspection is pure_read.

stops records user-visible stops. path installs owned temporary breakpoints at
selected source targets; conditions additionally permits explicitly requested
bounded locals. Values remain off unless capture_values=true. These modes
require operator control to continue between stops, do not evaluate condition
expressions, and do not promise complete instruction or branch coverage.
mutations currently returns trace_capability_unavailable.

Events retain goroutine/thread identity, bounded stack frames, launch-source
hashes and revision-bound handles when mapping succeeds. Missed stop sequence
numbers and event/value caps are disclosed. Explicit executable files receive
a SHA-256 fingerprint when bounded acquisition succeeds; adapter-managed build
identity remains unverified. Even a fingerprint does not prove correspondence
between the binary and launch sources. Changed source or epoch makes mapping
stale. Arguments, environment values and debugger output are never persisted.

Trace storage is bounded to 16 records, 8 MiB each, with 32 frames per event,
128 targets, and the value ceilings above. Secret-like local names and optional
redact_names are redacted before persistence. Files use mode 0600. Restart
finalizes interrupted recordings with a coverage gap; corrupt records refuse
recovery. Completed traces cannot be appended to. Temporary breakpoint cleanup
preserves pre-existing and subsequently replaced user breakpoints.
Source handles retain their normal lifetime; trace source fingerprints remain
available after handles expire. Every trace describes only one sampled execution.

## Static/observed overlay (X26)

path_explain mode=combined retains the static snapshot and candidate paths,
adding a detached overlay keyed by graph, revision, trace ID and completed digest.
Exact launch-source mappings plus unique caller/callee call-site matches mark
static call edges observed. Duplicate call-site candidates remain ambiguous;
a mapped stack relation missing from the static model is retained as a runtime-only
adapter gap. No historical graph is rewritten.

Verified declaration targets can qualify a call edge as not_observed; other
unsampled edges remain uninstrumented. Both classifications concern only the
retained samples. Dropped events and binary/source uncertainty remain visible.
Unmapped stack frames are disclosed rather than guessed. Overlay work shares
the acquisition deadline and has event, frame, edge and encoded byte ceilings.

Sampled locations supply alternative frontier candidates. Thread transitions
describe sample order, not synchronization. Branch-edge outcomes remain unknown:
a stack in one branch does not establish the whole branch history. The live gate
executes each branch separately, retains both static alternatives, records only
the executed branch functions, preserves a dynamic callback as a runtime-only
relation, and refuses an overlay after source changes. Combined status remains
unknown rather than claiming that an entire candidate path was observed.

## Branch and absence explanations (X27)

Combined path responses add an explanation containing the target's observed or
not_observed status within retained samples, candidate frontiers, captured
condition operands and static/observed boundary candidates. Cause status stays
unknown. An early return or exception near an absent target is not automatically
its cause. Several visits and goroutines can support competing frontiers.

Only a stopped top frame at the exact condition line with matching source
fingerprint supplies operands. Go integer/bool comparisons, boolean combinations,
parentheses and unary not/negation can be evaluated without running debuggee code.
Calls, indexing, field access, arithmetic, missing/duplicate names, unsupported
types and redacted/truncated values leave evaluation unknown. Even a successful
evaluation is inferred from a snapshot and identifies candidate branch edges,
not a proven subsequent branch history. true/false identifiers require captured
bindings because Go permits shadowing.

Explanations retain at most 128 condition samples and 1 MiB of condition details,
with a separately capped 128 boundary candidates. All ceilings disclose coverage
limits. Source ambiguity, uninstrumented callbacks, interrupted capture and
schedule uncertainty prevent general negative or causal claims.

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
Later stages must add assertions about the actual graph and trace behavior.
