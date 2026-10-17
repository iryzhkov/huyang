# Diagnostic evidence contract

This is what Huyang guarantees about the diagnostic evidence behind edit verdicts,
`diagnostic_updates` and the `diagnostics` tool: when a language server's publication counts
as complete, how pre-edit baselines and late findings enter the per-workspace ledger, how
proof is deduplicated and retained, and how the ledger is persisted. It was written for the
M1 diagnostics work and lived in the experimental execution contract until that contract
was removed with `execution_graph` and `path_explain`; the guarantees themselves are
unchanged. [The agent guide](agent-guide.md) summarises what they mean for a caller.

The reply style changes how this evidence is presented, never what it is. A plain reply
(the default since amendment 8 of the [v1alpha1 contract](plans/huyang-tools-v1alpha1.md))
prints every new finding of `diagnostic_delta` and every `diagnostic_updates` entry, merged
by finding ID, on whichever tool carries them, with any incomplete or unavailable verdict
and its reasons, truncation of the notices, and late attribution as recorded. The
`diagnostics` tool's report, including `current` bodies recovered after acknowledgement, is
printed in full as JSON data in a plain reply; with `--reply-style json` the envelope is
sent as before. Nothing is removed from the ledger, the receipts or the evidence store to
render a reply.

## Completeness and late evidence

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

## Baselines

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

## Deduplication and evidence retention

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

## The diagnostics tool and the persisted ledger

The compact `diagnostics` result is a notice delta. `since` acknowledges notices
workspace-wide; after acknowledgement or reconnect, `full: true` recovers current
finding bodies under `current`. This recovery does not reannounce them as new.
The persisted diagnostic ledger is version 2. Version-1 data is migrated and the
new header is saved before use; version-1 binaries reject it. Back up state before
upgrade if rollback must preserve diagnostic history. Newer unknown ledger
versions are refused without rewriting them. The tool API namespace is unchanged.
