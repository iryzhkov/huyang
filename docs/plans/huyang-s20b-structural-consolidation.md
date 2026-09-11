# S20b — Structural consolidation after the hardening rounds

Status: in progress on `feature/huyang`; this record is updated as each wave merges
Prepared: 2026-09-11
Predecessor: S20 release candidate at `4f87241`, followed by 26 probe-driven hardening commits ending at `f0b537d`
Audit baseline: `f0b537d`, `go build`, `go vet`, `go test ./...` green

## Why this stage exists

Several agent-led hardening sessions probed the S20 release candidate, patched what broke,
and repeated with varied environments. That found real defects and left regression tests,
but the patches accumulated as symptoms: string-prefix parsing, substring error
classification, reconnect logic in the proxy, persistence added for every "lost state" probe
with no bound, and probe conveniences left in production paths. A design-anchored audit at
`f0b537d` found the structural causes beneath them. This stage replaces the patches with
structural fixes, removes surface that no binary reaches, and records the decisions the
future plans must build on.

The audit report itself is summarised in "Findings" below; the fixes are grouped by wave so
the merge order and file ownership stay traceable in `git log`.

## Findings that drove the stage

Must-fix correctness:

- A commit precondition failure after the journal was written, but before any canonical
  write, marked the journal `recovery_required`; the next open then refused the root.
- A failed commit left the plan lease held and the sandbox stager open.
- `DiscardPlan` accepted any state and `transitionPlan` had no legality table.
- PROVISIONAL plans committed with no explicit override.
- `providerMu` was held while waiting on stager locks that prepare and verify hold for
  minutes, so one prepare stalled every workspace.
- Scheduler classes were assigned by tool name and most provider-touching tools were
  classed `pure_read`; `CheckProviderAccess` was a stub.
- One cancelled request restarted the provider generation and invalidated every client's
  handles.
- The service reached 8 GB peak RSS. The state directory explained it: a 524 MB
  `registry.json` holding 2,199 idempotency receipts with full result payloads, and per-
  workspace plan files up to 201 MB, each rewritten whole with fsync on every mutation.

Design drift:

- About 5,300 lines of Go and 1,900 lines of Lua formed a legacy Agent99 tool layer that no
  shipped binary could reach, including Lua paths that ran `sh -c` from environment
  variables outside the Go trust policy.
- Error codes were carried in error strings and recovered by substring.
- Revision tokens hashed inode and mtime, so a `touch` invalidated every handle.
- The native text path shelled out to `git ls-files` with the ambient environment.
- Symlinks inside the root were recreated verbatim in sandboxes and path confinement was
  lexical.
- The tool roster grew to 19 while every document said 17.

## Decisions

1. **Delete, do not quarantine, the legacy layer.** The S20 compatibility window is void;
   README already stated the Agent99 executable was not shipped. The embedded provider is
   the only backend. Research spikes leave the tree once their decision record is committed.
2. **Typed error codes.** `internal/workspace` exposes `CodedError` with `ErrorCode(err)`;
   the bridge classifies with `errors.As`, never with string matching. Error text keeps the
   `code: detail` shape for humans.
3. **The plan state machine is a table.** `canTransition(from, to)` is enforced by every
   transition, including discard. `CONFLICTED` and `EXPIRED` exist. A precondition failure
   before the first canonical write is `CONFLICTED`, and its journal is `rolled_back`;
   `RECOVERY_REQUIRED` is reserved for a failure after a write.
4. **Only READY commits by default.** A PROVISIONAL plan commits only with an explicit
   `AcceptProvisional(dimension)` option, and the accepted gaps are recorded on the plan.
5. **Every store has a bound.** Revisions per document, handles, result sets, diagnostic
   items and notices, plan events, terminal plans, commit journals, idempotency receipts.
   Each bound is a named constant with a comment. A hardening probe that adds persistence
   must add its bound in the same commit.
6. **Content is the revision.** Document revisions derive from content hash plus kind, mode
   and symlink target. Disk metadata is a change detector only. Identical bytes after a
   `touch`, atomic save or checkout keep the revision and every handle.
7. **Per-record plan storage.** Plan records live at `plans/<workspace>/<plan>.json`
   (version 2). Terminal plans are pruned by count and age and compacted after a short
   window; a compacted plan keeps identity, state, revisions and conflict but refuses edits.
8. **Scheduler class is declared beside the schema.** Each `modernTool` descriptor names its
   class; a test refuses a tool without one. Classes follow behaviour, not tool names.
9. **Cooperative cancellation first.** Go asks the Lua kernel to cancel the request and
   waits a bounded grace period before killing the generation.
10. **Compact by default.** Results carry deltas and IDs; bodies are behind `evidence_get`
    or an explicit `full`/`include_ranges` argument. `diagnostic_updates` is a per-client
    delta and is omitted when empty.
