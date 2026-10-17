# M1 language probe: job-local prerequisites (2026-10-17)

`tests/langprobe-chaldea.sh` provisions everything the nine-language probe
(`TestLangprobeABCD` in `cmd/huyang/langprobe_test.go`) needs inside a Chaldea job
workspace, then runs it. Nothing is installed on a host and the installed Huyang
service is never contacted: the probe starts its own private service, socket and
state directory.

## Why a bootstrap is needed

Agent-a jobs provide Go 1.27.0, Rust 1.99.0 (no `rust-src` component), Node
26.12.0, Python 3.13.5, git, curl, tar, xz and gzip, but no `unzip`, no Java and
none of the language servers. Their system Neovim is 0.10.4 (API level 12), which
the provider refuses. Jobs carry no Git metadata and no caches.

The kernel finds language servers through `vim.lsp.config` entries on the
runtimepath and enables them through `vim.lsp.enable`. On gaming-pc, Mason and
mason-lspconfig did that from the user configuration. In a job, the script
fetches nvim-lspconfig and writes a small `nvim-init.lua` that enables the seven
configurations (`gopls`, `rust_analyzer`, `pyright`, `ts_ls`, `jdtls`, `clangd`,
`bashls`). The provider then loads that init through `HUYANG_HEADLESS_INIT` (Neovim
runs with `--clean -u`). The system-server fallback in `install.lua` alone is not
enough: it skips configurations whose `cmd` is a function, which includes `ts_ls`
and `jdtls`.

## Pins

The pins match the Mason packages behind the 2026-10-16 probe on gaming-pc. clangd
is the upstream 22.x build closest to that host's system 22.1.8.

| Component | Pin | Integrity |
|---|---|---|
| Neovim | 0.12.5, `nvim-linux-x86_64.tar.gz` | sha256 `bce0f56e…6875` |
| nvim-lspconfig | commit `19576de1c1b6d1b5e231ea6ef9cba9a811c322bb` | Git object ID; archive fallback sha256 `e18b21ee…a8fb` |
| gopls | v0.23.0 | `go install`, go.sum and sum.golang.org |
| rust-analyzer | 2026-09-07 (0.3.3041) | sha256 `a3500183…fb12` |
| rust-src | 1.99.0, through `RUST_SRC_PATH` | sha256 `3f1f9b7e…77aa` |
| clangd | 22.1.6 | sha256 `a9c77443…7cfa` |
| ShellCheck | v0.11.0 (bash-language-server needs it for SC2154) | sha256 `8c3be12b…7198` |
| JDK | Temurin `jdk-21.0.12.1+1` | sha256 `ce79869e…aee94` |
| jdtls | `jdt-language-server-1.60.0-202606262232.tar.gz` | sha256 `e94c303d…1d` |
| npm | pyright 1.1.413, typescript-language-server 6.0.0, typescript 6.0.3, bash-language-server 5.6.0 | `--before=2026-10-16T00:00:00Z`, registry integrity, `--ignore-scripts` |

The script holds the full digests. Each download is checked against its pinned
digest. Where an official mirror exists, the script tries that mirror before
failing: the jdtls milestones directory, and Adoptium's binary API for the JDK.
If the job's rustc is not 1.99.0, the script uses the digest that
static.rust-lang.org publishes for the actual version and records in the manifest
that this digest was not pinned.

## Environment

`HOME` is never changed. Writes go to the prefix (`tests/.deps/langprobe`, which
`.gitignore` already excludes):

- the XDG directories, used by Neovim, gopls, clangd and tsserver;
- `GOPATH`, `GOMODCACHE` and `GOCACHE`;
- `CARGO_HOME` and the npm cache;
- `JDTLS_JVM_ARGS`, which sets `maven.repo.local` and the Java preferences root.

Other settings:

- `umask 022` and `HUYANG_FRICTION=0` are set at the top of the script.
- `TMPDIR` is `~/.cache/hf`, a real directory with no symbolic link, so Unix
  sockets under `t.TempDir()` stay inside the 108-byte limit. If `HOME` is not
  writable, `TMPDIR` falls back to the prefix's `tmp`. A path longer than 48 bytes
  is refused.
