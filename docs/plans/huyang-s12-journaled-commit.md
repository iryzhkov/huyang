# Huyang S12 journaled commit

Captured: 2026-09-10
Stage: S12 — Journaled commit
Starting commit: d1417ac5ca33f940a47f690e1234659d5c19ec6f

## Delivered scope

- `change_plan(action="apply")` now requires the exact plan revision and prepared revision,
  accepts only a READY provider-backed plan, and revalidates the complete canonical write set
  before the first filesystem mutation.
- The workspace core writes a versioned mode-0600 journal containing exact preimage and
  postimage bytes, object kinds, modes, symlink targets, plan/workspace identity, and per-path
  progress. The journal file and its containing directory are synced before canonical writes.
- Regular and binary postimages use same-directory temporary files, exact permission
  preservation, file sync, rename, and directory sync. Symlink postimages use a same-directory
  temporary symlink and rename. Deletes are followed by directory sync.
- Write order materializes creates/replacements/move destinations before deletions and move
  sources. Each completed path is durably recorded before the next path begins.
- Successful apply clears the provider transaction view through an explicit commit/resync
  operation, advances the workspace state sequence once, records the canonical revision, marks
  the plan COMMITTED, releases the lease, and retains the completed journal for later garbage
  collection.
- Ordinary write, persistence, provider-resync, and final-transition failures durably mark both
  the journal and plan RECOVERY_REQUIRED. S13 owns automatic startup recovery, exhaustive
  crash-step failpoints, third-party post-write preservation, and compensating undo.

## Guarantee and boundary

S12 preserves the reviewed cooperative guarantee: with cooperative Huyang writers and no
external mutation during the bounded commit window, every declared postimage is applied or the
journal retains the exact information needed to restore every preimage during recovery. Multi-file
replacement is not described as filesystem-atomic. An external change discovered before the
window refuses the entire write set without mutation; an interruption during the window remains
explicitly RECOVERY_REQUIRED for S13.

Provider staging remains exclusive and canonical-buffer-backed. Disk-dependent formatter,
check, and test stages remain unavailable until S16. S12 does not add sandbox isolation,
startup recovery, compensating undo, deployment, installation, live service/configuration
changes, push, or a pull request.

## Verification coverage

Focused workspace and official-SDK/provider tests cover:

- regular create, replace, move, and delete in one journaled commit;
- executable and moved-file permission preservation;
- symlink move without following or rewriting its target;
- exact preimage/postimage and per-path progress persistence;
- injected failure after a canonical rename with a durable RECOVERY_REQUIRED record;
- persistence of recovery-required plan state across workspace reconstruction;
- whole-write-set stale refusal before any canonical mutation;
- real embedded-provider apply, canonical disk update, transaction release, and provider
  buffer resynchronization.

The exact commands and results are recorded in `huyang-handoff.md`.
