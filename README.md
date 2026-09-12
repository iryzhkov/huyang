# Huyang

Huyang is a local transactional semantic workspace for coding agents. It combines a
long-lived Go service with isolated Neovim providers so MCP clients can navigate, edit,
verify, debug, and recover changes against an explicit workspace revision.

The MCP surface is `huyang.workspace/v1alpha1`. It exposes 19 tools through the portable
`full` profile, with fixed `orient`, `edit`, and `debug` profiles for smaller tool
catalogs. There is no legacy or compatibility surface: the embedded Neovim provider is the
only backend and the tools below are the only tools.

## What Huyang provides

- Explicit project and document workspaces with revision-bound reads and writes.
- Semantic handles backed by Tree-sitter, LSP, and exact text anchors.
- Isolated multi-operation preparation with formatter, diagnostic, check, and test evidence.
- Journaled canonical apply, crash recovery, and compensating undo.
- Bounded impact analysis and a strict distinction between affected and full test coverage.
- A four-tool DAP facade with explicit evaluation side-effect policy.
- A native text path that still works when Git, Neovim, parsers, or language servers are absent.

The service owns durability, policy, scheduling, sandboxes, and recovery. A small embedded
Lua kernel under `lua/huyang` owns Neovim buffer, Tree-sitter, LSP, and DAP operations and
speaks kernel protocol version 2 to the service.

## Agent guide

[docs/agent-guide.md](docs/agent-guide.md) names the cheapest correct call for each common
operation (read a file, read a region by name, change a line you know, replace a block
located by content, rename a local identifier, add a file, coordinate edits and test) with
the measured token cost of each, from the benchmark under
[bench/agent-efficiency](bench/agent-efficiency/README.md). Read it before wiring Huyang into
an agent's instructions.

## Tools

The `full` profile lists all 19 tools in this order. Each smaller profile is a fixed subset.

Orientation, in every profile (`full`, `orient`, `edit`, `debug`):

- `workspace_open`, `workspace_inspect`, `search`, `symbol_find`, `navigate`, `read`,
  `diagnostics`, `evidence_get`.

Editing and verification, in `full` and `edit`:

- `code_actions`, `edit_apply`, `change_plan`, `verify_run`, `revision_diff`.

Language-server administration, in `full` only:

- `language_server_status`, `language_server_setup`.

Debugging, in `full` and `debug`:

- `debug_session`, `debug_breakpoints`, `debug_control`, `debug_inspect`.

That gives 19 tools in `full`, 8 in `orient`, 13 in `edit`, and 12 in `debug`. The service
refuses to start if the registered catalog differs from those counts, and every tool
declares the scheduler class it runs under. The contract is described in
[docs/plans/huyang-tools-v1alpha1.md](docs/plans/huyang-tools-v1alpha1.md).

## Requirements

- Go 1.27 or newer to build.
- Neovim with API level 13 or newer for the semantic and debugger providers. The service
  reads `nvim_get_api_info` at provider start and refuses an older Neovim with an
  `incompatible` failure. API level 13 is Neovim 0.11, which introduced the
  `vim.lsp.config` and `vim.lsp.enable` functions the kernel depends on; Neovim 0.12 also
  satisfies the check.
