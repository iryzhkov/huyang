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

- Completed stage: S15 — Isolated sandbox prepare.
- Starting commit: 2d4fe9b5bf4e95aac3d6e34bf1371a9198798c3c.
- Reconciliation fetched origin, confirmed a clean `feature/huyang` branch, confirmed reviewed
  base `1c5302efe9ca51e1701e73df72d903f225fefe04` in branch history, read the complete plan,
  frozen tool contract, handoff, and every predecessor/stage artifact named below, and verified
  S15 was exactly the first incomplete checklist stage.
- The S14 portable and intended-filesystem spike gates passed before production editing.
- S15 exit gates are satisfied: exact stable snapshots, reflink/safe-copy fallback, quotas,
  ownership and cleanup, per-sandbox providers, canonical path mapping, bounded parallel
  same-workspace preparation, stable canonical reads, verified prepared bytes, and commit-time
  canonical conflict checks are implemented and covered.
- Only S15 is newly marked complete in the committed checklist.
- Exact next stage: S16 — Repository formatting and verification pipeline.

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

S12 adds:

- docs/plans/huyang-s12-journaled-commit.md

S13 adds:

- docs/plans/huyang-s13-crash-recovery.md

S14 adds:

- docs/plans/huyang-s14-sandbox-backend.md
- docs/plans/fixtures/huyang-s14-sandbox-backends.json
- internal/sandboxspike/sandbox_spike_test.go

S15 adds:

- docs/plans/huyang-s15-isolated-sandbox.md
- internal/workspace/sandbox.go
- internal/workspace/sandbox_linux.go
- internal/workspace/sandbox_other.go
- internal/workspace/sandbox_test.go

## S15 changes

- Added production exact-tree inventory and snapshot materialization with stable source hashing,
  per-file clone-only reflinks, whole-candidate safe-copy fallback, special-file refusal,
  cancellation, S14 quotas, and destination-manifest verification.
- Added service-owned versioned markers, confined cleanup, and startup reaping that preserves
  referenced or foreign directories.
- Replaced the canonical exclusive preparation provider with one embedded provider per sandbox.
  Prepared postimages are present and verified on sandbox disk; results expose canonical paths
  with sandbox provenance and never expose sandbox paths as canonical locators.
- Changed scheduling so two same-workspace preparations and four service-wide preparations can
  run concurrently subject to provider quota. Apply and other canonical plan mutations retain
  the canonical lane.
- Canonical provider reads no longer block behind isolated prepared state. Existing journaled
  apply still performs the complete canonical precondition check, so competing writers may both
  prepare and one conflicts at apply after the other changes its base.
- Added sandbox, parallel preparation, quota/cancellation, SDK/provider lifecycle, discard,
  cleanup, and canonical stability coverage.
- Added no formatter/check/test pipeline, diagnostic evidence store, installation, deployment,
  installed-plugin update, live configuration/state change, push, or pull request.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- `go test -v ./internal/sandboxspike -count=1` — exit 0; safe-copy fallback,
  manifest/isolation, special-file, and capability tests passed on the temporary filesystem;
  unsupported reflinks skipped explicitly.
- `HUYANG_SANDBOX_SPIKE_DIR=/home/igor/Work go test -v ./internal/sandboxspike -count=1`
  — exit 0; reflink and safe-copy paths, exact manifest/isolation, special-file refusal, and
  capability reporting passed on the intended Btrfs filesystem.
- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0; sandbox,
  parallel-prepare, scheduler, provider, transaction, journal, recovery, SDK, and service
  focused suites passed.
- `go test ./...` — exit 0; every Go package passed.
- `make smoke` — exit 0; both binaries built and unit_edit, unit_testrun, unit_check,
  unit_index, headless, multi-workspace, debug, and final smoke markers reported OK.
- `go vet ./...` — exit 0 with no output.
- `git diff --check` — exit 0 after final code, tests, artifact, checklist, and handoff
  updates.

## Decisions and risks

- Exact-by-default means ignored and in-tree Git entries are copied; no ignore convention is
  treated as authorization to verify different bytes.
- Reflink is capability-probed per source/destination pair. Only unsupported clone errors choose
  safe copy; source change, quota, permission, cancellation, and special-file failures remain
  explicit failures.
- Safe copy conservatively counts logical bytes toward its materialization limit. It may reject
  a very large sparse tree that a future allocation-aware implementation could admit, but cannot
  exceed the configured copy limit silently.
- Each sandbox has a separate provider and transaction-local serialization. The fixed two/four
  sandbox caps are the reviewed S14 defaults and remain further bounded by provider quota.
- Prepared sandbox files are exact postimages. The provider retains its semantic staged buffers
  until discard/apply, then closes before the owned tree is removed.
- Cleanup validates the single-child resolved location and marker identity before recursive
  removal. Invalid or foreign directories are retained rather than guessed safe.
- Startup currently reaps all unreferenced valid sandbox markers because restored plan loading
  invalidates process-local prepared providers. Referenced directories and foreign entries stay.
- Trusted configured exclusions, formatting, checks, tests, generator write-set enforcement,
  and full tool delta belong to S16; diagnostic provenance/evidence storage belongs to S17.
- No deployment, installation, live-service/configuration mutation, installed-plugin update,
  push, or pull request occurred.
- Exact next stage: S16 — Repository formatting and verification pipeline.
