# Huyang S09 semantic handles

S09 adds service-local, epoch-bound semantic handles without extending the stage into
Git provenance or transaction intent.

## Delivered scope

- Opaque range, match, symbol and frozen-result-set identifiers are created by the
  workspace service. Their records remain inspectable and include the human-readable
  locator, workspace ID, workspace epoch, source revision, creation/expiry times and
  display label.
- Handle lifetime defaults to 30 minutes and is configurable for deterministic tests.
  Expired handles and handles from an earlier workspace epoch are rejected.
- Resolution is explicit: an operation receives an exact, relocated or conflicted
  result. A relocation carries both the original and current locator; a conflict carries
  a stable conflict code and bounded candidate locators.
- Exact range resolution requires the recorded revision and content. Relocation requires
  a unique content-and-anchor match. Symbol relocation uses parser-provided section
  identity, normalized node/signature hashes and stable file identity checks.
- Rename, signature change, ambiguous duplicate/overload, cross-file move, formatting
  change and delete/recreate are classified without silently selecting a different
  declaration. Formatting and unique move are explicit relocations; unsafe cases
  conflict.
- Existing human locators remain accepted. Opaque range or symbol handles can be resolved
  and used directly by one-operation replacement preparation/application.

## Frozen search result sets

Every initial workspace search now creates an opaque result-set handle whose private
record retains the complete, unpaged match set. Its public metadata exposes:

- completeness, source workspace revision, file manifest and per-document revisions;
- total match/file counts, overlap state and all-match replacement eligibility;
- creation/expiry time, workspace epoch, query/mode and inherited constraints;
- optional parent lineage plus retained and eliminated match counts.

Presentation limits affect returned hits only; they do not truncate a complete frozen set.
All-match resolution is allowed only for a current, complete, non-overlapping set whose
workspace epoch, file manifest and document revisions still match. Capped searches remain
inspectable but are ineligible.

Refinement is monotonic. A child may add path and matched-text literal/regex constraints,
inherits its parent's coverage, and records the parent ID and retained/eliminated counts.
Current workspace sets and explicitly historical sets are distinct; S09 does not implement
the historical Git provider that belongs to S09H.

## Modern MCP surface

- `workspace_search` accepts either a new query or a frozen result-set handle plus
  refinement constraints.
- Search hits include opaque match handles and responses include result-set metadata.
- `workspace_read` accepts an opaque range/symbol/match handle and returns its explicit
  resolution.
- `workspace_edit_apply` accepts either the existing human file/range locator or an opaque
  range/symbol/match handle. This is the existing one-operation edit surface, not S10
  durable transaction intent.
- `workspace_symbol_find` emits inspectable symbol handles when the configured workspace
  parser implements the existing provider-independent section interface. When it does not,
  the response remains truthfully partial instead of fabricating semantic identity.

## Verification coverage

Workspace and bridge tests cover exact inspection, TTL and epoch invalidation, unique range
relocation, opaque edit use, complete and capped result sets, overlap rejection, source
invalidation, monotonic refinement lineage, historical/current separation, official SDK
round trips, formatting relocation, cross-file movement, signature change, rename,
duplicate ambiguity and delete/recreate.

## Boundaries and residual risks

- Handle storage is intentionally service-local and in-memory. Restart invalidation is
  enforced by workspace epoch; persistence is not part of S09.
- Semantic symbol quality is bounded by the configured parser/provider. The text core can
  always issue content-anchored range/match handles, while semantic symbol handles require
  a section-capable provider.
- S09 does not add Git history/blame, durable transaction intent, journals, deployment or
  live-service changes.
