# Huyang implementation handoff

## Authority and invariant

- Repository: `https://github.com/iryzhkov/agent99`
- Standalone checkout: `/home/igor/Work/huyang`
- Branch: `feature/huyang`
- Reviewed base: `1c5302efe9ca51e1701e73df72d903f225fefe04`
- Base ancestry check: passed with
  `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`.
- Deployment boundary: no install, deployment, installed-plugin update, live MCP restart,
  live configuration/state mutation, push, or pull request is authorized.
- Dependency protocol: clean branch, committed checklist in the implementation plan, and this
  handoff. Exactly one ungated successor is queued after a successful stage.

## Current checkpoint

- Completed stage: S05 — Explicit workspaces and revisions.
- Starting commit: `483ab0a30603810c8ab7e7f0117cfa3d8f1fdd4f`.
- Completion commit: the `feature/huyang` commit containing this handoff.
- Predecessor reconciliation:
  - S04 is committed at `483ab0a30603810c8ab7e7f0117cfa3d8f1fdd4f`;
  - `feature/huyang` was clean after `git fetch origin --prune`;
  - the reviewed base remains an ancestor;
  - the committed checklist, S00–S04 records, frozen contract fixtures, provider code, and
    recorded S04 gates agree that S05 was the first incomplete stage.
- S05 exit gates achieved:
  - the workspace core owns random 128-bit IDs, project kind, provider epoch, and monotonic
    state sequence;
  - layered document snapshots expose exact filesystem kind/identity, provider
    changedtick/LSP versions/dirty state, and lazy SHA-256 revision tokens;
  - forced mutation validation requires workspace ID and revision and catches same-metadata
    rewrites, atomic saves, deletes/recreates, symlink retargets, and provider restarts;
  - legacy root inference remains in the bridge adapter and core mutation entry points require
    explicit identity/revision;
  - focused race tests, both provider smoke matrices, `go test ./...`, `go vet ./...`, and
    `git diff --check` pass.
- Checklist: only S05 was marked complete in this stage.
- Exact next stage: S06 — Provider-independent text core and document workspaces.

## Predecessor artifacts

S05 reconciled and consumed these committed predecessor artifacts completely:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/huyang-s01-package-boundary.md`
- `docs/plans/huyang-s02-provider-seam.md`
- `docs/plans/huyang-s03-embedded-provider-spike.md`
- `docs/plans/huyang-s04-embedded-provider.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

## S05 changes

- Added `internal/workspace` as the transport-independent identity/revision core.
- Added random 128-bit workspace IDs, closed workspace kinds, provider epoch synchronization,
  and monotonic state sequences.
- Added layered document snapshots covering exact filesystem kind/identity, canonical URI,
  dirty provider content, changedtick, per-provider LSP versions, and SHA-256 revisions.
- Ordinary snapshots cache hashes by metadata; refresh and mutation validation force a fresh
  exact-byte hash.
- Added typed mutation conflicts for missing/mismatched workspace identity, missing revision,
  provider epoch change, document deletion, and other content changes.
- Mutation paths are lexically confined and inspect objects with `lstat`; symlinks are
  revisioned as links rather than followed.
- Wired the headless bridge to retain core identity across a provider generation restart,
  propagate ID/epoch into provider request contexts, and return current identity fields from
  `open_workspace`.
- Added adversarial filesystem/race tests and socket/embed smoke assertions.
- Added `docs/plans/huyang-s05-workspaces-revisions.md` and marked only S05 complete.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0.
- `go test ./...` — exit 0; bridge, provider, embed, embedspike, socket, and workspace
  packages passed; the command package has no tests.
- `make smoke` — exit 0 on the default socket backend; reported `headless: OK`,
  `multi-workspace: OK`, `debug: OK`, and `smoke: OK`.
- `AGENT99_PROVIDER_BACKEND=embed make smoke` — exit 0 with the same four OK markers.
- `bash tests/smoke.sh headless:workspace` — exit 0 after the identity assertion.
- `AGENT99_PROVIDER_BACKEND=embed bash tests/smoke.sh headless:workspace` — exit 0 after the
  identity assertion.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0; no output.
- `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`
  — exit 0.

## Decisions and risks

- S05 creates no modern MCP catalog. It establishes the core that the S07 adapter will expose;
  the existing root/sticky routing remains explicitly legacy.
- Content hashes are lazy and metadata-cached for ordinary snapshots. Mutation validation
  always forces a hash, matching the contract's optimistic-concurrency boundary and catching
  same-metadata replacement.
- A document revision includes filesystem and provider layers, so inode/mode/mtime,
  changedtick, dirty state, or selected LSP versions may conservatively invalidate a token even
  when bytes are equal.
- State sequence advances only when the core observes a change or provider epoch transition;
  no filesystem watcher is claimed in this stage.
- Provider-buffer bytes are accepted as an explicit layer rather than fetched implicitly.
  Provider-independent collection and document workspaces belong to S06.
- The revision core currently stores service-local revision evidence in memory. Durable
  transaction and recovery records remain S10–S13 work.
- No install, deployment, live MCP/config/state mutation, push, or pull request occurred.
- Exact next stage: S06 — Provider-independent text core and document workspaces.
