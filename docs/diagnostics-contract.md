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

For canonical edits, `document_revision` is the exact per-file postimage
`docrev_...` returned in the mutation receipt, including the final formatter
output and the last edit to each file in a batch. The coordinator passes those
revisions to the provider by document path before diagnostic collection. Pending
late pushes and pulls retain that document revision with the transaction and
expected buffer version; collection never substitutes the current workspace or
document revision. A supplied map with no entry for a document leaves its
revision unknown. The scalar provider argument remains available to older
callers and preparation-scoped evidence. Preparation providers are private and
closed on apply or rollback; their pending observations are never transferred
to the canonical provider or collected into the canonical ledger.

## Completeness and late evidence

Immediate and late push evidence follow the same completeness rule: the
publication must cover the expected document version, an empty push must have
150 ms of quiet, and work-done progress started for that edit vetoes completion.
Late pushes and pulls recheck buffer validity, loaded state, path, changedtick,
LSP version and disk currency at collection. Any currently open producer progress
vetoes late completion. Push and pull retain independent lanes per provider and
document through immediate selection, late coalescing, ledger insertion and
coverage. A complete publication covers only its declared stream. Within one
lane, an intermediate empty set cannot resolve a superseding positive observation,
and a later provisional empty cannot discard an undelivered positive.
Ordinary `textDocument/diagnostic` responses have no producer document version.
Both positive and empty full bodies remain provisional: the request's expected
version, a changed opaque result ID, quiet and progress completion do not prove
that the producer analyzed that document version. Request/postimage guards retain
useful positive findings and their pending transaction, but no pull body can
resolve omitted findings or turn older empty coverage into authoritative proof.
A pull-only producer may therefore remain pending even when the document is clean.
Versioned current pushes retain the completeness rule above.

A bounded pull watcher preserves the native refresh handler and fetches again on
producer progress completion, diagnostic refresh or later evidence collection.
Each producer/document has one owned request in flight, with at most 16 requests,
30 seconds of retention and 64 watchers per provider process. These limits only
bound fetching and memory; their expiry never proves absence. Buffer, client
identity/attachment, version, disk and pending transaction changes cancel stale
work, and late observations are revalidated before delivery. Attachment checks accept
Neovim 0.11 boolean entries and Neovim 0.12 language-ID entries; absent or false
entries and replaced client objects are rejected. Native request
cancellation retires the request generation, wakes a waiting caller and schedules
a replacement within the same budget; a stale callback cannot mutate its
replacement. Closing a watcher also wakes its caller. A version-matched push
received during a pull wait wakes the caller; when complete, it is included in
the immediate response;
the independent pull watcher and its in-flight request stay alive. Later edits
continue to collect supported pulls regardless of earlier complete pushes.
Late collections can deliver both lanes together. Repeated payloads do not
reannounce findings, and merged current/new bodies deduplicate by finding ID.

Omission resolves a finding only when its nonempty, known historical
`evidence_kinds` set is wholly covered by that valid complete batch's stream.
Push-only findings can resolve on complete current push omission, including
omitted Sources within the push domain. A pull-only or mixed push/pull finding
cannot resolve on push omission. Rejected withdrawals retain their Active/Stale
membership. Baselines never withdraw.

Finding identity remains shared across streams and includes source, message,
severity, code and occurrence; ranges can move without creating a new ID.
Different source/message findings remain distinct even at the same range with
the same code. Historical kinds are append-only across edits, recurrence and
bounded evidence-reference pruning. They conservatively over-approximate
outstanding origins and may retain a recurring finding indefinitely. Missing,
empty or unknown legacy kinds retain unknown coverage after refresh. Latest
item revision/version fields describe its latest observation, not freshness for
every historical lane. This model makes no claim of sequential withdrawal or
precise per-origin freshness.

Provider dimensions retain each lane and aggregate the weakest confidence.
Document changes invalidate each lane's coverage even when a shared finding ID
is refreshed by another lane. Immediate observation confidence, diagnostics
queries and empty later collections cannot turn independent stale/provisional
origins into authoritative clean coverage.

Unversioned push positives are provisional and unattributed; unversioned emptiness
cannot prove resolution. Rust uses `rust_diagnostics_pending` when semantic proof
is incomplete and delivers late findings via `diagnostic_updates` on later calls.

