# Huyang

Huyang is a local transactional semantic workspace for coding agents. It combines a
long-lived Go service with isolated Neovim providers so MCP clients can navigate, edit,
verify, and recover changes against an explicit workspace revision.

The MCP surface is `huyang.workspace/v1alpha1`. It has three frozen profiles: `full` (13
tools), `edit` (11 tools, the default of `huyang mcp`) and `orient` (8 tools). A fourth,
`experimental`, is `full` plus the input shapes still being designed; a caller asks for it
by name. There is no legacy or compatibility surface: the embedded Neovim provider is the
only backend and the tools below are the only tools.

## What Huyang provides

- Explicit project and document workspaces with revision-bound reads and writes.
- Semantic handles backed by Tree-sitter, LSP, and exact text anchors.
- Isolated multi-operation preparation with formatter, diagnostic, check, and test evidence.
- Journaled canonical apply, crash recovery, and compensating undo.
- Bounded impact analysis, and the full configured test suite as a gate on every trusted preparation.
- A native text path that still works when Git, Neovim, parsers, or language servers are absent.

The service owns durability, policy, scheduling, sandboxes, and recovery. A small embedded
Lua kernel under `lua/huyang` owns Neovim buffer, Tree-sitter, and LSP operations and
speaks kernel protocol version 2 to the service.

## Agent guide

[docs/agent-guide.md](docs/agent-guide.md) names the cheapest correct call for each common
operation (read a file, read a region by name, change a line you know, replace a block
located by content, rename a local identifier, add a file, coordinate edits and test) with
the measured token cost of each, from the benchmark under
[bench/agent-efficiency](bench/agent-efficiency/README.md). Read it before wiring Huyang into
an agent's instructions. What the diagnostic evidence behind edit verdicts and the
`diagnostics` tool guarantees is in [docs/diagnostics-contract.md](docs/diagnostics-contract.md).

## Tools

The `full` profile lists all 13 tools in this order. `edit` and `orient` are fixed subsets
of it.

Orientation, in every profile (`full`, `orient`, `edit`, and `experimental`):

- `workspace_open`, `workspace_inspect`, `search`, `symbol_find`, `navigate`, `read`,
  `diagnostics`, `evidence_get`.

Editing and verification, in `full` and `edit`:

- `edit_apply`, `change_plan`, `revision_diff`.

Language-server administration, in `full` (and `experimental`) only. A session on the
default `edit` profile does not see these two tools:

- `language_server_status`, `language_server_setup`.