- The `HUYANG_LANGPROBE*`, `HUYANG_SOCKET` and `HUYANG_STATE_DIR` variables are
  cleared, except where a mode sets them on purpose.

## Modes

```sh
tests/langprobe-chaldea.sh submit check            # setup, versions, Neovim LSP smoke
LANGPROBE_LABEL='M1 acceptance, <sha>' tests/langprobe-chaldea.sh submit probe
tests/langprobe-chaldea.sh submit suite            # go test -count=1 ./...
tests/langprobe-chaldea.sh submit race ./internal/...
tests/langprobe-chaldea.sh submit run go vet ./...
```

`submit` calls `chaldea exec --host ${LANGPROBE_HOST:-agent-a}`. It retries only a
refusal that reports nothing was dispatched, waiting 60 s between attempts. Any
other outcome is reported and never re-run, including an uncertain one.

`probe` runs the test with `HUYANG_LANGPROBE=1` and `HUYANG_LANGPROBE_STRICT=1`. It
succeeds only if each of the nine subtests reports `--- PASS`. A `SKIP`, `FAIL` or
missing subtest fails the run, and so does a top-level skip.
`LANGPROBE_LANGUAGES` selects a subset, and the summary then marks the run as
partial: a subset run never counts as acceptance.

Every mode leaves two artifacts in `tests/.deps/langprobe/out/`:

- `summary.txt`;
- `langprobe-report.tar.gz`, which holds the manifest, the tool versions, the npm
  lock, the smoke JSON, the go test log and the probe's per-language replies and
  results. `.git` and `target` directories are left out.

## Validation (job `1bb576a0-9e26-4f16-ab51-98385a4035bc`, agent-a)

This was a prerequisite check only. It ran at base `6188357` and an earlier
revision of the script, which still pointed `HOME` at the prefix. It is not M1
acceptance. The journal reads `finished`, exit 0, and both artifacts came back.

The downloads came to 389 MB and the unpacked prefix to 1.5 GB. Every executable
reported its pinned version. The headless Neovim smoke opened one broken file per
language and saw each server attach and publish the expected error on the expected
line, with no other warning or error:

| Language | Server | Attach | First diagnostic | Line and message |
|---|---|---|---|---|
| Go | gopls v0.23.0 | 0.1 s | 0.2 s | L2 cannot use "bad" … as int value |
| Rust | rust-analyzer 0.3.3041 | 0.0 s | 2.3 s | L1 mismatched types |
| Python | pyright 1.1.413 | 0.2 s | 0.6 s | L2 `Literal['bad']` is not assignable |
| TypeScript | ts_ls 6.0.0 | 0.1 s | 0.8 s | L1 string not assignable to number |
| JavaScript | ts_ls 6.0.0 | 0.1 s | 0.7 s | L3 string not assignable to number |
| Java | jdtls 1.60.0 on JDK 21.0.12.1 | 2.5 s | 6.8 s | L2 Type mismatch |
| C++ | clangd 22.1.6 | 0.0 s | 0.1 s | L1 undeclared identifier |
| C | clangd 22.1.6 | 0.0 s | 0.0 s | L1 undeclared identifier |
| Bash | bashls 5.6.0 with ShellCheck 0.11.0 | 0.2 s | 0.7 s | L3 SC2154 |

The change that keeps `HOME` unchanged and routes writes through the XDG
directories and `JDTLS_JVM_ARGS` came after this job and has not been run in a job
yet. The first integrated `check` or `probe` run is what confirms it. For Java,
that run should show where m2e put its local repository. If m2e ignores
`maven.repo.local`, it writes the user's `~/.m2`, but in a job that directory is
the disposable job home.

## Reuse

The tool bundle cannot travel as a Chaldea artifact. Exec v0 caps artifacts at
64 MiB per job, and the downloads alone are 389 MB. A job's upload snapshot is
capped at 64 MiB, and ignored files are never uploaded. Each job therefore
provisions itself, which takes a few minutes. Within one job, `setup` is
idempotent through stamp files, and a cached download is reused only if its digest
still matches. A local run reuses `tests/.deps/langprobe` the same way.
