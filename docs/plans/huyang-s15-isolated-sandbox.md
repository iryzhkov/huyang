# Huyang S15 isolated sandbox prepare

Captured: 2026-09-10
Stage: S15 — Isolated sandbox prepare
Starting commit: 2d4fe9b5bf4e95aac3d6e34bf1371a9198798c3c

## Delivered scope

- Production materialization inventories the complete canonical tree, including dirty, untracked, ignored, generated, and in-tree Git entries. It uses lstat, hashes regular bytes, copies symlinks as links, preserves modes, rejects special files, and validates source identity while reading and after materialization.
- Linux tries clone-only per-file reflinks when source and destination share a filesystem. Unsupported clone operations discard the whole candidate before a fresh userspace byte-copy fallback. Neither backend creates hardlinks.
- S14 defaults are enforced before provider start: 200,000 entries, 16 GiB logical bytes, 4 GiB safe-copy bytes, 60 seconds, and 1 GiB free-space headroom. Cancellation is observed during inventory and copy.
- Each candidate is below a mode-0700 service directory and has a mode-0600 versioned ownership marker. Cleanup validates confinement, directory identity, workspace, and plan ownership before removal. Startup reaps only unreferenced directories with valid markers and ignores foreign entries.
- A plan receives its own embedded provider rooted at the sealed sandbox. The complete provider batch is staged only after the destination manifest equals the stable canonical base. Exact postimages are materialized in sandbox files and verified before READY. Preparation metadata records backend, base revision, verified disk state, and canonical evidence-path mapping; sandbox paths are not exposed.
- Discard and successful journaled apply close the sandbox provider before confined cleanup. Canonical bytes remain unchanged during prepare; the existing journal applies only after its full commit-time canonical precondition check.
- The scheduler permits two same-workspace sandbox preparations and four service-wide, further bounded by provider quota. Each sandbox provider serializes its own transaction, while pure canonical reads remain independent. Non-prepare plan actions, especially apply, retain the canonical workspace lane.

## Verification coverage

- Sandbox tests cover exact Git metadata, dirty bytes, modes, symlinks, inode separation, mutation isolation, prepared postimages, cancellation, special-file refusal, ownership-confined cleanup, referenced retention, unreferenced startup reap, and preservation of foreign directories.
- Workspace race tests prove two disjoint plans enter provider staging concurrently while a canonical read continues to observe its original bytes.
- Scheduler tests prove two same-workspace sandbox slots are available, a third queues and honors cancellation, and canonical pure reads do not queue behind preparation.
- Official SDK/provider tests prove the provider is rooted at the sandbox, prepared disk bytes equal the declared operation result, canonical bytes remain stable, and discard/apply close and remove the owned sandbox.
- Existing stale whole-write-set commit coverage proves competing prepared content is revalidated at apply and changes nothing on conflict.

## Decisions and residual risks

- S15 keeps exact-by-default snapshots; trusted configured cache exclusions remain policy work with .huyang.toml in S16 and are not guessed from ignore files.
- Safe copy counts logical bytes as its conservative materialization reservation. This may reject a large sparse tree that a more allocation-aware copier could accept, but it cannot exceed the configured materialized-byte limit silently.
- Evidence paths are canonical with sandbox provenance metadata. Detailed durable evidence storage and diagnostic producer provenance remain S17.
- Repository formatter, check, test, generator, and undeclared-write policies remain S16. S15 only provides the exact isolated filesystem and provider they require.
- No installation, deployment, installed-plugin update, live MCP restart, live configuration or state mutation, push, or pull request occurred.