## Python environments

Pyright and basedpyright retain an explicit `settings.python.pythonPath`. Environment
settings (`venv`, `venvPath`, `pythonPath`) in `pyrightconfig.json` or the
`[tool.pyright]` / `[tool.basedpyright]` table also own interpreter selection.
Their configuration is left to the language server; discovery does not validate that
dependencies are installed. Otherwise discovery reuses the pipeline's filesystem selector:
`.venv/bin/python`, `venv/bin/python`, `env/bin/python`, executable `VIRTUAL_ENV`,
then the lockfile manager (uv before Poetry). A non-executable preferred venv is an
explicit failure, not permission to substitute an unrelated interpreter.

Provider bootstrap configures the interpreter before the first source buffer loads, including
on generation restart. The canonical workspace root supplies environment metadata even when
source bytes live in a preparation sandbox. The pipeline's command detection remains read-only and does not execute
these queries. Language support discovery may issue
`uv --offline python find --no-python-downloads` or
`poetry --no-plugins env info --executable`, with that root as cwd and a two-second
deadline, only when the canonical environment root is trusted by the M5 user policy.
The gate reads `TrustedRoots`, resolves symlinks on both sides and uses the pipeline's
root-or-descendant matching. Missing trust, policy read/parse errors or root resolution
failures deny execution with `environment_manager_untrusted`. A sandbox cannot grant
trust to an untrusted canonical root. Local venv, `VIRTUAL_ENV` and explicit-setting
selection remain filesystem/configuration-only and do not require command trust.

These manager queries read project-controlled configuration and may execute candidate
interpreters internally. They are not metadata-only or safe to run in an untrusted root.
Huyang does not directly launch Python snippets, project scripts, a shell, `uv run`
or `poetry run`, and requests no downloads, dependency sync or installation.
A manager executable resolving inside the project is refused. Manager stderr is not published.

Manager output is accepted only if it names an absolute executable in a `bin` directory
whose parent contains a regular `pyvenv.cfg` file. The marker is checked against the
selected path, without resolving the Python binary: a valid virtualenv interpreter may
symlink to a base interpreter outside the venv. External Poetry venvs are allowed.
System/uv-managed base interpreters without that marker remain unresolved with
`environment_manager_not_project_environment`, so unsynced projects cannot acquire
full import authority from a base interpreter. Other failed/missing managers, multiline
output, relative paths, and absent or non-executable interpreters also remain unresolved.

When no interpreter is known to the attached client, only `reportMissingImports` findings
from Pyright/basedpyright are collapsed into one stable warning per producer/document.
The warning uses the existing finding shape, severity 2, source `huyang`, code
`python_environment_unresolved`, and a stable range/message so ordinary ledger identity
deduplicates it. Its severity keeps the explanation visible in edit deltas. Other findings
and import-related codes are retained. Grouped evidence has `complete: false` and reason
`python_environment_unresolved`; confidence stays provisional even if a producer incorrectly
claims completion. The reason is a public diagnostic reason code and cannot be corroborated
away by a project check. Omitted findings cannot be treated as resolved.

Immediate evidence and late pushes/pulls use the same policy. Late normalization works on a
copy after publication/version/progress checks; it never changes raw publication proof,
guard tokens or transaction attribution. Existing baselines, retention and notification
rules still apply. Interpreter configuration changes require a provider restart; a later
global setting is not proof that an already-running client used it.

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
alone is insufficient after checkout or external writes. Reading the postimage
invalidates current lane coverage but does not invalidate retained authoritative
payload proof for the exact pre-edit revision in that same lane. Baseline lookup
rechecks that immutable payload's own authority and producer identity; current
coverage need not still describe the preimage. Findings without a matching
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
The persisted diagnostic ledger is version 3. Version-1 and version-2 data is
migrated and the new header is saved before use; older binaries reject it.
Unsplittable legacy provider/document coverage stays provisional with
`legacy_stream_coverage_unknown`; bounded retained evidence cannot reconstruct
missing stream proof. Historical resolved records are retained as historical
records, without claiming verification under the new withdrawal rule. Back up
state before upgrade if rollback must preserve diagnostic history. Newer unknown
ledger versions are refused without rewriting them. The tool API namespace is
unchanged.
