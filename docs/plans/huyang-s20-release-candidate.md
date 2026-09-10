# Huyang S20 release candidate and deployment-readiness report

Captured: 2026-09-10  
Starting commit: `26864fe3a890d423d94701fddb3cfde27deffc75`  
Candidate status: implementation-complete and verified; not deployed

## Scope completed

S20 routes the remaining legacy mutation families—`rename_symbol`,
`apply_code_action`, `replace_pattern`, and `move_symbols`—through the same
isolated snapshot and durable single-operation commit journal used by the first S19
families. The provider still owns guarded semantic resolution, LSP workspace edits,
Tree-sitter classification, import organization, diagnostics, exact legacy rendering,
and the client undo ledger. Huyang captures every explicitly named file plus every
modified loaded buffer under the canonical root, discovers the exact sandbox delta,
and commits that delta only after canonical drift validation.

The global `HUYANG_LEGACY_WRAPPERS=direct` escape and per-tool
`HUYANG_LEGACY_<TOOL>_PATH=direct` escapes remain. Read-only `dry_run=true`
and asynchronous `wait=false` calls remain direct. File-system effects emitted
inside an LSP workspace edit remain bounded by the provider's existing behavior and
the commit journal's discovered write set; they are a residual compatibility risk,
not an authorization for arbitrary command execution.

`verify_run` now reuses an in-memory result only when workspace ID, exact canonical
or prepared revision, ordered stage selection, and test scope all match. A new
idempotency key can therefore reuse a revision-keyed check/test result; transport
replay remains the stronger per-request guarantee. A changed revision or different
stage/scope cannot hit the cache. Test history remains durably revision-keyed.

## Compatibility window

The legacy agent99 tool names and text rendering are supported from this release
candidate through the first stable Huyang release and for at least 90 days after a
modern profile becomes the documented default. Removal requires both:

1. a tagged release that emits a deprecation notice and names the modern replacement;
2. at least one later tagged release and 30 calendar days after that notice.

The direct escape flags remain throughout the compatibility window. Legacy behavior
is compatibility-scoped: guarded targeting, response shape, dry-run semantics,
per-client undo, and explicit degraded coverage are preserved. New modern features
are not required to be backported into legacy schemas.

The frozen modern public surface is `huyang.workspace/v1alpha1`: 17 tools in
`full`, 8 in `orient`, 13 in `edit`, and 12 in `debug`. A compatible
v1alpha1 change may add optional result fields or new stable error codes, but may not
rename a tool, change an existing field's meaning, weaken a mutation precondition,
or turn a non-executing call into an executing call.

## Version promises

### Public protocol

MCP revision negotiation and `huyang.workspace/v1alpha1` are independent.
Wire-transport upgrades do not silently change workspace semantics. Incompatible
model-facing schema changes require a new Huyang API version and a separately named
catalog or endpoint. Structured content is authoritative; deterministic text fallback
remains available for the v1alpha1 compatibility window.

### Provider kernel

The Go/provider boundary and Lua RPC kernel use explicit capability negotiation and
provider epochs. Additive capabilities may appear within a minor Huyang release.
Removing or changing an operation requires a kernel protocol version increment.
A service refuses an incompatible kernel instead of guessing, and a provider restart
invalidates epoch-bound handles and evidence.

### Durable workspace, plan, evidence, and transaction formats

Every persisted record retains its explicit format version. Readers accept all
formats written by the same major Huyang release. A future writer must either migrate
atomically while retaining a recoverable preimage or leave the old record untouched
with an actionable incompatibility error. Commit-journal version 1 is never abandoned
while a version-1 journal may still require recovery. Unknown journal versions are
quarantined from writes; they are not deleted or treated as completed.

## Migration guide

1. Launch a fixed modern profile for new sessions: `full` for portability,
   `orient`, `edit`, or `debug` when the host already knows the task.
2. Replace inferred roots with one `workspace_open` call and retain its workspace ID
   and revision. Reuse those values across adapter reconnects.
3. Replace tree/map/skim/read aliases with `workspace_inspect` and `read`;
   replace grep with literal-by-default `search`; replace navigation aliases with
   `navigate(relation=...)`.
4. Use `edit_apply` for exactly one guarded operation. Use
   `change_plan(action="prepare")` with inline operations for a known multi-file
   change, then apply the exact plan and prepared revisions.
5. List fixes with read-only `code_actions`; apply a selected revision-bound action
   explicitly. Use `replace_matches` only for one homogeneous frozen result set.
6. Replace `run_tests` and `check_project` with `verify_run`. Preserve
   `affected_tests_passed` versus `full_tests_passed`; inspect evidence IDs for
   detailed output.
