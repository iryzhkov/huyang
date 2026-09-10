# Huyang implementation handoff

## Authority and invariant

- Repository: `https://github.com/iryzhkov/agent99`
- Standalone checkout: `/home/igor/Work/huyang`
- Branch: `feature/huyang`
- Reviewed base: `1c5302efe9ca51e1701e73df72d903f225fefe04`
- Base ancestry check passed with
  `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`.
- No install, deployment, installed-plugin update, live MCP restart, live
  configuration/state mutation, push, or pull request is authorized.

## Current checkpoint

- Completed stage: S09 — Semantic handles.
- Starting commit: `590beff7bd14e4bdd7285ed2569e96e2c12aba5d`.
- Reconciliation before implementation fetched origin and confirmed a clean
  `feature/huyang` branch, the reviewed base in branch history, S08 committed at the
  starting commit, and S09 as exactly the first incomplete checklist stage.
- S09 exit gates are satisfied: inspectable epoch-bound range/symbol handles, explicit
  exact/relocated/conflicted resolution, safe adversarial relocation, complete frozen
  result-set coverage, monotonic refinement lineage, and opaque-handle support on the
  existing one-operation edit path.
- Only S09 is marked complete in the committed checklist.
- Exact next stage: S09H — Read-only Git source provenance.

## Predecessor and stage artifacts

These tracked artifacts were read completely before implementation:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/huyang-s01-package-boundary.md`
- `docs/plans/huyang-s02-provider-seam.md`
- `docs/plans/huyang-s03-embedded-provider-spike.md`
- `docs/plans/huyang-s04-embedded-provider.md`
- `docs/plans/huyang-s05-workspaces-revisions.md`
- `docs/plans/huyang-s06-text-core-documents.md`
- `docs/plans/huyang-s07-mcp-sdk-direct.md`
- `docs/plans/huyang-s08-service-scheduler.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

S09 adds:

- `docs/plans/huyang-s09-semantic-handles.md`

## S09 changes

- Added service-local opaque range, match, symbol, and result-set handles with
  inspectable locator records, configurable TTL and workspace-epoch invalidation.
- Added explicit exact, relocated, and conflicted resolution. Range relocation requires
  a unique content/anchor match; semantic relocation uses parser sections, normalized
  node/signature hashes and file identity.
- Added adversarial handling for formatting, movement, overload/duplicate ambiguity,
  rename, signature change, and delete/recreate so no unsafe case silently rebinds.
- Search now freezes the full unpaged match set behind an opaque result-set handle and
  reports source coverage, revisions, counts, overlap and all-match eligibility.
- Added monotonic result-set refinement with inherited coverage, parent lineage, and
  retained/eliminated counts. Current and explicitly historical set types are distinct.
- Updated modern MCP search, read, symbol-find and existing one-operation edit surfaces
  to issue or accept opaque handles while retaining human range locators.
- Added focused workspace and official-SDK bridge tests. No S09H Git provenance or S10
  durable transaction intent was implemented.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0:
  `ok agent99/internal/workspace` and `ok agent99/internal/bridge`.
- The focused race run covers exact inspection, TTL/epoch invalidation, unique range
  relocation and opaque edits; complete/capped/non-overlapping/stale result sets;
  monotonic refinement and historical/current separation; and format, move, signature,
  rename, duplicate and delete/recreate symbol cases.
- `make smoke` — exit 0; built `bin/agent99-bridge` and `bin/huyang`,
  then reported `unit_edit: OK`, `unit_testrun: OK`, `unit_check: OK`,
  `unit_index: OK`, `headless: OK`, `multi-workspace: OK`,
  `debug: OK` and `smoke: OK`.
- `go test ./...` — exit 0; all command, bridge, provider and workspace packages
  passed.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0 after the final documentation update; no output.

## Decisions and risks

- Handles are deliberately in-memory and service-local. Workspace epoch changes make
  restart invalidation explicit; persistence is outside S09.
- Presentation paging never truncates the private frozen match set. All-match resolution
  additionally requires complete, non-overlapping coverage and unchanged file manifest,
  document revisions, workspace revision and epoch.
- Refinements only add predicates and cannot broaden inherited coverage. Historical sets
  use a separate source kind, but their Git-backed production is deferred to S09H.
- Semantic symbol quality is bounded by the configured section-capable parser/provider.
  Without one, `workspace_symbol_find` remains truthfully partial; content-anchored
  range and match handles remain available in the provider-independent text core.
- Opaque handles augment the existing human locator forms. The edit integration is the
  existing single-operation preparation/application path, not a durable multi-operation
  plan.
- Language-server diagnostics on changed Go files contain only modernization hints and no
  errors or warnings.
- No install, deployment, installed-plugin update, live MCP restart, live
  configuration/state mutation, push, or pull request occurred.
- Exact next stage: S09H — Read-only Git source provenance.
