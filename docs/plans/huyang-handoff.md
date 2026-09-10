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

- Completed stage: S12 — Journaled commit.
- Starting commit: d1417ac5ca33f940a47f690e1234659d5c19ec6f.
- Reconciliation fetched origin and confirmed a clean feature/huyang branch, the reviewed
  base in branch history, S11 committed with its focused tests/artifact, and S12 as exactly
  the first incomplete checklist stage.
- The S11 predecessor race gate passed before S12 editing.
- S12 exit gates are satisfied: the entire prepared canonical write set is revalidated;
  exact preimages, postimages, modes, symlink targets, and per-path progress are durably
  journaled and synced before writes; normal create/replace/move/delete applies through
  same-directory temporary writes, file sync, rename, and directory sync; the provider is
  resynced after success; and ordinary injected failures persist RECOVERY_REQUIRED state.
- The implementation retains the cooperative journal guarantee and does not claim
  filesystem-wide atomicity. Startup recovery and compensating undo remain S13.
- Only S12 is newly marked complete in the committed checklist.
- Exact next stage: S13 — Crash recovery and compensating undo.

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

## S12 changes

- Added COMMITTING, COMMITTED, and RECOVERY_REQUIRED plan states plus canonical revision and
  journal identity on successful preparation records.
- Added a versioned write-ahead commit journal with exact pre/post bytes, filesystem kind,
  permissions, symlink target, deterministic write ordering, and durable per-path progress.
- Added whole-write-set revalidation immediately before mutation. A stale revision, content,
  inode, mode, object kind, or symlink target refuses apply before the first path changes.
- Added same-directory temp-write/file-sync/rename/directory-sync regular and binary writes,
  same-directory temporary symlink rename, and synced deletes. Moves preserve the source
  object's bytes, kind, mode, and symlink target.
- Added explicit provider commit/resync after canonical writes. Provider buffers retain the
  prepared postimages, become clean canonical buffers, deleted buffers close, and the provider
  transaction is released.
- Wired the frozen modern apply branch to require plan revision plus prepared revision and
  return COMMITTED through the official SDK path.
- Added focused race-tested core and embedded-provider coverage for normal filesystem commits,
  exact journal records, durable failure state, pre-write stale refusal, and provider resync.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -run
  'TestJournaledCommit|TestOfficialClientAppliesJournaled|TestOfficialClientPreparesExclusive'
  -count=1 -v` — exit 0: journal, stale refusal, recovery record, prepare/discard, and real
  embedded-provider apply/resync cases passed.
- `go test -race ./internal/workspace ./internal/provider/embed ./internal/bridge -count=1`
  — exit 0 after the final implementation: all three packages passed.
- `make smoke` — exit 0: built both binaries and ended unit_edit, unit_testrun,
  unit_check, unit_index, headless, multi-workspace, debug, and smoke with OK.
- `go test ./...` — exit 0 after the final implementation; all Go packages passed.
- `go vet ./...` — exit 0 with no output after the final implementation.
- `git diff --check` — exit 0 after the final implementation, artifact, checklist, and
  handoff updates.

## Decisions and risks

- Journals are retained after COMMITTED for later garbage collection rather than deleted at
  the success boundary. S13 can therefore exercise and audit the same durable format used by
  ordinary commits.
- Write ordering creates/replaces destinations before deleting sources. This narrows the
  ordinary move failure mode to a recoverable duplicate rather than premature source loss.
- Exact preconditions include object kind and the complete observed disk snapshot, not content
  hash alone. Symlinks are copied as links and never followed for mutation.
- The provider does not stage non-text filesystem objects. Their exact state is owned by the Go
  journal; regular text buffers still provide the coherent prepared semantic view.
- A post-write error does not pretend the workspace rolled back. The journal and plan remain
  RECOVERY_REQUIRED with exact pre/post state and progress; S13 implements startup recovery,
  kill-step coverage, third-party race protection, and compensating undo.
- A COMMITTING record found during plan load is conservatively changed to RECOVERY_REQUIRED,
  but no automatic filesystem recovery is performed ahead of S13.
- Commit success advances the workspace state sequence exactly once and invalidates cached
  canonical document observations before returning the new canonical revision.
- Disk-based formatters/checks/tests remain unavailable in exclusive buffer-backed prepare.
  Repository verification and authoritative diagnostic evidence remain S16 and S17.
- Existing unrelated Python language-server diagnostics remain pre-existing; all Go race,
  test, vet, smoke, and diff gates pass.
- No install, deployment, installed-plugin update, live MCP restart, live configuration/state
  mutation, push, or pull request occurred.
- Exact next stage: S13 — Crash recovery and compensating undo.
