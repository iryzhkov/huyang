# Huyang S13 crash recovery and compensating undo

Captured: 2026-09-10
Stage: S13 — Crash recovery and compensating undo
Starting commit: 7185f4661d166dce4f2e86b3c3b66cca5aeee8c0

## Delivered scope

- Workspace construction scans its service-owned commit-journal directory after durable plan
  loading and before returning an open root. Prepared, applying, and recovery-required journals
  all enter the same exact-image rollback path; committed journals remain receipts and completed
  rollback journals reconcile their plan record idempotently.
- Recovery walks the inverse durable path order. A canonical object that still equals its exact
  preimage needs no write; one that equals the transaction postimage is restored through the same
  same-directory temporary-write, sync, rename, and directory-sync primitive. Each restored path
  and the completed rollback state are journaled durably.
- Recovery compares exact bytes, filesystem kind, mode, size, and symlink target. Device, inode,
  and mtime are observations rather than desired restored identities because a safe atomic restore
  necessarily creates a new inode.
- A path that matches neither image is never overwritten. Recovery records
  `recovery_required`, retains the journal, returns `commit_recovery_required`, and refuses to
  open the root. Recovery rechecks the postimage immediately before replacement so a third-party
  write injected after initial inspection is also preserved.
- Commit persistence now exposes failpoints immediately before and after every prepared,
  applying, per-path-progress, and committed journal write. Existing per-path before/after apply
  failpoints bracket the canonical replacement.
- `CompensatePlan` implements durable undo as a new provider-staged transaction whose exact
  journal swaps every committed preimage and postimage. It revalidates the complete committed
  postimage set before staging, restores regular/binary bytes, modes, missing paths, and symlink
  targets, advances the canonical state sequence once, and replays an already committed
  compensation idempotently.
- The legacy `undo_edit` implementation and schema remain unchanged as the compatibility
  adapter. Its migration onto `CompensatePlan` remains in the named wrapper stages; S13 does not
  alter the frozen modern action union.

## Crash and adversarial coverage

`internal/workspace/recovery_test.go` runs the workspace service core in a separate test
process, waits until the named durable boundary is reached, then sends `SIGKILL`. Fourteen
commit cases cover both sides of the prepared/applying/final journal writes, both sides of each
of two canonical replacements, and both sides of both progress-journal writes. Six additional
cases kill recovery itself before/after canonical restoration, progress journaling, and final
rollback journaling. Every non-final interruption resumes to exact preimages; a kill after the
durable committed journal retains all postimages.

Focused tests also cover:

- startup recovery from every incomplete journal state;
- a third-party post-write present before recovery;
- a third-party post-write injected between recovery inspection and replacement;
- exact compensating restoration of an executable mode, a deleted mode-0600 file, a created
  file, and a moved symlink;
- compensation receipt identity and idempotent replay;
- the preceding S12 ordinary failure case now automatically rolling back on workspace restart.

## Guarantee and residual risks

The cooperative guarantee remains unchanged: with cooperative Huyang writers and no external
mutation during the bounded commit window, every declared postimage is applied or recovery
restores every preimage. Multi-file replacement is not filesystem-atomic.

An arbitrary external writer can always race the final pathname check and rename on an ordinary
filesystem; Huyang cannot provide compare-and-swap pathname replacement without a snapshotting
filesystem. Recovery therefore performs the narrowest practical recheck and never overwrites a
third-party image it observes. An unrecognized or corrupt journal, an unsupported object kind, or
a conflicting canonical image fails closed and requires explicit recovery instead of guessing.

Completed journals are retained for later garbage collection. Sandbox isolation, repository
formatting/check execution, diagnostic evidence, and legacy wrapper migration remain their later
named stages.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/workspace -run 'TestStartupRecovery|TestCompensating|TestCrashRecovery|TestJournaledCommit' -count=1`
  — exit 0.
- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0.
- `make smoke` — exit 0; unit, headless, multi-workspace, debug, and final smoke markers
  reported OK.
- `go test ./...`, `go vet ./...`, and `git diff --check` are recorded in the final S13
  handoff after the tracked documentation update.
