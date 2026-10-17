# M9 historical post-fix observation — adopted source contract

Igor directly instructed the execution thread on 2026-10-17T21:59Z:
**"Follow the rec for the decision"**. The root recorded adoption in
`tooling/plans/huyang-friction-roadmap-2026-10.md` revision164 and checkpoint53,
before this implementation. The recommendation is
`/home/igor/.cache/huyang-friction/python-terminal-observation-critical-design-r2.md`.
It replaces only condition3's additional settled-terminal pure-pull requirement
from `m9-pull-acceptance-policy-review.md`. Origin, history, non-resolution and
actual stamped-push safeguards remain binding. The original decision inbox record
may remain technically parked; no self-answer or new Igor approval is needed.

The coverage loss is explicit: the historical outcome does not supply the former
settled-terminal empty observation. It never supplies producer analysis completion,
a latest-empty guarantee, clean resolution, or omission-based withdrawal. A result
`historical_post_fix_observation_pending` always reports
`analysis_completion_proven=false`, `clean_resolution_proven=false` and
`old_terminal_gate_qualified=false`. Pending describes unproven knowledge; actual
open progress tokens and inflight requests are separate observations.

The old cd7da subset remains 0/3 FAIL, with six pure tests PASS. The earlier cc399
matrix remains 8/15 actual passes and overall FAIL. Their source, evidence IDs,
timestamps and results are not rewritten. No production bug was established by
cd7da. A new source review and fresh full fifteen-case matrix are required.

## Actual origin, without a diagnostic schema extension

The test-only raw request wrapper observes dispatch, transport acceptance,
cancellation and callback, including process/client/request identity, exact URI,
local version/changedtick, buffer/disk hashes and current attachment. It scopes
that actual callback identity around the original inner handler. It preserves the
handler's arguments, errors, results and return slots.

`diagnostic_pull.lua` invokes the optional observer only after its existing
generation/current/inflight checks accept the callback. Native or discarded
callbacks cannot borrow this acceptance. A missing raw context stays unattributed.
The original watcher caps, generation transitions, cancellation, deduplication,
wakeups and scheduling remain unchanged.

`diagnostic_observer.lua` keeps weak sidecar mappings. Accepted receive scopes
identify the actual `pull_batch` construction. The real `queue_late` boundary adds
its actual sequence to that sidecar. Late delivery observes the original batch and
the normalized copy together; immediate delivery observes the normalized original.
Neither identity nor queue sequence is inserted into the diagnostic payload.

A test-only wrapper around the real `vim.rpcnotify(..., "huyang/result", ...)`
observes the actual completion's batches. Its weak normalized-table mapping links
that exact transported table to the construction/emission sidecar and actual
provider RPC ID. It follows the production decoder's preference for nonempty
`completion.evidence`, otherwise `completion.result.batches`. Callback snapshots
are deep copies. Observer exceptions leave production delivery intact and cause
qualification to fail through recorded errors or missing causal events.

No observer is installed in ordinary sessions. Absent-observer functions return
without snapshots, logging, fetching or I/O. The optional observer is not authority
or a supported producer proof capability.

## Conservative transport-to-selected Go edge

An observed Lua emission alone is insufficient. The probe records raw-trace byte
boundaries immediately before/after each real MCP call, outside the immutable MCP
reply body. The first report/receipt citing a selected evidence ID must contain its
unique successful actual provider transport entirely inside that call's boundary.

The selected batch must match the entire typed emitted diagnostic payload:
provider/producer, kind, canonical document, DocRev/transaction, expected and
producer version, result ID, selection/completeness, timeout/progress/barrier,
reason/dimension/staging/baseline, findings and culprit candidates. Omitted typed
empty strings, false optional booleans and empty arrays have their serialized zero
meaning. Unknown payload fields are rejected.

Only three Go-owned additions are excluded from transport equality, at immutable
parent `89cbead1f2f5e990b140e65e8aea7f4f2b860edf`:

| Field | Actual ingestion source |
| --- | --- |
| `pre_edit_document_revision` | `providerpool.bindDiagnosticBatchDocument`, `internal/providerpool/evidence.go:125–126`, called by `recordDiagnostics` after receipt binding. |
| `state_seq` | `Workspace.RecordDiagnosticEvidenceBatches`, `internal/workspace/evidence.go:1410–1421`, fills zero from workspace identity on its copied input. |
| `observed_at` | `diagnosticStore.recordBatches`, `internal/workspace/evidence.go:519–520`, assigns the recording time only when absent. |

These fields never supply clean/version/completion proof. The original selected
evidence ID and full original observed_at remain in the result unchanged.
The ordering check that recording follows actual transport is an additional
consistency rejection, never an origin guess.

