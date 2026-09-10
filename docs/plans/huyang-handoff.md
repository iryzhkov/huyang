# Huyang implementation handoff

## Authority and invariant

- Repository: https://github.com/iryzhkov/agent99
- Standalone checkout: /home/igor/Work/huyang
- Branch: feature/huyang
- Reviewed base: 1c5302efe9ca51e1701e73df72d903f225fefe04
- Base ancestry check passed with git merge-base --is-ancestor.
- No install, deployment, installed-plugin update, live MCP restart, live
  configuration/state mutation, push, or pull request is authorized.

## Current checkpoint

- Completed stage: S17 — Diagnostic evidence and provenance.
- Starting commit: `bc5fc8f256d098c00abbddccab047fb278886dfb`.
- Reconciliation fetched origin, confirmed a clean `feature/huyang` branch, confirmed reviewed
  base `1c5302efe9ca51e1701e73df72d903f225fefe04` in branch history, read the complete plan,
  frozen tool contract, handoff, and every predecessor/stage artifact named below, and verified
  S17 was exactly the first incomplete checklist stage.
- S17 exit gates are satisfied: versioned push/pull/workspace/project-check normalization and
  deduplication; selected-provider provenance and conflict retention; coverage/confidence and
  transaction/revision/producer provenance; progress-only veto; durable acknowledged inbox,
  cursored evidence, and same-workspace notices; evidence-ranked attribution; provisional modern
  results when evidence is incomplete; fake permutations and real gopls/tsserver/pyright/lua_ls/bash
  cases never label incomplete evidence clean.
- Only S17 is newly marked complete in the committed checklist.
- Exact next stage: S18 — Impact graph, targeted tests and variants.

## Predecessor and stage artifacts

These tracked artifacts were read completely before implementation:

- docs/plans/huyang-s00-baseline.md
- docs/plans/huyang-s00-model-selection.md
- docs/plans/huyang-s01-package-boundary.md
- docs/plans/huyang-s02-provider-seam.md
- docs/plans/huyang-s03-embedded-provider-spike.md
- docs/plans/huyang-s04-embedded-provider.md
- docs/plans/huyang-s05-workspaces-revisions.md
- docs/plans/huyang-s06-text-core-documents.md
- docs/plans/huyang-s07-mcp-sdk-direct.md
- docs/plans/huyang-s08-service-scheduler.md
- docs/plans/huyang-s09-semantic-handles.md
- docs/plans/huyang-s09h-git-provenance.md
- docs/plans/huyang-s10-transaction-intent.md
- docs/plans/huyang-s11-exclusive-prepare.md
- docs/plans/fixtures/huyang-v1alpha1/contract-schema.json
- docs/plans/fixtures/huyang-v1alpha1/golden-results.json
- docs/plans/fixtures/huyang-v1alpha1/multi-provider.json
- docs/plans/huyang-s12-journaled-commit.md
- docs/plans/huyang-s13-crash-recovery.md
- docs/plans/huyang-s14-sandbox-backend.md
- docs/plans/fixtures/huyang-s14-sandbox-backends.json
- internal/sandboxspike/sandbox_spike_test.go
- docs/plans/huyang-s15-isolated-sandbox.md
- internal/workspace/sandbox.go
- internal/workspace/sandbox_linux.go
- internal/workspace/sandbox_other.go
- internal/workspace/sandbox_test.go

S16 adds:

- docs/plans/huyang-s16-repository-pipeline.md
- internal/workspace/pipeline.go
- internal/workspace/pipeline_test.go
- internal/bridge/pipeline_test.go

S17 adds:

- docs/plans/huyang-s17-diagnostic-evidence.md
- internal/workspace/evidence.go
- internal/workspace/evidence_test.go
- internal/bridge/evidence.go
- internal/bridge/evidence_test.go
- tests/huyang_diagnostics_init.lua

## S17 changes

- Added a durable, atomically persisted per-workspace diagnostic ledger with stable diagnostic
  and evidence IDs, cursored new/resolved notices, acknowledgement, and restart recovery.