- The language servers, parsers, formatters, and debugger adapters required by your projects.
- [mfussenegger/nvim-dap](https://github.com/mfussenegger/nvim-dap) installed in the host
  Neovim for the four debugger tools (`debug_session`, `debug_breakpoints`,
  `debug_control`, `debug_inspect`). nvim-dap is GPL-3.0 and is not distributed with
  Huyang; the kernel discovers it at provider start, first hit wins:
  1. `HUYANG_NVIM_DAP_PATH` (alias `AGENT99_NVIM_DAP_PATH`), a checkout directory holding
     `lua/dap.lua`. When set it is authoritative: nothing else is searched.
  2. A `lua/dap.lua` already on the runtimepath, for example loaded by a
     `HUYANG_HEADLESS_INIT` file, or a `require("dap")` that already succeeds.
  3. `$XDG_DATA_HOME/nvim/lazy/nvim-dap` (lazy.nvim; `$XDG_DATA_HOME` defaults to
     `~/.local/share`).
  4. `$XDG_DATA_HOME/nvim/site/pack/*/start/nvim-dap` and
     `$XDG_DATA_HOME/nvim/site/pack/*/opt/nvim-dap` (packer, paq, `vim.pack`, manual packs).

  The outcome is recorded in the provider health detail shown by `workspace_inspect`
  (`nvim-dap runtime: <path> (<commit>)` or `nvim-dap runtime: absent (...)`). Without
  nvim-dap the provider still starts and every other tool works; the four debugger tools
  answer `unavailable` with a message that lists the searched locations and the override
  variable. The test harness resolves nvim-dap the same way from `HUYANG_NVIM_DAP_PATH`,
  then `tests/.deps/nvim-dap`, then the lazy.nvim clone; `tests/fetch-nvim-dap.sh` clones
  the pinned upstream commit into `tests/.deps/` (gitignored, network needed), and
  `.luarc.json` points lua-language-server at that test dependency for editor diagnostics
  only.

The native text core does not require Neovim or an LSP.

## Build and test

```sh
make build
make smoke
go test ./...
go vet ./...
```

The sole shipped executable is `bin/huyang`.

## Run the service

Start the long-lived service:

```sh
./bin/huyang serve
```

`huyang serve` accepts these flags:

| Flag | Default | Purpose |
|---|---|---|
| `--socket PATH` | `$HUYANG_SOCKET`, else `$XDG_RUNTIME_DIR/huyang/control.sock` | Private Unix control socket, mode 0600. When `XDG_RUNTIME_DIR` is unset the base is `$TMPDIR/huyang-<uid>`. |
| `--state-dir PATH` | `$HUYANG_STATE_DIR`, else `$XDG_STATE_HOME/huyang`, else `~/.local/state/huyang` | Durable service state directory, mode 0700. |
| `--http LOOPBACK:PORT` | unset | Optional Streamable HTTP listener. The host must be a numeric loopback address. Routes are `/mcp` (`full`), `/mcp/orient`, `/mcp/edit`, and `/mcp/debug`; every request needs `Authorization: Bearer <token>`. The transport is stateless. |
| `--pprof LOOPBACK:PORT` | unset | Optional `net/http/pprof` listener behind the same bearer token, for heap and goroutine profiles of the live service. |
| `--provider-quota N` | 4 | Maximum concurrent provider-backed jobs. |
| `--external-job-quota N` | 2 | Maximum concurrent external jobs (repository checks and tests). |

The bearer token for `--http` and `--pprof` is generated on first use and stored in
`<state-dir>/http-token` with mode 0600; the service prints the token file path at start.

Connect an MCP client through the stdio adapter:

```sh
./bin/huyang mcp --profile full
```

`huyang mcp` accepts `--profile full|orient|edit|debug` (default `full`) and
`--socket PATH` (default as for `serve`). It refuses any other profile name. The third
subcommand, `huyang trust`, administers the user trust policy (see "Trust and repository
commands"). The adapter is
intentionally thin: it forwards newline-delimited JSON-RPC to the service, and on a service
disconnect it reconnects for up to 30 seconds, replays the client's `initialize` exchange,
and resends outstanding requests. Workspace identity, receipts, providers, and recovery
state live in the service and survive adapter reconnects.

## Environment variables

Every variable the shipped code reads. "Fallback" names are read only when the `HUYANG_`
name is empty; they survive from the Agent99 lineage and are not documented anywhere else.

| Variable | Default | Purpose | `AGENT99_` fallback |
|---|---|---|---|
| `HUYANG_SOCKET` | `$XDG_RUNTIME_DIR/huyang/control.sock` | Default for `--socket` in `serve` and `mcp`. | no |
| `HUYANG_STATE_DIR` | `$XDG_STATE_HOME/huyang` | Default for `--state-dir`. | no |
| `HUYANG_PROVIDER_BACKEND` | `embed` | Provider backend name. Only `embed` (or empty) is accepted; anything else fails at provider start. | `AGENT99_PROVIDER_BACKEND` |
| `HUYANG_RUNTIME_PATH` | directory of the executable, its parent, or the working directory, whichever holds `lua/huyang/rpc.lua` | Runtime path handed to the embedded Neovim so it can load the kernel. | `AGENT99_RUNTIME_PATH` |
| `HUYANG_HEADLESS_INIT` | unset | Init file handed to every embedded Neovim the service starts, for language-server configuration. | `AGENT99_HEADLESS_INIT` |
| `HUYANG_FRICTION` | unset | `1` enables the friction spool, `0` disables it; otherwise the spool is enabled by a file named `enabled` in the spool directory. | `AGENT99_FRICTION`, then `TOOLFEEDBACK` |
| `HUYANG_FRICTION_DIR` | `$XDG_DATA_HOME/toolfeedback`, else `~/.local/share/toolfeedback` | Spool directory; Huyang writes under its own `huyang/` subdirectory. | `AGENT99_FRICTION_DIR`, then `TOOLFEEDBACK_DIR` |
| `HUYANG_COMMAND_CACHE` | on | `off`, `0` or `false` gives every verification command a throwaway compiler build cache instead of the shared one under the state directory. Full isolation, at the price of recompiling the package set in every stage. | no |
| `HUYANG_DIRECT_STATE_DIR` | a fresh temporary directory | State directory for the in-process direct mode; only the test suites call that path. The service uses `--state-dir`. | no |
| `HUYANG_TEST_FAULTS` | unset | `1` arms the kernel's fault-injection hooks in the embedded provider. Test-only. | no |
| `HUYANG_FORMAT` | off | Formatting after a provider-side symbol edit: `range`, `file`, or off. Read by the Lua kernel from the provider's environment. | `AGENT99_FORMAT` |
| `HUYANG_POST_EDIT_WAIT` | wait | `0`, `false`, or `off` defers the provider's post-edit diagnostic verdict. Read by the Lua kernel. | `AGENT99_POST_EDIT_WAIT` |
| `HUYANG_DEBUG_VERDICT` | unset | Any value makes the kernel report which signal ended a post-edit wait. Read by the Lua kernel. | `AGENT99_DEBUG_VERDICT` |
| `HUYANG_DEBUG_IDLE_MS` | 600000 | Idle timeout of a debugger session in milliseconds. Read by the Lua kernel. | `AGENT99_DEBUG_IDLE_MS` |
| `HUYANG_NVIM_DAP_PATH` | unset | nvim-dap checkout for the debugger tools; when set, the discovery in [Requirements](#requirements) searches nothing else. Read by the Lua kernel. | `AGENT99_NVIM_DAP_PATH` |
| `CLAUDE_CODE_SESSION_ID` | random per process | Session identifier recorded in friction events when the client is Claude Code. | no |
| `XDG_RUNTIME_DIR`, `XDG_STATE_HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME` | XDG defaults | Bases for the socket, state directory, friction spool, and `huyang/config.toml` trust policy. | no |

The kernel variables reach Neovim because the provider inherits the service's environment;
set them on the service, not on an MCP adapter. Repository commands run under an isolated
environment that forwards only `PATH`, `TMPDIR`, `LANG`, `LC_ALL`, and a fixed set of
toolchain cache variables (`MISE_*`, `RUSTUP_HOME`, `CARGO_HOME`, `GRADLE_USER_HOME`,
`GOMODCACHE`, `NUGET_PACKAGES`).

The embedded provider also takes a per-root lock file under
`$XDG_RUNTIME_DIR/agent99-huyang/`; the directory name is historical and only the lock lives
there.

## User service

`contrib/systemd/huyang.service` is a systemd user unit:

```ini
[Unit]
Description=Huyang semantic workspace service
After=default.target

[Service]
Type=simple
WorkingDirectory=%h/.local/share/huyang
ExecStart=%h/.local/share/huyang/bin/huyang serve
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
```

After installing the unit:

```sh
systemctl --user daemon-reload
systemctl --user enable --now huyang.service
```

Register the MCP command in each harness as
`~/.local/share/huyang/bin/huyang mcp -socket /run/user/1000/huyang/control.sock -profile full`.
Pinning the socket matters for harness subprocesses that do not inherit `XDG_RUNTIME_DIR`.

### Upgrading a deployed service

The install location is a Git checkout of this repository, not a bare binary. The service
loads the Lua kernel from `lua/` next to the executable's parent directory (see
`shippedRuntimePath` in `internal/bridge/provider_factory.go`), so copying only
`bin/huyang` leaves a stale kernel and the provider fails with
`provider_incompatible: kernel protocol version 1 is outside the accepted range 2-2`.
Update the checkout and rebuild in place:

```sh
git -C ~/.local/share/huyang fetch <remote> feature/huyang
git -C ~/.local/share/huyang checkout --detach FETCH_HEAD
make -C ~/.local/share/huyang build
systemctl --user restart huyang.service
```

Wait for `$XDG_RUNTIME_DIR/huyang/control.sock` to reappear, then confirm with a
`workspace_open` and a `language_server_status` call. On the first start after an upgrade
from a pre-S20b build the service migrates the receipts out of `registry.json` and splits
the plan files into per-plan records; it logs the counts it kept and dropped.

## State directory

Everything durable lives under the state directory. Every store has a bound, and each bound
is a named constant in the code:

| Path | Contents | Bound |
|---|---|---|
| `registry.json` | Workspace definitions only, format version 2 (`registryStateVersion`). A version 1 file, which also embedded receipts, is migrated on load and rewritten as version 2. | one record per open workspace |
| `receipts/<workspace>.json` | Idempotency receipts, file version 1. A receipt keeps its full result for 15 minutes (`PayloadWindow`), then is trimmed to provenance fields. | 256 receipts per workspace (`PerWorkspace`), 32 MiB in total (`TotalBytes`), 4096 tombstones per workspace (`Tombstones`); an evicted receipt leaves a tombstone so a late retry is refused with `idempotency_receipt_evicted` rather than re-executed |
| `plans/<workspace>/<plan>.json` | One plan record per file, format version 2 (`planRecordVersion`). The version 1 layout, one file per workspace holding every plan, is read on open, split into per-plan files, and removed. | terminal plans: newest 200 kept (`planRetainCount`), dropped after 30 days (`planRetainAge`), bulky payloads stripped after 1 hour (`planCompactAge`); events per plan: first 8 and last 56 (`planEventsHead`, `planEventsTail`) |
| `commit-journals/<workspace>/` | Commit journals, format version 1 (`commitJournalVersion`). Prepared, applying, and recovery-required journals are never collected. | completed journals dropped after 7 days (`commitJournalRetention`), newest 64 kept beyond that (`commitJournalRetainCount`) |
| `diagnostics/<workspace>.json` | Diagnostic ledger, format version 1 (`diagnosticStateVersion`). Findings whose document changed are marked `stale`. | inactive items dropped after 24 hours (`diagnosticRetentionWindow`), 1000 inactive items (`maxInactiveDiagnosticItems`), 500 unreferenced evidence records (`maxUnreferencedDiagnosticEvidence`), 1000 notices (`maxDiagnosticNotices`) |
| `test-history/<workspace>.json` | Revision-keyed test history, format version 1 (`testHistoryVersion`). | last 1000 entries |
| `sandboxes/sandbox-*/` | Isolated preparation trees, each with an `owner.json` marker (version 1). Sandboxes whose plan is no longer referenced are removed on service start. | one per prepared plan |
| `command-cache/go-build` | Compiler build cache shared by every verification stage, so a repeated `verify_run` does not recompile the package set. Entries are addressed by the hash of their inputs; HOME and `XDG_CACHE_HOME` stay throwaway per run. | trimmed by the Go toolchain on its own schedule, not by Huyang; delete the directory to reclaim it, or set `HUYANG_COMMAND_CACHE=off` for a throwaway cache per run |
| `http-token` | Bearer token for `--http` and `--pprof`. | one file |

In-memory stores are bounded too: 32 revisions per document (`maxRevisionsPerDocument`),
20000 live handles (`maxLiveHandles`), 64 live result sets (`maxLiveResultSets`), and 20
diagnostic updates per reply (`maxDiagnosticUpdates`).

## Friction logging scope

Friction logging is evaluated in the process that services the tool call. With the
long-lived user daemon, set `HUYANG_FRICTION` and `HUYANG_FRICTION_DIR` in
`huyang.service`. The spool file is
`<spool dir>/huyang/<short hostname>-<YYYY-MM-DD>.jsonl`, where the spool directory is
`HUYANG_FRICTION_DIR`, else `$XDG_DATA_HOME/toolfeedback`, else
`~/.local/share/toolfeedback`. Environment variables set only on an MCP adapter process do
not cross the adapter/service boundary and therefore cannot relocate the daemon's spool.

Each line records the tool, the argument key names, the outcome, and the duration; never
argument values or replies.

## Trust and repository commands

Huyang preserves bytes by default and never runs a discovered repository command merely
because it exists. Checked-in `.huyang.toml` declares formatter/check/test stages; without
one, the commands are detected from the repository (a Makefile's `test`, `lint` and
`check` targets, `go.mod`, a Python project's declared pytest and ruff through its own
`.venv`, `uv.lock` or `poetry.lock` environment, a `package.json` test script). User
policy under `$XDG_CONFIG_HOME/huyang/config.toml` must trust the workspace root before
those commands can execute:

```sh
huyang trust /path/to/repository   # appends the resolved root to [trust].roots
huyang trust --list
huyang trust --remove /path/to/repository
```

`huyang trust` edits the file textually, keeps its comments, validates the result before
writing and creates the file with mode 0600 when absent; the running service reads the
policy on the next `verify_run`. Verification always reports its exact revision, scope,
environment, writes, and coverage.

## File lifecycle and Git

`edit_apply` moves, copies and deletes files (`move_file`, `copy_file`, `delete_file`) and
overwrites one with `create_file` plus `replace: true`, each guarded by the file's
revision or content hash and journaled like every native write. A moved or copied file
keeps the source's exact bytes and mode and is never reformatted, so Git recognises the
rename by content. Huyang never writes the Git index: the reply names each path's tracked
state and the `git add` command that stages the change. A copy may read an absolute path
outside the workspace; the reply records its hash and size. Transfers are bounded at
4 MiB (`MaxTransferBytes`).

## Recovery

On workspace open Huyang scans that workspace's commit journals before serving any call.
The outcome depends on the journal state:

- `committed`: the canonical writes finished; a plan still marked `COMMITTING` or
  `RECOVERY_REQUIRED` only lost its receipt transition and is marked `COMMITTED`.
- `rolled_back`: recovery already finished; the plan is marked `ROLLED_BACK`.
- `prepared`: the commit never reached its first canonical write, so nothing is restored.
  The journal is closed as `rolled_back` without touching any file and the plan is marked
  `ROLLED_BACK`.
- `applying` or `recovery_required`: canonical bytes are compared with the journal images.
  If they match a known preimage or postimage, the safe path completes. If a third-party
  write matches neither, Huyang refuses further writes to that workspace and reports
  `commit_recovery_required`; it does not guess or overwrite the external content.

A precondition failure detected before the first canonical write is not a recovery case.
The plan moves to `CONFLICTED`, its journal is rolled back, and the plan can be re-prepared
against the new revision; `RECOVERY_REQUIRED` is reserved for a failure after a write.

Back up both the canonical workspace and Huyang state directory before manual recovery.
Never delete a journal to force progress.

## Versioning

The MCP API (`huyang.workspace/v1alpha1`), the provider-kernel protocol (version 2), and
every durable record format (registry 2, plan record 2, receipts 1, commit journal 1,
diagnostics 1, test history 1, sandbox marker 1) carry explicit versions. The service refuses
an unknown version rather than guessing. No Agent99 executable, interactive UI, or harness
endpoint is shipped, and no compatibility profile exists.

See:

- [Implementation plan](docs/plans/huyang-transactional-semantic-workspace-implementation-plan.md)
- [Modern tool contract](docs/plans/huyang-tools-v1alpha1.md)
- [S20b structural consolidation](docs/plans/huyang-s20b-structural-consolidation.md)
- [Release candidate report](docs/plans/huyang-s20-release-candidate.md)
- [Current implementation handoff](docs/plans/huyang-handoff.md)

## License

MIT. See [LICENSE](LICENSE).

Huyang does not distribute [mfussenegger/nvim-dap](https://github.com/mfussenegger/nvim-dap),
which is licensed under the GNU General Public License version 3. It is a runtime
dependency discovered from the host Neovim installation at provider start (search order
and the `HUYANG_NVIM_DAP_PATH` override are listed under [Requirements](#requirements));
without it the four debugger tools report `unavailable` and everything else works. The
clone that `tests/fetch-nvim-dap.sh` makes under `tests/.deps/` is a test dependency and
is not part of the repository.
