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
vetoes late completion. Observations are coalesced by producer/document before
ledger insertion; an intermediate empty set cannot resolve a superseding positive
observation.
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
replacement. Closing a watcher also wakes its caller. Proven push mode survives
edits while each edit discards the old publication. A current authoritative push
received during a pull wait is included in the immediate response.
Push and pull observations retain separate coalescing slots, so an unstamped
empty push cannot discard an undelivered positive pull. Each late collection sends
at most one observation per producer/document: an undelivered pull goes first,
then the retained push on a later collection. Only a sent observation is marked
delivered; a deferred current push stays queued even when complete. This preserves
provisional positive facts through Go's
per-collection coalescing and delivers current push proof last, without replaying
older provisional slots afterward. Repeated pull payloads do not reannounce findings.

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