- Added normalized push, pull, workspace and project-check evidence, whitespace normalization,
  same-provider push/pull deduplication, selected-provider enforcement, conflicting-provider
  retention, and incomplete-evidence-safe resolution.
- Added authoritative/corroborated/provisional/unavailable confidence and coverage, with
  version/result/progress/timeout barrier semantics and the weakest selected provider controlling
  a required dimension.
- Added exact/strong/likely/ambiguous/unattributed culprit ranking from sandbox, postimage,
  symbol and bounded impact-path facts; arrival timing alone never creates causality.
- Added structured Neovim `huyang_diagnostic_evidence`, including bounded attachment, recovered
  publish versions, pull payload/result IDs, producer identity/version and progress veto.
- Implemented modern `diagnostics`, `evidence_get`, common-envelope evidence IDs, and compact
  `diagnostic_updates` on later same-workspace replies.
- Wired prepared and explicit verification to the provider barrier. Configured repository checks
  remain separately identified corroborating fallback evidence.
- Modern direct edits and prepared plans no longer report `ok`/`READY` while semantic evidence is
  unavailable: they return `provisional`/`PROVISIONAL` with evidence and missing coverage.
- Added no S18 impact graph, affected-test selection, variants, legacy wrapper migration,
  debugger facade, deployment, installation, live configuration/state change, push, or pull request.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -run 'Diagnostic|Evidence|Provisional' -count=1`
  — exit 0; evidence core, provider barrier, inbox, provenance and provisional-state coverage passed.
- `go test ./internal/bridge -run TestRealLanguageDiagnosticBarriersNeverPromoteIncompleteEvidence -count=1 -v`
  — exit 0; real gopls, typescript-language-server, pyright, lua-language-server and
  bash-language-server cases passed. Pyright produced authoritative pull evidence; active gopls
  progress and unstamped/silent cases remained provisional rather than clean.
- First `make smoke` — exit 2 in headless search because the initial shared minimal test init
  enabled tsserver for JavaScript and lengthened one compact grep annotation. No production search
  code failed. The new LSP registrations were isolated into `tests/huyang_diagnostics_init.lua`.
- `make smoke SUITES=headless:search` — exit 0 after isolation; every focused search case passed.
- Final `make smoke` — exit 0; both binaries built and unit_edit, unit_testrun, unit_check,
  unit_index, MCP, headless, multi-workspace and debug suites reported OK.
- `go test ./...` — exit 0; every Go package passed.
- `go vet ./...` — exit 0 with no output.
- `git diff --check` — exit 0 after implementation and before final handoff/checklist updates;
  repeated after final documents below.

## Decisions and risks

- Diagnostic identity is provider-scoped. Identical push/pull payloads from one producer deduplicate;
  conflicting findings from distinct selected providers remain distinct and retain provenance.
- Pull evidence supersedes an incomplete push from the same producer. Across selected producers,
  the weakest evidence controls the required coverage dimension.
- Work-done progress is only a veto. documentSymbol, semantic tokens, elapsed silence and arrival
  order are never treated as authoritative completion or causal attribution.
- A successful configured project check may corroborate unavailable LSP evidence, but remains
  explicitly stored as `project_check`; it is not relabeled as LSP evidence.
- The LSP attachment wait is bounded to 1.5 seconds. Slow, unstamped or silent providers return
  provisional evidence and cannot resolve active findings or produce a clean verification stage.
- The durable inbox is response-reliable and restart-safe. Transport-native subscription delivery
  remains a compatible future enhancement over the same evidence IDs/cursors.
- Modern native range edits still apply exact guarded bytes before diagnostics, as in S06/S07, but
  now honestly return a provisional outcome when no semantic provider barrier exists.
- S17 records transaction, revision, state sequence, producer/version and evidence-ranked culprit
  candidates. S18 owns complete language-specific impact edges, transitive scope, variants and
  advisory targeted-test selection; no such completeness is claimed here.
- No deployment, installation, live-service/configuration mutation, installed-plugin update,
  push, or pull request occurred.
- Exact next stage: S18 — Impact graph, targeted tests and variants.
