# Huyang S14 sandbox backend decision spike

Captured: 2026-09-10
Stage: S14 — Sandbox backend decision spike
Starting commit: acbd3cd159d6f453ebb2c2ba08342ce93e8e9041

## Decision

Use per-file copy-on-write reflinks as Huyang's primary sandbox materialization backend and
an ordinary userspace byte copy as its mandatory safe fallback. Selection is capability-based
for every source/destination filesystem pair, not cached from a hostname or filesystem label.
Neither path may create a hardlink between the canonical tree and a sandbox.

S15 should implement clone-only and byte-copy paths inside the Go service. The GNU `cp`
commands in this spike are measurement oracles, not a production command dependency. A
reflink candidate is all-or-nothing: the service inventories first, tries clone-only semantics
for every regular file, removes the complete candidate after any failure, and retries the safe
copy in a fresh directory. It never silently mixes aliases, clones, and copies in one candidate.

Kernel overlayfs and fuse-overlayfs are not selected. Overlayfs is useful only after its lower
tree is immutable. Pointing the lower layer at the canonical workspace would expose concurrent
canonical changes and violate the exact-base invariant. Taking a reflink/copy snapshot first
already supplies the required isolation, while overlay adds a namespace keeper, mount cleanup,
whiteout, ownership, and provider-lifecycle boundary. Direct mounting also requires privilege
on the measured host. Unprivileged user-namespace mounting worked, but does not remove those
semantic and lifecycle costs. `fuse-overlayfs` is not installed and would add another supervised
process; ordinary coding calls must not install it.

## Host and repository measurements

The available intended development host was `omarchy-normandy`, Linux
`7.1.9-arch1-2`, with the checkout on a compressed Btrfs `/home` subvolume. The host had
49,688,027,136 bytes available during the probe. The exact checkout, including `.git`,
tracked modifications made for this stage, untracked files, and ignored build output, measured
49,361,676 logical bytes and 50,315,264 allocated bytes as reported by `du`.

One fresh whole-checkout sample produced:

| Backend oracle | Command shape | Elapsed | Result |
| --- | --- | ---: | --- |
| Reflink | `cp -a --reflink=always --sparse=auto SOURCE/. DEST` | 0.027 s | pass |
| Safe copy | `cp -a --reflink=never --sparse=always SOURCE/. DEST` | 0.083 s | pass |

Clone-only success is the capability proof; `du` block counts do not distinguish shared Btrfs
extents. A deterministic adversarial fixture containing dirty, untracked, ignored, executable,
mode-0600, symlink, sparse, and in-tree Git entries passed both strategies on the Btrfs
workspace filesystem. Its one-run times were 2.632 ms for reflink and 1.751 ms for byte copy;
the fixture is intentionally too small for latency ranking. Both destinations had distinct
inodes from their sources and sandbox writes did not change canonical bytes.

The same fixture defaults to the system temporary filesystem in ordinary tests, where
clone-only returned `EOPNOTSUPP` and safe copy passed. This is the required fallback case, not
a skipped correctness path. The focused host gate sets `HUYANG_SANDBOX_SPIKE_DIR=/home/igor/Work`
so both primary and fallback are exercised on the intended workspace filesystem.

Kernel overlay was present. A direct mount returned exit 32 because the unprivileged service
user is not permitted to mount it. A mount inside `unshare -Urnm` returned exit 0 with
`kernel.unprivileged_userns_clone=1`. `fuse-overlayfs` was absent. These results are host
observations, not portable backend assumptions.

## Exact snapshot and exclusion policy

The default inventory includes every entry under the canonical root: committed, dirty,
untracked, ignored, generated, and in-tree `.git` data. Ignored is not synonymous with
irrelevant; excluding ignored build inputs would verify different bytes. Symlinks are copied
as link objects and are never followed. Regular content, binary content, permission bits,
sparse logical size, and symlink targets participate in the verified destination manifest.

Huyang service state outside the root and the separately allocated sandbox destination are
never inventory inputs. Unix sockets, FIFOs, devices, a destination nested in the source, and
any path escaping lexical/real-path confinement are rejected. A `.git` file or symlink that
refers outside the tree remains an inert copied object; S15 must report Git-in-sandbox
unavailable rather than follow it.

A trusted explicit project policy may exclude named ignored/cache paths only after inventory.
That changes the snapshot claim to provisional, lists every excluded path and byte count, and
cannot exclude a declared mutation target. Exclusion is never an automatic response to a quota
failure.

## Quotas and deterministic fallback

S15 starts with conservative configurable defaults:

- 200,000 filesystem entries;
- 16 GiB total logical regular-file bytes;
- 4 GiB bytes materialized by the safe-copy fallback;
- 60 seconds for inventory plus materialization;
- at least 1 GiB free-space headroom after reservation;
- two live sandboxes per workspace and four service-wide, further bounded by the existing
  provider quota.

Inventory, quota reservation, and backend choice complete before a provider starts. A changed
source identity/hash during materialization conflicts and discards the candidate; it does not
fall back, because the base itself is stale. Reflink `unsupported`, cross-device, or
per-file clone refusal discards the entire candidate and selects safe copy. Quota, permission,
special-file, cancellation, or source-change failures do not select a different backend.

Sparse logical bytes count toward the logical quota. Safe-copy reservation uses allocated
source bytes with a conservative filesystem-block allowance, bounded by the 4 GiB default;
actual written bytes are monitored. Reflinks still reserve the 1 GiB COW headroom because
later provider and tool writes allocate private extents.

## Cleanup and S15 contract

Every sandbox lives beneath a service-owned mode-0700 directory with a versioned ownership
marker naming workspace, plan revision, base revision, backend, and lifecycle state. Cleanup
closes the sandbox provider first, validates that both marker and resolved path belong to the
service sandbox root, and removes without following symlinks. Cancellation and failed
materialization remove the incomplete candidate before returning or trying the fallback.

Startup reaps only unreferenced incomplete directories with valid ownership markers.
A cleanup failure retains a bounded recovery record and returns `cleanup_required`; it never
reuses the directory. Completed evidence may retain hashes and manifests, not source contents,
after the sandbox itself is removed.

S15 must additionally prove a stable base with pre/post identity and hash validation during
the walk, verify the complete destination manifest before provider start, and run provider
preparation only after materialization is sealed. This spike does not add a production
sandbox, start a sandbox provider, or change transaction behavior.

## Reproduction and scope

The spike source (formerly the test-only package `internal/sandboxspike`) was removed after
this decision record was committed. It ran as a portable fallback gate:

```sh
go test -v ./internal/sandboxspike -count=1
```

Run both chosen backends on the workspace filesystem:

```sh
HUYANG_SANDBOX_SPIKE_DIR=/home/igor/Work \
  go test -v ./internal/sandboxspike -count=1
```

The machine-readable observations and policy fixture are in
`docs/plans/fixtures/huyang-s14-sandbox-backends.json`. Measurements are bounded single-host,
single-run observations and are not performance promises. Backend selection remains portable
because it probes the actual filesystem pair and always has a non-hardlink byte-copy fallback.
No installation, deployment, live-service/configuration change, push, or pull request occurred.
