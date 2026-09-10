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

- Completed stage: S19D — Compact modern debugger facade.
- Starting commit: `a69be491f8c68313b27eaaa551fc279a7beb0723`.
- The reviewed base remains in branch history; the worktree was clean after fetching origin.
- Exact exit gates:
  - the four closed-action modern debug tools map to the proven DAP operations while the modern
    `full` and `debug` catalogs expose no legacy one-tool-per-action debugger names;
  - breakpoints and stopped source locations reuse revision-bound workspace targets/handles;
  - start/attach may include initial breakpoints, and stop/control results retain stop reason,
    top source location, compact locals, output changes, and stale-source coverage;
  - read-only evaluate refuses without execution unless enforceable purity exists, while explicit
    `allow_side_effects` is forwarded and disclosed;
  - missing adapters/runtime/source mappings return actionable `unavailable` or partial coverage
    without breaking orientation tools;
  - equivalent debugger workflows use fewer calls and fewer advertised schema bytes than the
    legacy roster, recorded in the S19D artifact;
  - `go test ./internal/bridge -run 'TestModernDebug|TestModernRegistry'`, `tests/smoke.sh debug`,
    `make smoke`, `go test ./...`, `go vet ./...`, and `git diff --check` all exit 0.
- S19D met every exit gate and is committed with this handoff.
- Exact next stage: S20 — Remaining wrappers, evaluation and release candidate.

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

S19 adds:

- docs/plans/huyang-s19-legacy-wrappers.md
- internal/workspace/commit.go
- internal/workspace/commit_test.go
- internal/bridge/legacy_state.go
- internal/bridge/legacy_wrapper.go
- internal/bridge/legacy_wrapper_test.go

S19D adds:

- docs/plans/huyang-s19d-debugger-facade.md
- internal/bridge/modern_debug.go
- internal/bridge/modern_debug_test.go
- modern debugger integration coverage in tests/drive_debug.py

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

## S19D changes

- Replaced four placeholder modern debug schemas with executable `debug_session`,
  `debug_breakpoints`, `debug_control`, and `debug_inspect` facades over the proven DAP provider.
- Added closed per-action arguments and complete mappings for all 19 facade actions while keeping
  the modern `full` and `debug` catalogs free of the legacy one-tool-per-action roster.
- Added initial breakpoints and shared revision-bound handle, exact file-range, and symbol targets;
  debugger source locations return registered handles and locators.
- Added stop-context normalization with state/reason, top location, locals, output/tracked/stale-source
  changes, stack locations, and explicit partial coverage when source mapping is unavailable.
- Made evaluation refuse without execution under the default read-only policy; explicit
  `allow_side_effects` is forwarded and disclosed in the result.
- Added actionable unavailable results for missing adapters/runtime and provider cleanup at MCP exit.
- Added real Delve smoke coverage for the modern profile while preserving the full legacy debugger
  smoke suite. The four modern descriptors total 7,505 bytes versus 8,383 for 13 legacy tools.
- Detailed evidence and scope: docs/plans/huyang-s19d-debugger-facade.md.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- `go test ./internal/bridge -run 'TestModernDebug|TestModernRegistry'` — exit 0.
- `tests/smoke.sh debug` — exit 0; legacy and modern real-Delve paths passed.
- `make smoke` — exit 0; unit, MCP, headless, multi-workspace, and debugger suites passed.
- `go test ./...` — exit 0; every Go package passed.
- `go vet ./...` — exit 0 with no output.
- `git diff --check` — exit 0 before final document updates; repeated before commit.

## Decisions and risks

- Modern action schemas advertise a closed action enum and closed property set; a compact server-side
  validator enforces action-specific required and irrelevant fields without duplicating every property
  in every action branch.
- Shared target validation now honors JSON Schema `oneOf` exactly and validates common root keywords;
  schemas with an intentionally empty union root continue to delegate closure to their alternatives.
- DAP cannot enforce expression purity, so read-only evaluation always refuses. Side effects require
  the explicit `allow_side_effects` policy and remain a documented debuggee-state risk.
- Provider errors are classified conservatively: missing debugger facilities degrade to unavailable,
  while unclassified adapter failures remain failed rather than being misrepresented as partial success.
- No deployment, installation, live-service/configuration mutation, installed-plugin update, push,
  or pull request occurred.
- Exact next stage: S20 — Remaining wrappers, evaluation and release candidate.