`language_server_setup` only restarts the provider (`action=restart`); Huyang does not
install language servers or parsers (see [Requirements](#requirements)).

That gives 13 tools in `full`, 8 in `orient`, and 11 in `edit`. The service refuses to start
if the registered catalog differs from those counts, and every tool declares the scheduler
class it runs under. The `experimental` profile adds a few experimental arguments to `full`
and no tools of its own; it is not a contract. The contract is described
in [docs/plans/huyang-tools-v1alpha1.md](docs/plans/huyang-tools-v1alpha1.md).

The debugger tools (`debug_session`, `debug_breakpoints`, `debug_control`, `debug_inspect`),
the `debug` and `debugger` profiles, the `/mcp/debug` route and the nvim-dap dependency were
removed on 2026-10-17, together with `file_range` targets and the `include_ranges` search
option. `verify_run`, its affected-test selection and the `--external-job-quota` flag were
removed the same day: run builds and tests in the shell, and `change_plan` prepare still
runs a trusted root's commands against its sandbox. Later the same day `code_actions`, the
`install` action of `language_server_setup`, the experimental `execution_graph` and
`path_explain` tools and the `path_unreachable` invariant were removed as well: a
`change_plan` `apply_code_action` operation names the action by title, and a title the
server does not offer is refused with the titles it does. These are breaking changes inside
`v1alpha1`; see [the contract's change log](docs/plans/huyang-tools-v1alpha1.md#change-log).

## Requirements

- Go 1.27 or newer to build.
- Neovim with API level 13 or newer for the semantic provider. The service
  reads `nvim_get_api_info` at provider start and refuses an older Neovim with an
  `incompatible` failure. API level 13 is Neovim 0.11, which introduced the
  `vim.lsp.config` and `vim.lsp.enable` functions the kernel depends on; Neovim 0.12 also
  satisfies the check.
- The language servers, parsers, and formatters required by your projects.

The native text core does not require Neovim or an LSP.

Huyang installs no language servers or Tree-sitter parsers. Install them outside it, the
same way an interactive Neovim gets them: with Mason (`:MasonInstall gopls`, or
`ensure_installed` in your mason-lspconfig setup) and nvim-treesitter (`:TSInstall go`), or
with the system package manager (`pacman -S gopls`, `brew install pyright`). The provider
enables a server Mason has installed, and one on `PATH` that an lspconfig config on the
runtimepath describes, each time it starts. After installing one, call
`language_server_setup` with `action=restart` (in the `full` profile), or restart the
service, so the running provider picks it up. `language_server_status` names a language
that has no server, and lists the Mason packages that serve it under `install_options`.

## Build and test

```sh
make build
make smoke
go test ./...
go vet ./...
make live
```

`make check` runs the lot.

`make live` is the service boundary: it starts the daemon as its own process with a
disposable socket, state directory and friction spool, reaches it through the `huyang mcp`
adapter, and drives real calls against the fixture repositories in `tests/fixtures/`. It is
behind the `live` build tag, so `go test ./...` stays fast, and it is the only gate that
sees what a client actually receives: the guide field reached every in-process test and no
client at all, because the renderer that puts an envelope on the wire did not copy it.

The suite includes a contract walk that calls every operation kind and navigation relation
the catalog advertises. An advertised value with no implementation behind it fails there.
The three frozen catalogs and the experimental one are also compared byte for byte against
`docs/plans/fixtures/huyang-v1alpha1/catalog-*.json`; a deliberate schema change is adopted
with `go test ./internal/mcpapi -run TestFrozenCatalog -update` and reviewed as a diff.

The sole shipped executable is `bin/huyang`.

## Run the service

Start the long-lived service:

```sh
./bin/huyang serve
```

`huyang serve` accepts these flags:

| Flag | Default | Purpose |
|---|---|---|
| `--socket PATH` | `$HUYANG_SOCKET`, else `<runtime dir>/huyang/control.sock` | Private Unix control socket, mode 0600. The runtime directory is `$XDG_RUNTIME_DIR` when it is set to an absolute path, else `/run/user/<uid>` when that is a real directory (not a symbolic link) owned by the current user and not group- or world-writable, else `$TMPDIR/huyang-<uid>`. |
| `--state-dir PATH` | `$HUYANG_STATE_DIR`, else `$XDG_STATE_HOME/huyang`, else `~/.local/state/huyang` | Durable service state directory, mode 0700. |
| `--http LOOPBACK:PORT` | unset | Optional Streamable HTTP listener. The host must be a numeric loopback address. Routes are `/mcp` (`full`), `/mcp/orient` and `/mcp/edit`; there is no route for `experimental`. Every request needs `Authorization: Bearer <token>`. The transport is stateless. |
| `--pprof LOOPBACK:PORT` | unset | Optional `net/http/pprof` listener behind the same bearer token, for heap and goroutine profiles of the live service. |
| `--provider-quota N` | 4 | Maximum concurrent provider-backed jobs. |
| `--max-providers N` | 4 | Maximum live providers, including canonical and sandbox providers. |
| `--max-language-servers N` | 4 | Maximum language-server clients per provider (at most 16 across the default pool). |

The process limits are independent of job quotas. Zero selects the bounded default; negative
limits are refused. Opening and closing processes count toward capacity. When the provider
pool is full, it closes the least recently used idle canonical provider before admitting a
replacement. A lease lasts until its caller releases it, including cancellation cleanup and
background work. Leased or locked slots and sandbox providers held by a plan are never
evicted for capacity. If every slot is held, opening fails with
`provider capacity exhausted`; finish or discard work before retrying, or raise
`--max-providers`. The existing 15 minute idle reaper and plan expiry also release capacity.
Workspace IDs survive eviction, and reopening advances the provider epoch.

Language servers use a separate LRU within each provider, installed before the headless
init can auto-attach servers. Revisiting a loaded buffer restarts its evicted server on demand.
Clients used by active tool calls, clients with outstanding
requests or work progress, clients still initializing, and clients attached to unsaved
buffers are held. Transactions and sandbox providers pin their clients. An eviction waits for the server's exit callback before reusing its slot; a slow exit or
an all-held pool returns `language_server_capacity` instead of exceeding the bound. Retry
after work finishes, or raise `--max-language-servers` for a plan that needs more languages
simultaneously. The bound counts LSP clients; servers may spawn their own helper processes
and use very different amounts of memory.

The bearer token for `--http` and `--pprof` is generated on first use and stored in
`<state-dir>/http-token` with mode 0600; the service prints the token file path at start.

Connect an MCP client through the stdio adapter:

```sh
./bin/huyang mcp --profile full
```

`huyang mcp` accepts `--profile edit|full|orient|experimental` (default `edit`) and
`--socket PATH` (default as for `serve`, derived the same way, so the adapter finds the
service's socket even when a harness starts it without `XDG_RUNTIME_DIR`). It refuses any
other profile name. When a pinned socket (`--socket` or `HUYANG_SOCKET`) does not exist and
lies in another UID's `/run/user/<n>/`, the adapter fails with an error that names both UIDs
and the socket it would use without the pin; it never substitutes that path for the pinned
one. The third
subcommand, `huyang trust`, administers the user trust policy (see "Trust and repository
commands"). The adapter is
intentionally thin: it forwards newline-delimited JSON-RPC to the service, and on a service
disconnect it reconnects for up to 30 seconds, replays the client's `initialize` exchange,
and resends outstanding requests. Workspace identity, receipts, providers, and recovery
state live in the service and survive adapter reconnects.

### The client's working directory

The service is shared and long-lived, so its own working directory means nothing to any
client. The adapter is started by the client, and announces two things on every connection
it makes to the service, including the one that replaces a lost connection: the directory
the client started it in, and the agent session (`HUYANG_SESSION_ID`, else
`CLAUDE_CODE_SESSION_ID`, else a name the adapter gives itself, `adapter-<random>`, kept
for its lifetime). The service uses the directory for two things, and only when the answer
is unambiguous:

- **A relative `root`** (`"."`, `"../other"`) is resolved against that directory, following
  symbolic links before applying `..`, so `link/..` means what the agent's shell means. It
  is resolved only when the client's directory and the result both lie in a Git working
  tree (a `.git` file counts, so a linked worktree is its own tree). Edits are included,
  because the caller wrote the root. The reply warns `root "." was resolved against your
  working directory to /abs; send absolute roots`.
- **A default workspace, for read-only tools only.** A call to `workspace_open`,
  `workspace_inspect`, `search`, `symbol_find`, `navigate`, `read`, `diagnostics`,
  `evidence_get`, `revision_diff` or `language_server_status` that names
  neither `workspace_id` nor `root` reads the repository the client was started in, but
  only when the client was started at the top of that working tree and the call names no
  absolute path (an absolute path keeps its exact-document meaning). The reply warns
  `no workspace was named, so the repository your client was started in, /abs, was used;
  pass root to name another`.

Nothing is defaulted when the connection announced no directory (HTTP, an older adapter),
when the client was started outside any working tree (Codex starts every MCP server in the
home directory), or when it was started in a subdirectory, where a relative path would
mean one file to the agent and another to the workspace. An explicit `workspace_id` or
absolute `root` always wins. **Mutations are never defaulted:** `edit_apply` and
`change_plan` need the workspace named, because their
guards check content, not intent, and an edit meant for another repository could land in
this one. A replay key is built after a root is adopted into a `workspace_id`, so a retry
that names the same workspace by `"."`, by its absolute root or by its ID replays the
first call instead of applying it again.

## Environment variables

Every variable the shipped code reads. "Fallback" names are read only when the `HUYANG_`
name is empty; they survive from the Agent99 lineage and are not documented anywhere else.

| Variable | Default | Purpose | `AGENT99_` fallback |
|---|---|---|---|
| `HUYANG_SOCKET` | `$XDG_RUNTIME_DIR/huyang/control.sock`, else `/run/user/<uid>/huyang/control.sock` when that directory is private to the user, else `$TMPDIR/huyang-<uid>/huyang/control.sock` | Overrides the derived default for `--socket` in `serve` and `mcp`. Only for non-standard layouts; a standard install leaves it unset. | no |
| `HUYANG_STATE_DIR` | `$XDG_STATE_HOME/huyang` | Default for `--state-dir`. | no |
| `HUYANG_PROVIDER_BACKEND` | `embed` | Provider backend name. Only `embed` (or empty) is accepted; anything else fails at provider start. | `AGENT99_PROVIDER_BACKEND` |
| `HUYANG_RUNTIME_PATH` | directory of the executable, its parent, or the working directory, whichever holds `lua/huyang/rpc.lua` | Runtime path handed to the embedded Neovim so it can load the kernel. | `AGENT99_RUNTIME_PATH` |
| `HUYANG_HEADLESS_INIT` | unset | Init file handed to every embedded Neovim the service starts, for language-server configuration. | `AGENT99_HEADLESS_INIT` |
| `HUYANG_FRICTION` | unset | `1` enables the friction spool, `0` disables it; otherwise the spool is enabled by a file named `enabled` in the spool directory. | `AGENT99_FRICTION`, then `TOOLFEEDBACK` |
| `HUYANG_FRICTION_DIR` | `$XDG_DATA_HOME/toolfeedback`, else `~/.local/share/toolfeedback` | Spool directory; Huyang writes under its own `huyang/` subdirectory. | `AGENT99_FRICTION_DIR`, then `TOOLFEEDBACK_DIR` |
| `HUYANG_FRICTION_RETENTION_DAYS` | 90 | Days this host's spool files are kept, matching the `toolfeedback` reader's own retention; older ones, and the oldest beyond 256 MiB in total (`frictionSpoolMaxBytes`), are removed at service start and when the day file rotates. Files pulled from other hosts are left alone. | no |
| `HUYANG_COMMAND_CACHE` | on | `off`, `0` or `false` gives every verification command throwaway toolchain caches instead of the shared ones under the state directory. Full isolation, at the price of recompiling and re-downloading in every stage. | no |
| `HUYANG_PLAN_IDLE_TTL` | `1h` | How long a plan that is not applied or discarded may sit unused (a Go duration such as `30m` or `4h`; `0` or `off` disables it). Past it, an intent or a preparation moves to `EXPIRED`, and a prepared plan's sandbox and provider are released. Any change to the plan, and any read, verification or apply lookup of its prepared revision, restarts the clock; a service restart does not. An expired plan can be prepared again, edited or discarded. Checked every minute and when a workspace is loaded. | no |
| `HUYANG_DIRECT_STATE_DIR` | a fresh temporary directory | State directory for the in-process direct mode; only the test suites call that path. The service uses `--state-dir`. | no |
| `HUYANG_TEST_FAULTS` | unset | `1` arms the kernel's fault-injection hooks in the embedded provider. Test-only. | no |
| `HUYANG_FORMAT` | off | Formatting after a provider-side symbol edit: `range`, `file`, or off. Read by the Lua kernel from the provider's environment. | `AGENT99_FORMAT` |
| `HUYANG_POST_EDIT_WAIT` | wait | `0`, `false`, or `off` defers the provider's post-edit diagnostic verdict. Read by the Lua kernel. | `AGENT99_POST_EDIT_WAIT` |
| `HUYANG_DEBUG_VERDICT` | unset | Any value makes the kernel report which signal ended a post-edit wait. Read by the Lua kernel. | `AGENT99_DEBUG_VERDICT` |
| `HUYANG_CARGO_TARGET_MAX_AGE_DAYS` | 30 | For a Cargo project the provider points `CARGO_TARGET_DIR` (unless already set) at `$XDG_CACHE_HOME/huyang/cargo-target/<name>-<hash of root>`, else `~/.cache/...`, shared by every session on that root, so rust-analyzer's builds stay out of both the project's `target/` and `/tmp`. Directories no session has used for this many days, or whose project root no longer exists, are removed when a provider starts. Read by the Lua kernel. | no |
| `CLAUDE_CODE_SESSION_ID` | random per process | Session identifier recorded in friction events when the client is Claude Code. | no |
| `XDG_RUNTIME_DIR`, `XDG_STATE_HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME` | XDG defaults | Bases for the socket, state directory, friction spool, `huyang/config.toml` trust policy, and private `toolfeedback/correlation` keys. | no |

The kernel variables reach Neovim because the provider inherits the service's environment;
set them on the service, not on an MCP adapter. Repository commands run under an isolated
environment that forwards only `PATH`, `TMPDIR`, `LANG`, `LC_ALL`, and a fixed set of
package and registry homes (`MISE_*`, `RUSTUP_HOME`, `CARGO_HOME`, `GRADLE_USER_HOME`,
`GOMODCACHE`, `NUGET_PACKAGES`), so a command can resolve dependencies the machine has
already fetched. Its home directory is a throwaway one, while its caches are the shared
ones under `command-cache/` described in [State directory](#state-directory).

The embedded provider also takes a per-root lock file under `agent99-huyang/` in the same
runtime directory as the socket, or under `$TMPDIR/agent99-huyang-<uid>/` when there is
none; the directory name is historical and only the lock lives there. The provider refuses a
lock directory that another user owns or that is group- or world-writable.

## User service

`contrib/systemd/huyang.service` is a systemd user unit:

```ini
[Unit]
Description=Huyang semantic workspace service

[Service]
Type=simple
WorkingDirectory=%h/.local/share/huyang
ExecStart=%h/.local/share/huyang/bin/huyang serve
Restart=on-failure
# Tune to the host's available memory and language mix. Throttles the whole tree.
MemoryHigh=2G
RestartSec=2

[Install]
WantedBy=default.target
```

Use `MemoryHigh=2G` as an initial service memory budget and tune it for the host and
language mix (Java and Rust can need substantially more). It applies memory pressure and
throttling to the service and its descendants; it does not trigger Huyang LRU eviction or
guarantee protection from OOM. The process caps bound concurrency, not bytes.

The unit has no `After=default.target`: it is wanted by `default.target`, so that ordering
would form a cycle with any unit ordered after `huyang.service`, and systemd would break the
cycle by deleting one of the start jobs at boot.

After installing the unit:

```sh
systemctl --user daemon-reload
systemctl --user enable --now huyang.service
```

Register the MCP command in each harness as
`~/.local/share/huyang/bin/huyang mcp --profile full`, with no socket. The service and the
adapter derive the same socket for the same user: `$XDG_RUNTIME_DIR/huyang/control.sock`,
else `/run/user/<uid>/huyang/control.sock` when that directory is a real directory owned by
the user and not group- or world-writable, else `$TMPDIR/huyang-<uid>/huyang/control.sock`.
A harness subprocess that does not inherit `XDG_RUNTIME_DIR` therefore still finds the
systemd user service's socket. The last, temporary-directory fallback depends on `TMPDIR`,
so on a host without `/run/user/<uid>` both sides must share `TMPDIR` or `XDG_RUNTIME_DIR`. Set `HUYANG_SOCKET` or pass `--socket` only for a
non-standard layout; a pinned path is tied to one UID and breaks when the registration is
copied to another user.

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

The restart is hot: sessions connected through `huyang mcp` stay connected. The adapter is
a process of its own, and when the service goes away it retries the socket for thirty
seconds, replays the client's initialize handshake and resends the requests that were in
flight, so a client sees a pause rather than a disconnection. `TestASessionSurvivesTheServiceBeingRedeployed`
in `internal/livetest` restarts the daemon under a live session and keeps using it.

Two things do not survive, and both are visible in the reply rather than silent. Handles
(`rng_`, `sym_`, `set_`) live in a bounded in-memory store, so one taken before the restart
is refused afterwards as `handle_unknown`, with taking a fresh one offered as the next
step. And a build that does not come up within the adapter's thirty seconds - a crash loop,
for instance - is a disconnection like any other, so run `make live` against the new build
before restarting the service that agents are using.

Wait for `$XDG_RUNTIME_DIR/huyang/control.sock` to reappear, then confirm with a
`workspace_open` and a `workspace_inspect` call; a session on the `full` profile can also
call `language_server_status`, which the default `edit` profile does not carry. On the first start after an upgrade
from a pre-S20b build the service migrates the receipts out of `registry.json` and splits
the plan files into per-plan records; it logs the counts it kept and dropped.

## State directory

Everything durable lives under the state directory. Every store has a bound, and each bound
is a named constant in the code:

| Path | Contents | Bound |
|---|---|---|
| `registry.json` | Workspace definitions only, format version 2 (`registryStateVersion`). A version 1 file, which also embedded receipts, is migrated on load and rewritten as version 2. A record whose root a sweep found gone carries `MissingSince`. | one record per registered workspace; a workspace whose root has stayed gone for 7 days (`missingRootGrace`) is forgotten (see below) |
| `receipts/<workspace>.json` | Idempotency receipts, file version 1. A receipt keeps its full result for 15 minutes (`PayloadWindow`), then is trimmed to provenance fields. | 256 receipts per workspace (`PerWorkspace`), 32 MiB in total (`TotalBytes`), 4096 tombstones per workspace (`Tombstones`); an evicted receipt leaves a tombstone so a late retry is refused with `idempotency_receipt_evicted` rather than re-executed |
| `plans/<workspace>/<plan>.json` | One plan record per file, format version 2 (`planRecordVersion`). The version 1 layout, one file per workspace holding every plan, is read on open, split into per-plan files, and removed. | terminal plans: newest 200 kept (`planRetainCount`), dropped after 30 days (`planRetainAge`), bulky payloads stripped after 1 hour (`planCompactAge`); events per plan: first 8 and last 56 (`planEventsHead`, `planEventsTail`) |
| `commit-journals/<workspace>/` | Commit journals, format version 1 (`commitJournalVersion`). Prepared, applying, and recovery-required journals are never collected. | completed journals dropped after 7 days (`commitJournalRetention`), newest 64 kept beyond that (`commitJournalRetainCount`) |
| `diagnostics/<workspace>.json` | Diagnostic ledger, format version 2 (`diagnosticStateVersion`); a version 1 ledger is migrated on open, and a version-1 binary refuses the upgraded file. Findings whose document changed are marked `stale`. A finding is identified by what it says (producer, document, severity, code, source, message, and its occurrence among identical ones in that document) rather than by where it sits, so an edit above an untouched warning moves it without retiring it and announcing an identical one; the reported range follows the latest observation. | inactive items dropped after 24 hours (`diagnosticRetentionWindow`), 1000 inactive items (`maxInactiveDiagnosticItems`), 500 unreferenced evidence records (`maxUnreferencedDiagnosticEvidence`), 1000 notices (`maxDiagnosticNotices`) |
| `test-history/<workspace>.json` | No longer written: it fed the affected-test selection of the removed `verify_run`. A state directory from an earlier build may still hold it. | removed with the rest of a forgotten workspace's state |
| `sandboxes/sandbox-*/` | Isolated preparation trees, each with an `owner.json` marker (version 1). Sandboxes whose plan is no longer referenced are removed on service start. | one per prepared plan |
| `command-cache/` | The toolchain caches every verification command shares, so a repeated preparation neither recompiles nor re-downloads: `xdg/` is the cache home for everything that follows the XDG specification, and `go-build/`, `golangci-lint/`, `ccache/`, `sccache/`, `zig/`, `npm/`, `yarn/`, `pip/`, `uv/`, `deno/` and `composer/` cover the toolchains that keep a cache elsewhere. HOME stays throwaway per run, and package or registry homes still come from the user. | removed whole when it passes 4 GiB (`commandCacheMaxBytes`) or when nothing has used it for 30 days (`commandCacheMaxAge`), checked at most daily (`commandCachePruneInterval`) at service start; every entry is reproducible, so deleting the directory only costs a cold build. `HUYANG_COMMAND_CACHE=off` gives each run a throwaway cache instead |
| `http-token` | Bearer token for `--http` and `--pprof`. | one file |

The per-workspace stores (`receipts/`, `diagnostics/`, `plans/`, `commit-journals/`,
`test-history/`) are also bounded by the registry. At service start and every hour
(`registrySweepInterval`) a sweep records when each registered root is first answered as not
existing, clears that mark if the root comes back, and forgets a workspace whose root has
stayed gone for 7 days (`missingRootGrace`), deleting its files in every store. Time rather
than the root's surroundings decides, because an unmounted disk leaves its mount point behind
and looks exactly like a deleted worktree; a permission or I/O error never counts as missing.
A workspace is kept, whatever became of its root, while a stateful call, a scheduler lane, a
provider or sandbox, a plan being prepared or committed, or an unfinished commit journal still
holds it. The same sweep deletes per-workspace files whose workspace is not registered at all,
except an unfinished commit journal. Forgetting only costs history: opening the root again
registers it under a new ID.

Registered workspaces are opened on first use rather than at service start, so the
diagnostic ledgers and plan records of workspaces nobody is using stay on disk. The exception
is a workspace with an unfinished commit journal or a plan that was committing, which is
opened at start so its recovery runs then.

In-memory stores are bounded too: 32 revisions per document (`maxRevisionsPerDocument`),
16 MiB of superseded revision records per workspace, least recently superseded evicted
first (`maxHistoricalRevisionBytes`),
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
argument values or replies. Partial replies include `has_results`, batch reads count
target failure codes in `sub_codes`, and an `initialize` event records standard advertised
capability names once per SDK connection, without their values.

With an explicitly provisioned host-local key, tool calls also include precise
`started_at` and a `tf-call-v1` HMAC of the original tool name and arguments, captured
before argument normalization or workspace defaults. Repeated identical calls have equal
MACs; downstream joins still require a unique native call within ±5 seconds. Producer
session IDs remain producer identities.

Keys live at `$XDG_CONFIG_HOME/toolfeedback/correlation`, default
`~/.config/toolfeedback/correlation`, outside the spool and shipping inputs. Huyang reads
private keys only; the separate `toolfeedback correlation init-key` administrative
command provisions them during deployment. Missing/unsafe keys or unsupported JSON omit
correlation without changing replies. Keys are reread per call, so rotation needs no
producer restart. `HUYANG_FRICTION=0` skips canonicalization and all key/spool I/O.
See the [producer contract](docs/toolfeedback-contract.md) for exact encoding, key file
format, privacy, matching limits, and public interoperability vectors.

The service keeps its own host's day files for `HUYANG_FRICTION_RETENTION_DAYS` (90 by
default, the retention `toolfeedback prune` applies) and at most 256 MiB of them, and never
removes the current day's file. Day files of other hosts, which `toolfeedback pull` copies
into the same directory, are left for `toolfeedback prune`.

The session a line belongs to is the one the adapter announces when it connects, taken
from `HUYANG_SESSION_ID` or `CLAUDE_CODE_SESSION_ID` in the adapter's environment and
reduced to the characters an identifier is made of. The daemon serves many agents and its
own environment names none of them, so without that announcement every call would be
spooled under one anonymous process and a report could not tell two sessions apart. A
client that announces nothing is still spooled under the servicing process.

## Trust and repository commands

Huyang preserves bytes by default and never runs a discovered repository command merely
because it exists. Commands run only inside `change_plan` prepare, against the staged
sandbox; builds and tests of the canonical tree belong to the agent's shell. Checked-in
`.huyang.toml` declares formatter/check/test stages; without
one, the commands are detected from the repository (a Makefile's `test`, `lint` and
`check` targets, `go.mod`, a Python project's declared pytest and ruff through its own
`.venv`, `uv.lock` or `poetry.lock` environment, a `package.json` test script). User
policy under `$XDG_CONFIG_HOME/huyang/config.toml` must trust the workspace root before
those commands can execute:

```sh
huyang trust /path/to/repository   # appends the resolved root to [trust].roots
huyang trust --list
huyang trust --remove /path/to/repository
huyang trust --config PATH ...     # any of the above against another policy file
```

`huyang trust` edits the file textually, keeps its comments, validates the result before
writing and creates the file with mode 0600 when absent; the running service reads the
policy on the next `change_plan` prepare. A preparation always reports each stage's exact
revision, scope, environment, writes, and coverage; in a trusted root a failing check or
test fails it.

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
diagnostics 2, sandbox marker 1) carry explicit versions. The service refuses
an unknown version rather than guessing. No Agent99 executable, interactive UI, or harness
endpoint is shipped, and no compatibility profile exists. Tool removals and input-shape cuts
inside `v1alpha1` are deliberate, documented breaking changes; the frozen catalog fixtures
are regenerated with them and the contract's change log records each one.

See:

- [Implementation plan](docs/plans/huyang-transactional-semantic-workspace-implementation-plan.md)
- [Modern tool contract](docs/plans/huyang-tools-v1alpha1.md)
- [S20b structural consolidation](docs/plans/huyang-s20b-structural-consolidation.md)
- [Release candidate report](docs/plans/huyang-s20-release-candidate.md)
- [Current implementation handoff](docs/plans/huyang-handoff.md)

## License

MIT. See [LICENSE](LICENSE).
