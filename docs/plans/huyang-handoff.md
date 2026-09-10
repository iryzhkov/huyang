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

- Completed stage: S20 — Remaining wrappers, evaluation and release candidate.
- Starting commit: `26864fe3a890d423d94701fddb3cfde27deffc75`.
- The reviewed base remains in branch history; the worktree was clean after fetching origin.
- Exact exit gates:
  - rename, code-action, homogeneous replace-matches, and move-symbol legacy mutation families
    use the isolated journaled transaction path while preserving guarded targets, exact legacy
    responses, undo, dry-run/`wait=false`, and documented direct escape behavior;
  - test/check wrappers use revision-keyed verification pipeline results without conflating
    affected and full coverage or executing untrusted commands;
  - all protocol/client/language/failpoint suites pass, and paired modern/legacy evaluation
    records calls, schema/result size proxies, recovery behavior, and quality without regression;
  - compatibility window, recovery guide, protocol/kernel/transaction version promises,
    migration guide, and deployment-readiness report are tracked release-candidate artifacts;
  - S20's focused tests, `make smoke`, `go test ./...`, `go vet ./...`, and
    `git diff --check` all exit 0;
  - the definition of done is reconciled honestly, the branch is committed and clean, and no
    deployment, installation, live service/configuration mutation, push, or pull request occurs.
- S20 met every exit gate and is committed with this handoff.
- Exact next stage: none. Await explicit rollout approval; do not install or deploy.

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

S20 adds:

- docs/plans/huyang-s20-release-candidate.md
- remaining wrapper migration in internal/bridge/legacy_wrapper.go and legacy_state.go
- wrapper coverage in internal/bridge/legacy_wrapper_test.go and tests/drive_headless.py
- revision-keyed verification caching in internal/bridge/modern_mcp.go
- cache and catalog evaluation coverage in internal/bridge/pipeline_test.go and
  internal/bridge/modern_mcp_test.go
- complete sealed/current hash and timestamp conflict evidence in internal/workspace/sandbox.go

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

## S20 changes

- Routed `rename_symbol`, `apply_code_action`, `replace_pattern`, and
  `move_symbols` through the journaled legacy-wrapper dispatcher by default.
- Extended provider capture to include every modified loaded buffer under the canonical
  root, so LSP rename/code-action and glob replacement effects are discovered even when
  their complete write set is not present in the request.
- Ran `move_symbols` in a dedicated sandbox-rooted provider to prevent its file creation
  and LSP/import behavior from touching canonical bytes before commit. Retained that
  provider only with the undo receipt; undo runs against the sandbox ledger and commits a
  compensating canonical transaction, then closes it.
- Preserved legacy exact rendering, client undo, dry-run/`wait=false`, and global/per-tool
  direct escapes. Canonical provider buffers are flushed before file-family snapshots.
- Added revision-keyed `verify_run` caching by workspace ID, exact canonical/prepared
  revision, ordered stages, and test scope, independently of transport idempotency replay.
- Added sealed/current SHA-256 and mtime evidence to sandbox drift errors.
- Published the compatibility window, recovery guide, protocol/kernel/durable-format
  promises, migration guide, paired catalog/task evaluation, risks, and deployment-readiness
  decision in `docs/plans/huyang-s20-release-candidate.md`.
- Added no deployment, installation, live-service/configuration mutation, installed-plugin
  update, push, or pull request.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- `go test ./internal/bridge -run 'TestLegacy|TestOfficialClientRunsTrustedPipelineAgainstPreparedSandbox|TestModernCatalogTokenBudgets|TestModernRegistry' -count=1` — exit 0.
- `go test ./internal/bridge -run TestModernCatalogTokenBudgets -count=1 -v` — exit 0;
  recorded modern full/orient/edit/debug as 17/8/13/12 tools and 17,181/6,774/13,495/10,460
  cl100k tokens, versus 9,014/9,324 for the 35/38-tool legacy ordinary catalogs.
- First `make smoke` — exit 2 in `headless:files`: `move_symbols` created its destination
  through the canonical provider before journal commit, correctly triggering
  `sandbox_source_changed`. No repository file was changed by the fixture.
- `go build -o bin/agent99-bridge ./cmd/agent99-bridge && tests/smoke.sh headless:files`
  after the dedicated sandbox-provider and retained-undo fix — exit 0.
- Final `make smoke` — exit 0; unit, MCP, full headless, multi-workspace, legacy debugger,
  and modern debugger suites passed.
- `go test -race ./internal/workspace ./internal/bridge` — exit 0.
- `go test ./...` — exit 0; every Go package passed.
- `go vet ./...` — exit 0 with no output.
- `git diff --check` — exit 0 before final document updates; repeated before commit.

## Decisions and risks

- Provider-driven mutation families keep the proven Lua targeting and diagnostic behavior;
  Huyang journals their exact resulting buffers instead of reimplementing rename, code-action,
  pattern, or move semantics in Go.
- `move_symbols` needs a sandbox-rooted provider because it may create a destination on disk
  and invoke import/LSP behavior. Its provider is retained only until the associated undo
  receipt is consumed or the workspace closes.
- Revision-cache reuse requires an exact workspace/revision/stage/scope key. It is in-memory and
  deliberately does not replace durable idempotency records or revision-keyed test history.
- The portable modern `full` catalog has a disclosed raw-schema premium over the ordinary
  legacy catalog. Task-selected `orient`/`edit` profiles plus reduced lifecycle/recovery
  calls make the frozen paired workload effectively schema-neutral before result savings.
- LSP rename/code actions can still report partial coverage or provider-specific workspace-edit
  behavior. The journal discovers touched loaded buffers, but coverage claims remain evidence-bound.
- The multi-file journal provides the documented cooperative recovery guarantee, not
  filesystem-wide atomicity. Sandbox fallback/resource and trusted-command risks remain.
- Compatibility lasts through the first stable release and at least 90 days after modern-default
  rollout, with one tagged deprecation release and at least 30 days before removal.
- No deployment, installation, live-service/configuration mutation, installed-plugin update,
  push, or pull request occurred.
- Exact next stage: none. Await explicit rollout approval.
