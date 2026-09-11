# S20b — Structural consolidation after the hardening rounds

Status: waves 0, 1 and 2 merged on `feature/huyang`; stage complete
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

## Wave 1 (merged)

Provider and kernel, merged at `9adebee` (`b289aac`, `6074f88`): cooperative
cancellation, kernel protocol 2 with `huyang/result` and native MessagePack payloads,
structured provider errors, `Result` extended with touched documents, evidence and health,
API-level Neovim compatibility (`minAPILevel` 13, Neovim 0.11), fault hooks behind
`HUYANG_TEST_FAULTS`, client-keyed Lua request state, removal of dispatch entries with no
Go caller, `HUYANG_*` names for the surviving `AGENT99_*` variables. The `AGENT99_*`
names are still read as fallbacks when the `HUYANG_*` name is empty; the README lists
every one.

Bridge, merged at `40af416` (`b1a70f5`, `d8e52b2`, `39967ce`, `0d51310`, `21bb8f2`, with
`eeeb885` and `4c7f6cf` merging the trunk into the working branch along the way):
cross-workspace stall removed, stagers keyed by workspace, scheduler class per descriptor,
envelope finaliser bounding `next` to two entries and filling the required envelope keys,
`AcceptProvisional` wired to an explicit `accept_provisional` argument, `ErrorCode`
classification, bounded and payload-stripped receipts in `receipts/<workspace>.json`
outside `registry.json` (which is now version 2 and holds workspace definitions only),
pprof on a loopback listener behind the HTTP bearer token, compaction of `diagnostics`,
`search`, `workspace_open` and `language_server_status`, the per-client
`diagnostic_updates` delta, the typed provider result and error codes, and the `read` by
`symbol_locator` failure. `fe143fe` widened one timing margin in the advertised-timeout
test after the merge.

The stage record itself was committed at `9f18be1` between the two merges.

### Follow-ups deferred by the bridge wave

- The diagnostic ledger marks findings whose document changed as `stale` and counts them
  (`StaleCount` in the report), but the compact `diagnostics` result does not surface that
  count; it is visible only with `full=true`.
- `diagnostic_updates` is a per-client delta keyed by the MCP session ID. The Streamable
  HTTP transport is stateless, so every HTTP request is a fresh session and receives the
  pending notices again; a client identity header would be needed to make the delta hold
  over HTTP.
- Output-schema validation of every envelope runs only under test: `envelopeAudit` is a
  package variable the test suite installs, and `finalizeEnvelope` enforces the required
  keys and the `next` bound in production without validating against the schema.

## Wave 2 (merged)

Docs sweep, `fb45e67` and `35dd80b`: README, tool contract amendment and fixture at 19 tools,
handoff checkpoint, rollout addendum with the checkout-based redeploy procedure, removal of
the Agent99 UI screenshots.

nvim-dap de-vendoring, `89cbfe7` and `42cc21a`: the GPL-3.0 nvim-dap copy left the tree;
the kernel discovers it from `HUYANG_NVIM_DAP_PATH`, the provider's own init, the lazy.nvim
path or a site pack, reports `dap_runtime` in the handshake, and the debugger tools return
`dap_runtime_unavailable` with the searched locations when it is absent.

Workspace refactor, `5defe8e..7d51038`: every function in `internal/workspace` under 80
lines, one temp-write path, unified preimage comparators, a package doc comment stating the
three invariants callers must not break.

Embedded provider, `5a61a1c`: `Call` and `bootstrap` split into phases.

Gates, merged at `846a586`: `make budget` (cold and warm RSS, state directory, code size,
descriptor bytes, compared against `docs/plans/budget/baseline.json`), `make lint` (gofmt,
vet, functions over 80 lines) and `make check`.

Bridge split, merged at `fc6faa8`: `internal/bridge` replaced by `internal/service`
(listeners, proxy, dispatcher, registry, receipts, scheduler), `internal/mcpapi` (catalog,
schemas, envelope, compaction), `internal/handlers` (one file per tool family) and
`internal/providerpool` (providers, stagers, evidence). Dependency direction is service to
handlers, mcpapi and providerpool; handlers to mcpapi and providerpool; providerpool to
provider and workspace. Hardening tests renamed after their invariants and moved beside the
code they guard; no function over 80 lines; `stale_count` surfaced in `diagnostics`;
`X-Huyang-Client` header gives HTTP clients a delta cursor; symbol name paths accept
`Type.Method` and `(*Type).Method` and resolve through the provider.

Also in this wave: `.huyang.toml` declares the repository's own gofmt gate, `go vet` check
and `go test` suite, so `verify_run` and prepared plans verify this repository.

Final budget at the stage end: 0 functions over 80 lines (39 at the start of wave 2),
largest Go file 1,373 lines (3,008), cold service RSS 13 MB, state directory under 100 KB
for the fixture scenario.

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