11. **v1alpha1 is amended by this stage and re-frozen after it.** The additions are optional
    inputs and compacted outputs. The contract document and fixtures are updated to match
    the 19-tool roster; the freeze in the semantic-evidence plan applies from the end of
    this stage.

## Wave 0 (merged)

Legacy removal, `197eb7c..367a2f4`: 57 files, 9,681 lines removed. Deleted the legacy
MCP catalog and tools, sticky-root routing, process client identity, legacy wrappers, the
socket provider, both research spikes, `plugin/huyang.lua`, `testrun.lua`, the Lua command
runners and disk writes reachable only from legacy tools, and the environment variables
whose only readers went with them. Helpers still used by the modern path moved to
`internal/bridge/args.go` and `render.go`.

Workspace transactions, merged at `f26c582` and `88d3657`: coded errors, the transition
table with `CONFLICTED` and `EXPIRED`, commit refusal before the first write, lease and
stager release on every exit, `AcceptProvisional`, commit journal retention (7 days, 64
journals), plan event caps, `RENAME_NOREPLACE` for creates, full mode preservation, per-path
recheck before each write, `knownPaths` kept in sync by commit, per-plan record files with
terminal-plan pruning (200 plans, 30 days, compaction after 1 hour).

Workspace stores, merged at `96c52e1`: content-derived revisions, narrowed `snapshot()`
lock, bounded handle and result-set stores, diagnostic ledger retention with `stale` status
for findings whose document changed, `DiagnosticNoticesSince(cursor)`, sanitized
`git ls-files`, symlink escape refusal in sandboxes and component-walked confinement,
parser stage kept `unavailable` under corroboration, explicit `FormattingClaims`, canonical
stage ordering, `atomicWriteFile`.

## Wave 1 (pending merge)

Bridge: cross-workspace stall removed, stagers keyed by workspace, scheduler class per
descriptor, envelope finaliser with universal output-schema validation, `AcceptProvisional`
wired to an explicit `accept_provisional` argument, `ErrorCode` classification, bounded and
payload-stripped receipts outside `registry.json`, pprof on the loopback listener,
compaction of `diagnostics`, `search`, `workspace_open`, `language_server_status`, the
per-client `diagnostic_updates` delta, and the `read` by `symbol_locator` failure.

Provider and kernel: cooperative cancellation, kernel protocol 2 with `huyang/result` and
native MessagePack payloads, structured provider errors, `Result` extended with touched
documents, evidence and health, API-level Neovim compatibility, fault hooks behind
`HUYANG_TEST_FAULTS`, client-keyed Lua request state, removal of dispatch entries with no
Go caller, `HUYANG_*` names for the surviving `AGENT99_*` variables.

## Wave 2 (planned)

- Docs sweep: README, tool contract and fixtures at 19 tools, environment and flag table,
  Neovim compatibility statement, handoff checkpoint, rollout record, this file.
- Hardening residue: rename `hardening_r*_test.go` and `probe_regression_test.go` after
  the invariants they protect and move them beside the guarded code; confirm each patch is
  superseded or still needed.
- Split `internal/bridge` along the seams the audit named: service and scheduler, MCP
  catalog and envelope, handlers, provider pool. The provider pool is the object the stall
  fix wants anyway. This split is a prerequisite for a v1alpha2 catalog to coexist.
- Add a `make budget` gate that records LOC delta, state-directory size and RSS after a
  cold start, so growth is caught by a number between hardening rounds.

## Consequences for the future plans

The semantic-evidence and execution-graph plans in `~/Work/plans` assumed the S20 shape.
Adjustments:

- Invariants (S26) build on `VerificationGap`, `PlanConflictReason` and
  `AcceptProvisional`; required `violated` or `unknown` is a gap that blocks READY, and
  advisory failures are the dimensions a caller may accept explicitly.
- Invariant proofs must survive plan compaction. Store proofs in the evidence store keyed
  by prepared revision, not inside `PlanPreparation`, which compaction strips.
- `AnalysisSnapshot` and `ExecutionGraph` caches are keyed by content-derived revisions,
  which makes them stable across metadata-only changes. Both need bounds on entry count and
  bytes from their first commit.
- Prepared-revision routing (S24) uses stagers keyed by workspace and plan; a prepared
  handle expiry follows the same epoch and plan-revision rules.
- New tools declare a scheduler class in their descriptor. Graph expansion batches use
  cooperative cancellation and the extended `Result` contract.
- New results are compact by default with bodies behind evidence IDs; the token budgets in
  S21 start from the post-compaction baseline.
- The `make live` gate in S21 should include the budget gate above.
- The v1alpha1 freeze in S21 applies to the amended contract from the end of this stage.