`providerpool.diagnosticPayload` (`evidence.go:342–370`) reads those actual typed
batches; `CollectLateEvidence` (`318–338`) coalesces within streams, canonicalizes
paths and records them. Immediate `recordDiagnostics` selects the same payload and
binds the receipt transaction/document revision (`99–126`). The test retains those
causal call boundaries and requires one unique origin across the full observed
construction/queue/emission/transport history. Distinct origins or repeated
identical emissions/transports are ambiguous and fail, even across separate calls.
Result ID/items equality or a nearby timestamp cannot replace the missing edge.

The captured case00 example remains an original association: callback354,
request74, result14 corresponds to
`ev_8f129ef7260b283b87105e3a285f123e`, observed_at
`2026-10-17T21:08:42.911601869Z`. Callback375 at
`21:08:42.913195Z` is a later identical observation. The new result must preserve
such duplicates separately, never attach the old batch to375. The old artifacts
lack this new seam and do not gain qualification retrospectively.

## Complete bounded history and later positives

Before fixing, the probe writes `historical-observation-plan.json`: five seconds
of monotonic sampling allocation from fix dispatch, then a final full diagnostics
call and recovery of all referenced evidence. The original 120-second case ceiling
still fails an interrupted procedure. Inotify/event bookkeeping drives collection.
The allocation is a measurement procedure, never a settle timer, quiet proof,
retry threshold or deadline-to-success rule. It does not stop at the first empty.

The final read defines a complete newline-terminated byte cut. Every process has
its own contiguous sequence from observer installation; processes are not merged
by trace_seq. The canonical attachment is selected explicitly, and all other raw
process records remain in the cut. Unknown events, gaps, unfinished collections,
telemetry errors or ambiguous identities reject qualification. The cut includes
work triggered by final diagnostics/evidence recovery.

All later relevant callbacks, progress, cancellations and pushes are replayed.
Later progress records every superseding sequence and invalidates terminal-current
claims while leaving the historical fact intact. Actual tokens/inflight maps can
be empty; no invented activity is reported. The latest raw payload remains visible.

Every successful later full positive callback must have a genuine accepted origin,
construction, enqueue/emission, transport, selected evidence and actual current/stale
finding bodies carrying that evidence ID. Raw presence, an emission alone, or an
eventual empty cannot hide an undelivered positive. Such a gap fails the result.
A delivered later positive gets the conspicuous final classification
`historical_empty_later_positive_delivered`, even if more progress follows.
Unattributed native/cancelled later positives conservatively remain unqualified.

Every intermediate/final report, edit delta and diagnostic_updates surface is
checked for provisional confidence, retained original current/stale IDs, no
resolved notice/body/ID and no truncation. Later correction cannot excuse earlier
false authority or disappearance. An actual stamped push selects the existing
authoritative lane with complete/current/quiet/progress and legitimate-resolution
checks. Failed available proof, unversioned/mixed pushes and ambiguity cannot fall
back to historical acceptance.

## Artifacts and remaining validation

The source writes exactly these additional case artifacts:

- `historical-observation-plan.json`: declaration before fix dispatch.
- `historical-trace-cut.jsonl`: exact final trace prefix, byte count and hash in
  coverage; the complete raw lsp.jsonl remains independently retained and hashed.
- `historical-observation.json`: separate historical/attribution/non-resolution
  claims, original evidence and sidecars, duplicate/superseding sequences, latest
  payload, actual tokens/inflight, final ledger and every report artifact hash.

Cleanup verifies the final raw trace still contains the declared cut. The collector
allowlist adds only these exact three basenames; its fullmatch, file safety,
inventory, recursion and size/file-count caps are unchanged. Root must refresh
source/collector entry pins, then verify the complete collected inventory, exact
cut prefix/hash, every report hash, plan presence and all required case artifacts.
Missing/truncated/interrupted collection remains unqualified. Collector
`complete_collection=true` alone does not establish these source acceptance gates.

No test, build, syntax/import check, linter, formatter, client, provisioning,
Chaldea job, SSH, install, push or review ran in this implementation child.
All source edits use Huyang with format=false. Root owns fresh other-provider main
review of the exact final clean commit and Git-independent agent-a checks:

- Existing six Go pure regressions plus `TestM9Historical.*`.
- `nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_edit.lua`,
  including dormant/error/snapshot/cancellation/dedup/real enqueue-emission checks.
- Collector exact-name acceptance and prefixed/suffixed/near-miss rejection, with
  existing inventory/hash/safety checks. No generic allowlist widening.
- Appropriate production Lua/Go regression checks and fresh full fifteen-case
  Python matrix from sealed updated source. Strict-nine and other milestones are
  separate gates.

Every Go invocation in a Chaldea job must set HUYANG_FRICTION=0. Use the established
scratch HOME/namespace, immutable tool pins, artifact limits and full wrapper.
The source is unvalidated until those checks and review complete.
