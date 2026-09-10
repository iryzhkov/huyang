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

- Completed stage: S19 — First legacy wrapper families.
- Starting commit: `3edeaef629cbbfa4a70fd7fefb200849693abfdd`.
- S19 migrates symbol/range and create/move/delete wrappers through isolated prepared deltas
  and one-operation commit journals while preserving the legacy schemas and response text.
- The reviewed base remains in branch history. Only S19 is checked complete.
- Exact next stage: S19D — Compact modern debugger facade.
- S19D and S20 work were not started.

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

S18 adds:

- docs/plans/huyang-s18-impact-tests-variants.md
- internal/workspace/impact.go
- internal/workspace/impact_test.go
- targeted/full verification integration in internal/workspace/pipeline.go,
  internal/bridge/modern_mcp.go, and internal/bridge/pipeline_test.go

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

## S18 changes

- Added bounded revision-keyed impact graphs with Go, TypeScript/JavaScript, Python, and Lua
  dependency adapters, transitive reverse impact, adapter/confidence provenance, and file/edge caps.
- Added explicit risks for unresolved dependencies, reflection/dynamic loading, generated code,
  configuration/schema files, unreadable files, graph caps, untested affected nodes, and omitted
  required variants.
- Extended trusted project policy with named test suites, path associations, variants, required
  suites, named build/configuration variants, and impact caps.
- Added advisory affected-test selection from affected paths, colocated tests, required suites,
  variants, and prior revision-keyed failures. Selected and executed suites retain reasons.
- Persisted bounded atomic test history by workspace and exact revision, including scope, suite,
  variants, status, duration, and timestamp.
- Kept `affected_tests_passed` separate from `full_tests_passed`; targeted verification never
  sets `full_test_gate`.
- Added no S19 legacy-wrapper migration, S19D debugger facade, S20 release work, deployment,
  installation, live configuration/state mutation, push, or pull request.

## S19 changes

- Added exact sandbox-to-canonical delta discovery and a journaled prepared-transaction commit API.
- Symbol/range edits keep the canonical provider for parser, import, diagnostic, token, async, and
  per-client compatibility, then capture exact dirty buffers into a sandbox before journal commit.
- File create/move/delete run in isolated trees and commit exact create/replace/delete deltas.
- Retained per-client receipts preserve legacy file-operation undo, stale refusal, and skip behavior.
- Added global/per-tool direct escape flags; dry runs and wait=false preserve their old direct paths.
- Added exact snapshot, journal, escape, sandbox-delta, drift, and state-sequence regression tests.
- Detailed evidence and scope: docs/plans/huyang-s19-legacy-wrappers.md.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- `go test ./internal/workspace ./internal/bridge` — exit 0.
- `tests/smoke.sh headless:edit headless:verdict headless:files headless:clients` — exit 0.
- `make smoke` — exit 0; unit, MCP, headless, multi-workspace, and debugger suites passed.
- `go test ./...` — exit 0; every Go package passed.
- `go vet ./...` — exit 0 with no output.
- `git diff --check` — exit 0 before final document updates; repeated before commit.

## Decisions and risks

- Synchronous symbol/range calls use canonical provider buffers for exact semantic behavior, capture
  those bytes into isolation, then accept the journaled disk image without losing provider state.
- File lifecycle sandboxes remain until their per-client undo receipt is consumed or the workspace
  closes. State lives under the configured Huyang state root, mode 0700.
- `wait=false` remains direct because consuming its deferred verdict during synchronous commit would
  break the frozen response/timing contract.
- Undo overlap merging is strict for ordinary calls; only `all=true` may overwrite an overlapping
  older wrapper delta after the provider has accepted the complete legacy undo.
- No deployment, installation, live-service/configuration mutation, installed-plugin update, push,
  or pull request occurred.
- Exact next stage: S19D — Compact modern debugger facade.
