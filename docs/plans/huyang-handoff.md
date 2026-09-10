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

- Completed stage: S09H — Read-only Git source provenance.
- Starting commit: `93d4db19d452e895c2364df7bc8a21f89dc324aa`.
- Reconciliation fetched origin and confirmed a clean `feature/huyang` branch, the reviewed
  base in branch history, S09 committed with its focused tests/artifact, and S09H as exactly
  the first incomplete checklist stage.
- S09H exit gates are satisfied: bounded first-parent overview commits and opaque handles;
  bounded history/change/search/file-age views; honest dirty/prepared mapping; sanitized,
  local-only Git execution; and adversarial merge, rename, shallow, missing-object, malicious
  config, binary, and submodule coverage.
- Only S09H is newly marked complete in the committed checklist.
- Exact next stage: S10 — Transaction intent, validation and preview.

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
- `docs/plans/huyang-s09-semantic-handles.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

S09H adds:

- `docs/plans/huyang-s09h-git-provenance.md`

## S09H changes

- Added a workspace-owned, optional Git provenance layer with opaque epoch/TTL-bound commit
  handles and a bounded three-commit first-parent workspace overview.
- Added exact-byte blame mapping for file, line/range, and semantic-handle history. Unmatched
  canonical spans are `uncommitted`; unmatched prepared spans are `derived_from_plan`;
  unchanged mapped spans retain committed provenance.
- Added recent touching commits and named file-age metrics with explicit ref, traversal,
  rename policy, shallow boundary, rename ambiguity, and provisional introduction evidence.
- Added bounded commit path/patch views, using explicit first-parent diffs for merges and
  typed binary/submodule coverage.
- Added local message/path/diff history search with typed historical frozen result sets that
  cannot be consumed by current-source all-match mutation.
- Hardened Git execution against repository/environment-configured hooks, pagers, fsmonitor,
  credential helpers, external diffs, textconv, repository redirection, object/index
  redirection, SSH/askpass, submodule recursion, optional locks, and network protocols.
- Updated the official-SDK `workspace_open`, `read`, and `search` surfaces. No S10
  durable transaction intent or prepared-plan state was implemented.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0:
  `ok agent99/internal/workspace` and `ok agent99/internal/bridge`.
- The focused race run covers bounded overview and commit handles; first-parent merge shape;
  rename following; shallow and missing-object degradation; dirty/prepared mapping; binary
  and gitlink histories; historical search/change views; malicious Git config non-execution;
  repository-inert HEAD/index/worktree state; and the official SDK surface.
- `make smoke` — exit 0; built both binaries and reported `unit_edit: OK`,
  `unit_testrun: OK`, `unit_check: OK`, `unit_index: OK`, `headless: OK`,
  `multi-workspace: OK`, `debug: OK`, and `smoke: OK`.
- `go test ./...` — exit 0; all command, bridge, provider, and workspace packages passed.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0 before final documentation; rerun after this handoff update
  and before commit.

## Decisions and risks

- Git is an optional provenance layer, never a workspace correctness dependency. Document
  workspaces and non-Git projects retain their text behavior and report history unavailable.
- Automatic overview results deliberately omit email, bodies, and path lists. Detailed paths
  and bounded patches require an explicit commit-handle read.
- Git subprocess output is capped at 2 MiB and calls at ten seconds. History/ref/path limits
  are fixed and capped; primary coverage flags are never hidden by truncation.
- Merge change views use an explicit first-parent comparison. File history uses bounded
  rename following; shallow or ambiguous introduction evidence remains provisional.
- Prepared bytes are accepted only as an explicit core input and labeled
  `derived_from_plan`. The later S10/S11 plan path will supply those bytes; S09H does not
  create transaction state early.
- Local repository config may still alter non-executable history semantics such as replace
  objects; detected replace/graft history is disclosed as incomplete rather than silently
  treated as canonical.
- Language-server diagnostics on changed Go files contain modernization hints only and no
  errors or warnings.
- No install, deployment, installed-plugin update, live MCP restart, live
  configuration/state mutation, push, or pull request occurred.
- Exact next stage: S10 — Transaction intent, validation and preview.
