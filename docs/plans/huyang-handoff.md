# Huyang implementation handoff

## Authority and invariant

- Source repository before the split: https://github.com/iryzhkov/agent99
- New repository targets: https://github.com/iryzhkov/huyang and https://git.ryzhkov.dev/igor/huyang
- Standalone checkout: /home/igor/Work/huyang
- Branch: feature/huyang
- Reviewed base: 1c5302efe9ca51e1701e73df72d903f225fefe04
- Base ancestry check passed with git merge-base --is-ancestor.
- On 2026-09-10 the user explicitly authorized the repository split, push, replacement
  deployment, live harness/configuration changes, and removal of Agent99 leftovers.

## Current checkpoint

- Completed stage: S20b — Structural consolidation after the hardening rounds, waves 0
  and 1. Record: `docs/plans/huyang-s20b-structural-consolidation.md`.
- Audit baseline: `f0b537d90d522ae79b7aabcc54ada1cbd374175c`, the last of the 26
  post-rollout hardening commits.
- Branch tip at this checkpoint: `fe143fe97dc7ac0060489c0d0840086a6c93122d` on
  `feature/huyang`.
- Wave 0, legacy removal and workspace fixes, committed directly on the branch:
  - `9978579d42efe0b1941e7ec919e60fa3332304af` remove legacy MCP catalog and tools
  - `58ac5e269d2e8104c66c5e0e771175393203391d` remove socket provider
  - `55eeb261de1b5deecc4dc2dcceb2d7b8ce5b7b75` remove embedded-provider and sandbox spikes
  - `93abf23b06adc13371154de62f967c5ed76e74be` remove legacy Lua kernel paths and harness leftovers
  - `367a2f40093a8b2d4818a706cafea6d7cc2be009` rename in-process agent99 identifiers to huyang
  - `f26c58205ebd2f934d10b69da4dac9086de51bfc` merge: workspace commit and plan lifecycle
    fixes (`d5bc7f8`, `6fd8c84`, `840b453`)
  - `96c52e1f56c84454552e76bb67753151ee61c65d` merge: workspace store, confinement and
    pipeline fixes (`e420f66`, `f898280`, `354043b`, `d77ea12`, `306dcb9`)
  - `88d36574b3700eb6d38b3f33508848d240de2bcd` store plans per record with bounded
    terminal retention
- Wave 1, provider and bridge:
  - `9adebee88761838b77f23350ee87a6c2c32d7abe` merge: provider protocol 2, cooperative
    cancellation and kernel cleanup (`b289aac`, `6074f88`)
  - `9f18be18bc455beaa31ebbe8c46b928a26ada47d` docs: record the S20b stage
  - `40af416f01be6adedc23b9146e58ba299d757c2f` merge: bridge concurrency, scheduling,
    compaction and receipt retention fixes (`b1a70f5`, `d8e52b2`, `39967ce`, `0d51310`,
    `21bb8f2`)
  - `fe143fe97dc7ac0060489c0d0840086a6c93122d` test margin for the advertised-timeout test
- Exit gates met at `fe143fe`: `go build`, `go vet ./...` and `go test ./...` green; the
  four profile catalogs match `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
  at 19/8/13/12 tools; every tool declares a scheduler class; no `AGENT99_`-only reader
  remains and no legacy tool, profile or endpoint ships.
- Wave 2 (documentation sweep, hardening-test consolidation, `internal/bridge` split,
  `make budget` gate) is planned in the stage record; the documentation sweep is the
  commit that carries this handoff.
- Normandy runs `fe143fe`; homelab and gaming-pc remain at `4f87241` and need the redeploy
  procedure in `docs/plans/huyang-rollout-2026-09-10.md`.

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
- internal/sandboxspike/sandbox_spike_test.go (spike source removed after its decision record was committed)
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
- modern debugger coverage in internal/bridge/modern_debug_test.go

S20 adds:

- docs/plans/huyang-s20-release-candidate.md
- remaining wrapper migration in internal/bridge/legacy_wrapper.go and legacy_state.go
- wrapper coverage in internal/bridge/legacy_wrapper_test.go (removed with the wrappers in S20b)
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
- The user superseded the planned Agent99 executable/UI deprecation window by explicitly
  requesting complete replacement and removal during rollout.
- Exact next stage: none. Rollout was explicitly authorized and is tracked below.

## Post-rollout hardening and S20b

Between the rollout at `4f87241` and the S20b audit baseline `f0b537d`, agent-led probe
sessions landed 26 hardening commits (`817f0fb` through `f0b537d`) against the deployed release
candidate: MCP recovery and probe workflows, sandbox copies and history counts, bounded
responses, creation and verification probes, verification latency and recovery, pipeline
policy bootstrap, embedded language-server attachment and administration (which added
`language_server_status` and `language_server_setup`), multilingual plan and LSP recovery,
Java and Rust toolchain recovery, transport recovery, exact plan recovery across service
restarts, and the shipped debugger runtime. Each fixed a real defect and left a regression
test, but the fixes accumulated as symptoms rather than causes: string-prefix parsing,
substring error classification, unbounded persistence, and a service that reached 8 GB
RSS behind a 524 MB `registry.json`.

S20b consolidated them. Wave 0 deleted the legacy tool layer, socket provider and spikes
(57 files, 9,681 lines) and fixed the workspace transaction and store layers with typed
error codes, an enforced plan state machine including `CONFLICTED` and `EXPIRED`,
content-derived revisions, per-plan record files and bounds on every store. Wave 1 moved
the kernel to protocol 2 with cooperative cancellation, keyed stagers and scheduler classes
per tool, compacted the results, and moved receipts out of `registry.json` into bounded
per-workspace files. The complete finding list, decisions and consequences for the later
plans are in `docs/plans/huyang-s20b-structural-consolidation.md`; the amended contract is
in `docs/plans/huyang-tools-v1alpha1.md`.

## Post-S20 repository split and rollout

- Started from `83009bc6caa3ef363bbdf140a9d31992c190ee44`; the reviewed base remains an ancestor.
- Renamed the Go module to `github.com/iryzhkov/huyang` and the embedded Lua kernel to
  `lua/huyang`.
- Removed the interactive Agent99 UI, embedded LLM agent loop, `agent99-bridge` command,
  and legacy Python/UI smoke suites. Huyang now ships one executable, `bin/huyang`.
- The embedded provider is the default; `HUYANG_PROVIDER_BACKEND` is primary while the
  historical environment alias remains accepted for migration.
- Added `contrib/systemd/huyang.service`.
- Pre-publish verification on 2026-09-10: `make smoke`, `go test ./...`,
  `go vet ./...`, and `git diff --check` all exited 0.
- Both new public repositories default to `main` at `4f87241`. Normandy, Homelab, and
  Gaming PC run the enabled Huyang user service and advertise only Huyang to Claude, Codex,
  and OpenCode; each passed MCP initialize and the 17-tool full-profile catalog check.
- Agent99 was removed from the shared Neovim configuration in `a8c430a`, pushed to both
  configured upstreams, pulled on the reachable hosts, and removed from their active lazy.nvim
  installations.
- `CLAUDE.md`, `AGENTS.md`, OpenCode instruction references, and the generated shared
  guide now describe Huyang's modern revision/transaction/evidence workflow.
- Exact deployment evidence and recovery state are in
  `docs/plans/huyang-rollout-2026-09-10.md`.
- Laptop is explicitly deferred at the user's direction. It remains unchanged and is outside
  this completed rollout; no successor is queued.
