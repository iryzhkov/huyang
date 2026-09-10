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

- Completed stage: S14 — Sandbox backend decision spike.
- Starting commit: acbd3cd159d6f453ebb2c2ba08342ce93e8e9041.
- Reconciliation fetched origin, confirmed a clean `feature/huyang` branch, confirmed reviewed
  base `1c5302efe9ca51e1701e73df72d903f225fefe04` in branch history, read the complete plan,
  frozen tool contract, handoff, and every predecessor/stage artifact named below, and verified
  S14 was exactly the first incomplete checklist stage.
- The S13 focused crash-recovery race gate passed before S14 spike editing.
- S14 exit gates are satisfied: reflink, kernel overlay, fuse-overlay, and safe-copy choices
  were measured on the available intended host and Btrfs workspace filesystem; exact
  repository-sized and adversarial fixture copies cover dirty, untracked, ignored, permissions,
  symlinks, sparse files, isolation, special-file refusal, and cleanup; deterministic selection,
  fallback, exclusions, quotas, and cleanup are frozen; reflink is the primary and userspace
  byte copy is the safe fallback; and no production sandbox backend was added.
- Only S14 is newly marked complete in the committed checklist.
- Exact next stage: S15 — Isolated sandbox prepare.

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

## S14 changes

- Added a decision record selecting capability-probed per-file reflinks as the primary sandbox
  materializer and a non-hardlink userspace byte copy as the portable safe fallback.
- Added a machine-readable host, filesystem, measurement, selection, quota, exclusion, and
  cleanup fixture.
- Added a test-only sandbox spike package. It preserves and verifies dirty, untracked, ignored,
  mode, symlink, sparse, binary, and in-tree Git entries; proves source/sandbox inode separation
  and write isolation; rejects special files; and reports host backend capabilities.
- Measured the exact 49,361,676-byte checkout on compressed Btrfs: clone-only copy completed in
  0.027 s and forced byte copy in 0.083 s in bounded single-run observations.
- Probed overlay choices: direct kernel overlay mount required privilege, user-namespace overlay
  mounted successfully, and fuse-overlayfs was absent. Overlay was rejected because an exact
  immutable lower tree still requires the chosen reflink/copy snapshot, after which namespace,
  whiteout, ownership, and cleanup machinery add cost without satisfying another S15 invariant.
- Defined conservative S15 starting quotas, exact-by-default inclusion of ignored/untracked
  state, fail-closed special/path policy, whole-candidate fallback, source-change conflict,
  destination-manifest validation, ownership markers, and confined cleanup.
- Added no production sandbox backend, provider, transaction behavior, deployment, installation,
  live configuration/state change, push, or pull request.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- `go test -race ./internal/workspace -run
  'TestStartupRecovery|TestCompensating|TestCrashRecovery|TestJournaledCommit' -count=1`
  — exit 0 before spike editing; the S13 predecessor gate remained green.
- `HUYANG_SANDBOX_SPIKE_DIR=/home/igor/Work go test -race -v
  ./internal/sandboxspike -count=1` — exit 0: both reflink and safe-copy fixture paths passed
  on the intended Btrfs workspace filesystem; special-file refusal and capability reporting
  passed; fuse-overlayfs was reported absent.
- `go test -v ./internal/sandboxspike -count=1` — exit 0: clone-only was unsupported on the
  system temporary filesystem and skipped as unavailable, while the safe-copy fallback,
  manifest equality, mutation isolation, special-file refusal, and capability test passed.
- Whole-checkout measurement commands using
  `cp -a --reflink=always --sparse=auto SOURCE/. DEST` and
  `cp -a --reflink=never --sparse=always SOURCE/. DEST` — both exit 0; elapsed times were
  0.027 s and 0.083 s respectively for 49,361,676 logical bytes.
- Kernel-overlay probe — direct `mount -t overlay` exit 32 (`must be superuser`);
  `unshare -Urnm ... mount -t overlay` exit 0; `kernel.unprivileged_userns_clone=1`.
- `jq empty docs/plans/fixtures/huyang-s14-sandbox-backends.json` — exit 0.
- `make smoke` — exit 0: built both binaries and ended unit_edit, unit_testrun, unit_check,
  unit_index, headless, multi-workspace, debug, and smoke with OK.
- `go test ./...` — exit 0; every Go package, including the test-only sandbox spike, passed.
- `go vet ./...` — exit 0 with no output.
- `git diff --check` — exit 0 after the final artifact, fixture, checklist, and handoff
  updates.

## Decisions and risks

- Reflink is the primary because clone-only succeeds on the intended Btrfs source/destination
  pair, creates distinct inodes, retains exact fixture state, and makes repository-sized
  snapshots cheaply. Backend selection probes every filesystem pair and does not trust a
  filesystem name.
- Userspace byte copy is the mandatory fallback. It preserves the same manifest without source
  aliases and works on the temporary filesystem where reflink is unsupported.
- GNU `cp` is only the spike oracle. S15 owns a Go implementation with clone-only semantics
  and an internal byte-copy walker, cancellation, stable source inventory, and destination
  verification.
- A clone failure discards the entire candidate before safe-copy retry. Source changes,
  permissions, special files, quotas, and cancellation fail closed rather than being disguised
  as backend fallback.
- Exact snapshots include dirty, untracked, ignored, generated, and in-tree Git entries.
  Explicit trusted exclusions are allowed only with provisional coverage and can never remove
  a declared mutation target.
- Kernel overlay inside an unprivileged user namespace is technically available on this host,
  but it cannot use the mutable canonical tree as a truthful lower layer. Pre-snapshotting that
  lower layer removes its materialization advantage while retaining namespace/mount/whiteout
  lifecycle risk, so it is not selected. Fuse-overlayfs is absent and is not installed.
- The default quotas are conservative starting points, not measured maxima. S15 must expose
  queueing/resource failures and validate stable source and destination manifests before a
  sandbox provider starts.
- Sparse logical size, physical reservation, COW-growth headroom, cleanup ownership markers,
  and special-file refusal are explicit because copying bytes alone does not bound disk or
  deletion risk.
- Measurements cover the available intended development host and one exact repository-sized
  tree. They are single-run observations, not performance promises; the capability probe plus
  safe fallback is the portability mechanism.
- The temporary whole-checkout probe directory was moved to the desktop Trash after
  measurement; the smaller test directories were removed by their test cleanup.
- Existing unrelated Python language-server diagnostics remain pre-existing; the focused race,
  smoke, Go test, and Go vet gates pass.
- No production sandbox implementation, install, deployment, installed-plugin update, live MCP
  restart, live configuration/state mutation, push, or pull request occurred.
- Exact next stage: S15 — Isolated sandbox prepare.
