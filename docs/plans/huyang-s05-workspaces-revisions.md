# Huyang S05 explicit workspaces and revisions

Captured: 2026-09-10
Stage: S05 — Explicit workspaces and revisions
Starting commit: `483ab0a30603810c8ab7e7f0117cfa3d8f1fdd4f`

## Workspace identity

The new `internal/workspace` core owns workspace identity independently of provider and
transport details:

- IDs are `ws_` plus 128 random bits and are never derived from roots, processes, or
  transport connections.
- Kind is the closed `project|documents|transaction_sandbox` set.
- The canonical root is informational and used for lexical confinement, not identity.
- Provider epoch is synchronized from the provider descriptor. Every epoch transition
  advances the workspace state sequence.
- State sequence begins at one and advances when a forced observation finds a changed
  filesystem/provider layer.

The legacy bridge keeps its path/sticky-root routing as a compatibility adapter. A headless
workspace now owns one core workspace for its lifetime, includes ID/kind/epoch/state sequence
in `open_workspace`, and puts workspace ID and epoch into every provider request context.
The bridge refreshes descriptors after provider calls so a restart cannot return a stale PID,
endpoint, provider epoch, or workspace epoch.

No modern MCP catalog is introduced in S05. The core mutation-validation entry point requires
the explicit workspace ID and document revision; the later modern adapter cannot route it from
a root string.

## Layered document revisions

A document snapshot records:

- workspace ID, kind, provider epoch, and observation state sequence;
- canonical file URI;
- object kind (`regular_text`, `binary`, `symlink`, `directory`, or `missing`);
- device, inode, exact size, nanosecond mtime, mode, and symlink target;
- provider changedtick, dirty state, and per-provider LSP versions;
- SHA-256 of exact disk bytes, exact dirty provider bytes, or the symlink target;
- an opaque `docrev_` SHA-256 revision token over the layered snapshot.

Hashes are created only when a document is requested and ordinary snapshots may reuse a
content hash while filesystem metadata is unchanged. `Refresh` and `ValidateMutation`
always read and hash the target again. Mutation validation therefore catches a same-size,
same-mtime rewrite that the cheap cache cannot see. Provider-owned content is copied before
hashing, and LSP-version maps are copied so caller mutation cannot alter a recorded snapshot.

Every filesystem inspection uses `lstat`: symlinks are revisioned as links rather than
followed for mutation, binary/text classification is explicit, paths are confined lexically
to the workspace, and regular-file reads are accepted only when the file identity and
metadata still match after the read.

## Conflict behavior

`ValidateMutation` rejects missing or wrong workspace IDs and missing revisions before any
mutation can run. A forced current snapshot then produces stable conflict codes:

- `workspace_epoch_changed` after provider replacement;
- `document_deleted` when a previously present target is missing;
- `document_content_changed` for same-metadata rewrites, atomic saves, recreates, symlink
  retargets, provider-buffer changes, and other stale layered revisions.

There is no automatic rebase, root inference, or silent revision retargeting in the core.

## Adversarial coverage

`internal/workspace/workspace_test.go` covers random explicit identity, layered dirty
provider snapshots, defensive copies, missing identity/revision refusal, same-size and
same-mtime rewrites, atomic-save inode replacement, delete/recreate, symlink retarget,
provider restart, binary classification, confinement, and provider changedtick/LSP-version
changes. The focused tests run under the race detector.

The headless smoke driver additionally requires every socket and embedded `open_workspace`
result to expose a correctly shaped workspace ID, project kind, matching provider/workspace
epoch, and nonzero state sequence.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0.
- `go test ./...` — exit 0.
- `make smoke` — exit 0; `headless: OK`, `multi-workspace: OK`, `debug: OK`, and
  `smoke: OK`.
- `AGENT99_PROVIDER_BACKEND=embed make smoke` — exit 0 with the same four OK markers.
- `bash tests/smoke.sh headless:workspace` — exit 0 after adding the explicit identity
  assertion.
- `AGENT99_PROVIDER_BACKEND=embed bash tests/smoke.sh headless:workspace` — exit 0 after
  adding the explicit identity assertion.
- `go vet ./...` — exit 0.
- `git diff --check` — exit 0.

No install, deployment, installed-plugin update, live MCP restart, live state/configuration
mutation, push, or pull request occurred.