7. During migration, compare legacy output and bytes with the modern result. Set a
   per-tool direct escape only as a bounded rollback and report the discrepancy before
   relying on the direct path.

There is no Git mutation in `change_plan(action="apply")`; it changes workspace
files through the recovery journal and creates no commit.

## Recovery guide

Normal recovery is automatic when a workspace is reopened. Huyang loads durable plan
state, scans commit journals, validates exact canonical objects, and either finishes
the known postimage or restores the known preimage under its cooperative guarantee.
It never overwrites bytes that do not match a journaled preimage or postimage.

For an interrupted operation:

1. Stop issuing writes to that workspace and preserve the Huyang state directory and
   canonical tree exactly.
2. Record the workspace ID, root, journal path, plan/transaction ID, current outcome,
   and the first path reported as conflicting.
3. Reopen the same workspace with the same state directory. Inspect the recovery
   outcome and evidence before retrying any mutation.
4. If the result is `RECOVERY_REQUIRED`, compare the conflicting path with the
   journaled preimage/postimage. Do not delete or edit the journal, sandbox, or
   canonical file to force progress.
5. Restore external ownership or disk capacity when that is the reported cause, then
   reopen. If a third-party write is present, keep it and escalate; Huyang deliberately
   refuses to choose which content wins.
6. A committed legacy undo is a compensating journaled transaction. A stale undo
   refuses unless the legacy `skip`/overwrite contract explicitly permits the
   requested behavior.

Back up both the canonical workspace and state directory before any manual repair.
There is no release-candidate authorization for a live service restart, state edit,
installation, or deployment.

## Paired evaluation

The six frozen S00 tasks were re-evaluated against the committed modern and legacy
contracts: orientation, production-only caller search, one guarded symbol edit,
two-file prepare without apply, staged-error/code-action recovery, and stale-conflict
recovery. Behavioral evidence comes from the official-client suites, legacy wrapper
snapshots, real-language diagnostic barriers, journal failpoints, and the complete
smoke matrix. No paired task required undocumented state, inferred complete coverage,
or a weaker mutation guard.

Measured with `cl100k_base` over SDK tool objects:

| Catalog | Tools | Bytes | Tokens |
| --- | ---: | ---: | ---: |
| modern `orient` | 8 | 30,746 | 6,774 |
| modern `edit` | 13 | 61,525 | 13,495 |
| modern `debug` | 12 | 46,879 | 10,460 |
| modern `full` | 17 | 77,658 | 17,181 |
| legacy default | 35 | 42,784 | 9,014 |
| legacy full ordinary | 38 | 44,272 | 9,324 |

The modern portable `full` catalog is intentionally more expensive in raw schema
tokens than the ordinary legacy catalog because it includes debugging and closed
action/target unions. Fixed task profiles are therefore part of the acceptance
measurement, not optional hand-waving. Across the four orientation-shaped and two
edit-shaped frozen tasks, selected-profile schema cost is effectively equal before
results (54,086 versus 54,084 legacy-default tokens); modern workflows then use fewer
catalog choices and fewer lifecycle/recovery calls. Debug evaluation separately
proved four modern descriptors (7,505 bytes) smaller than 13 legacy debug descriptors
(8,383 bytes), with equivalent real-Delve stop/source/variable fidelity. No quality
regression was observed in the black-box and smoke suites.

This is an honest release-candidate result, not a claim that `full` is cheaper for
every host. Hosts able to select fixed catalogs should use the task profile. Hosts
that require one portable catalog pay the disclosed schema premium in exchange for
the complete 17-tool surface.

## Deployment readiness

The branch is a release candidate for explicit review, not approved for rollout.
The code has direct-mode MCP, shared service/scheduler, revision-bound workspaces and
handles, isolated preparation, journal recovery, evidence, variants, wrapper
compatibility, and the compact debugger facade. The final verification commands and
their exact results are recorded in `docs/plans/huyang-handoff.md`.

Residual risks to review before rollout:

- the portable `full` schema premium described above;
- provider-specific rename/code-action workspace edits and degraded LSP coverage;
- trust/configuration mistakes in project command policy despite sandbox and write-set
  enforcement;
- platform-dependent sandbox fallback and resource costs on large repositories;
- compatibility escapes bypassing the journal when explicitly selected;
- the cooperative multi-file journal guarantee is not filesystem-wide atomicity.

No Huyang installation, deployment, live MCP restart, live configuration/state
mutation, pinned-plugin update, push, or pull request occurred. Rollout requires a
separate explicit approval and should begin with a non-live disposable state directory,
then a bounded canary using the documented direct escape.
