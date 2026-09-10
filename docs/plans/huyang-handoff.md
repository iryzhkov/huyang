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

- Completed stage: S16 — Repository formatting and verification pipeline.
- Starting commit: `5c08a65d383d7393c9e25125837d04edf2836834`.
- Reconciliation fetched origin, confirmed a clean `feature/huyang` branch, confirmed reviewed
  base `1c5302efe9ca51e1701e73df72d903f225fefe04` in branch history, read the complete plan,
  frozen tool contract, handoff, and every predecessor/stage artifact named below, and verified
  S16 was exactly the first incomplete checklist stage.
- S16 exit gates are satisfied: layered trust/resource policy, byte-preserving defaults,
  explicit transforms, syntax-anchor and formatter indentation, protected-byte and formatter
  scope enforcement, parser/configured format/check/test stages against sandbox bytes, bounded
  whole-tree write detection, exact rollback, complete `tool_delta`, and transformed postimages
  carried through prepared revisions and journaled canonical apply.
- Only S16 is newly marked complete in the committed checklist.
- Exact next stage: S17 — Diagnostic evidence and provenance.

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

## S16 changes

- Added versioned `.huyang.toml` project declarations and layered XDG user trust/resource
  policy. Repository commands run only for explicitly trusted resolved roots, with user ceilings
  able to tighten project/default budgets; `workspace_inspect` exposes the effective policy.
- Added exact, syntax-anchor, and formatter indentation policies. Syntax anchoring requires a
  semantic-node handle and refuses mixed/ambiguous leading whitespace; formatter execution
  requires visible project or operation policy.
- Added sandbox-only formatter transforms plus configured non-mutating format gates, parser
  checks, static checks, and full tests. `verify_run` implements the frozen
  `revision_or_transaction` contract and stateful replay against live prepared sandboxes.
- Added bounded whole-sandbox command snapshots, sanitized environments, output/time/file/byte
  ceilings, declared-write enforcement, protected Go literal/comment checks, second-pass
  formatter determinism, exact rollback, and structured per-stage/write results.
- Formatter changes, including configured whole-repository changes outside the original
  operation set, are carried into `tool_delta`, the prepared revision hash, the provider view,
  the commit journal, and canonical apply.
- Added no S17 durable evidence/provenance model, S18 impact graph/affected-test selection,
  installation, deployment, installed-plugin update, live configuration/state change, push,
  or pull request.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -run 'Pipeline|Verification|SyntaxAnchor' -count=1`
  — exit 0; focused policy, formatting, parser, rollback, timeout, nondeterminism, syntax-anchor,
  SDK, provider, prepared/canonical `verify_run`, `tool_delta`, and transformed-apply coverage passed.
- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0; all workspace,
  transaction, sandbox, pipeline, provider, scheduler, SDK, commit, and recovery tests passed.
- `make smoke` — exit 0; both binaries built and unit_edit, unit_testrun, unit_check,
  unit_index, headless, multi-workspace, debug, and final smoke markers reported OK.
- `go test ./...` — exit 0; every Go package passed.
- `go vet ./...` — exit 0 with no output.
- `git diff --check` — exit 0 after final code, tests, artifact, checklist, and handoff updates.

## Decisions and risks

- Project configuration declares argv; Huyang never inserts a shell. Trust is user-owned and
  resolved-root based, and command environments are sanitized.
- Rollback snapshots default to 64 MiB and fail closed before command launch when the configured
  surface is too large. Project policy may raise the ceiling; user policy can only tighten it.
- Parser coverage in S16 is exact UTF-8 plus built-in Go and JSON parsing. Other parser/provider
  dimensions remain explicit coverage gaps rather than false success.
- Protected literal/comment comparison is syntax-aware for Go formatter transforms. Other
  languages retain exact delta, scope, idempotency, and rollback enforcement without claiming
  syntax-aware literal protection.
- Declared formatter scope is currently enforced at file granularity. Complete within-file bytes
  remain visible in `tool_delta`; later language adapters may tighten this to parser-declared
  syntactic envelopes.
- Non-mutating stages are rolled back and fail if they write. Transform stages must be
  second-pass stable; cancellation, timeout, non-zero exit, undeclared/special-file writes, quota
  breaches, and protected-byte changes restore the exact pre-command sandbox tree.
- Whole-repository formatter changes are accepted only through the exact inspected
  `prepared_revision`; every resulting path is journaled rather than silently omitted.
- `verify_run` accepts prepared transaction IDs/revisions and the exact current canonical
  workspace revision; canonical runs use temporary exact snapshots and stale revisions conflict.
  Durable work/evidence IDs and restart-surviving command evidence belong to S17.
- Affected-test selection returns an explicit S18 coverage gap; full configured tests run in S16.
- No deployment, installation, live-service/configuration mutation, installed-plugin update,
  push, or pull request occurred.
- Exact next stage: S17 — Diagnostic evidence and provenance.
