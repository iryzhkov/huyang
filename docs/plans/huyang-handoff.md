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

- Completed stage: S13 — Crash recovery and compensating undo.
- Starting commit: 7185f4661d166dce4f2e86b3c3b66cca5aeee8c0.
- Reconciliation fetched origin, confirmed a clean `feature/huyang` branch, confirmed reviewed
  base `1c5302efe9ca51e1701e73df72d903f225fefe04` in branch history, read every declared
  predecessor artifact, and verified S13 was exactly the first incomplete checklist stage.
- The S12 focused race gate passed before S13 code editing.
- S13 exit gates are satisfied: startup recovery handles every incomplete journal state before
  returning an open root; recovery restores exact preimages only from recognized transaction
  postimages; third-party bytes remain untouched with explicit RECOVERY_REQUIRED; committed undo
  is a journaled provider-staged compensating transaction; and separate-process SIGKILL coverage
  spans both sides of every commit/recovery journal and canonical replacement boundary.
- Only S13 is newly marked complete in the committed checklist.
- Exact next stage: S14 — Sandbox backend decision spike.

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

## S13 changes

- Added startup scanning and rollback for prepared, applying, and recovery-required commit
  journals before a workspace root becomes available.
- Added exact logical-image matching over bytes, kind, size, mode, and symlink target, inverse
  durable write ordering, per-path restored progress, and idempotent completed recovery.
- Added fail-closed journal validation and a final pre-replacement recheck. A third-party image
  observed before or during recovery is retained, the journal remains RECOVERY_REQUIRED, and
  workspace open returns `commit_recovery_required`.
- Added before/after failpoints for every prepared/applying/progress/committed journal write
  while retaining the canonical before/after apply failpoints.
- Added `CompensatePlan`: an exact-image provider-staged transaction with its own journal,
  canonical revision, complete committed-postimage precondition, mode/symlink support, and
  idempotent committed replay.
- Added separate-process SIGKILL tests at fourteen commit boundaries and six recovery
  boundaries, plus incomplete-state, external-race, and exact compensating-undo tests.
- Kept legacy `undo_edit` unchanged as the compatibility adapter; wrapper migration remains
  in its later named stages.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -run
  'TestJournaledCommit|TestOfficialClientAppliesJournaled|TestOfficialClientPreparesExclusive'
  -count=1` — exit 0 before editing; the S12 predecessor gate remained green.
- `go test -race ./internal/workspace -run
  'TestStartupRecovery|TestCompensating|TestCrashRecovery|TestJournaledCommit' -count=1`
  — exit 0: all incomplete states, third-party preservation, compensating undo, fourteen
  separate-process commit kills, and six separate-process recovery kills passed.
- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0 after the final
  implementation; both packages passed.
- `make smoke` — exit 0: built both binaries and ended unit_edit, unit_testrun, unit_check,
  unit_index, headless, multi-workspace, debug, and smoke with OK.
- `go test ./...` — exit 0 after the final implementation; every Go package passed.
- `go vet ./...` — exit 0 with no output after the final implementation.
- `git diff --check` — exit 0 after the final code, test, artifact, checklist, and handoff
  updates.

## Decisions and risks

- Startup recovery runs after durable plan loading but before `Open` returns. The provider is
  not opened until later, so recovery always precedes semantic access to a conflicted root.
- Recovery accepts only an exact logical preimage or postimage. Device, inode, and mtime cannot
  be restored by an atomic replacement and are treated as observations; bytes, kind, size,
  permission bits, and symlink target remain exact.
- Recovery uses reverse durable commit order and records restored progress after every path.
  Completed rollback journals are retained and reconcile plan state idempotently on later opens.
- The final image check occurs immediately before replacement. Ordinary filesystems cannot
  provide a pathname compare-and-swap, so an arbitrary writer can still race the final check and
  rename; this unavoidable limitation remains inside the documented cooperative guarantee.
- A corrupt, foreign-workspace, unsupported, or conflicting journal fails closed before the
  root opens. No recovery path guesses or overwrites an unrecognized image.
- Compensating undo is a distinct provider-staged journal whose preimages are the committed
  postimages. It does not rewind history implicitly and cannot apply after a third-party change.
- The frozen modern `change_plan` action union is unchanged. Legacy `undo_edit` remains its
  existing compatibility adapter until the named wrapper migration stages consume the new core.
- Completed commit and compensation journals remain retained; garbage collection is still later
  maintenance work.
- Existing unrelated Python language-server diagnostics remain pre-existing; Go race, test, vet,
  smoke, and diff gates pass.
- No sandbox decision or implementation, install, deployment, installed-plugin update, live MCP
  restart, live configuration/state mutation, push, or pull request occurred.
- Exact next stage: S14 — Sandbox backend decision spike.
