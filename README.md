# Huyang

Huyang is a local transactional semantic workspace for coding agents. It combines a
long-lived Go service with isolated Neovim providers so MCP clients can navigate, edit,
verify, debug, and recover changes against an explicit workspace revision.

The modern MCP surface is `huyang.workspace/v1alpha1`. It exposes 17 tools through the
portable `full` profile, with fixed `orient`, `edit`, and `debug` profiles for
smaller tool catalogs.

## What Huyang provides

- Explicit project and document workspaces with revision-bound reads and writes.
- Semantic handles backed by Tree-sitter, LSP, and exact text anchors.
- Isolated multi-operation preparation with formatter, diagnostic, check, and test evidence.
- Journaled canonical apply, crash recovery, and compensating undo.
- Bounded impact analysis and a strict distinction between affected and full test coverage.
- A four-tool DAP facade with explicit evaluation side-effect policy.
- A native text path that still works when Git, Neovim, parsers, or language servers are absent.

The service owns durability, policy, scheduling, sandboxes, and recovery. A small embedded
Lua kernel under `lua/huyang` owns Neovim buffer, Tree-sitter, LSP, and DAP operations.
The former interactive Agent99 UI/agent implementation is not part of this repository.

## Requirements

- Go 1.27 or newer to build.
- Neovim 0.11 or newer for semantic and debugger providers.
- The language servers, parsers, formatters, and debugger adapters required by your projects.

The native text core does not require Neovim or an LSP.

## Build and test

```sh
make build
make smoke
go test ./...
go vet ./...
```

The sole shipped executable is `bin/huyang`; this repository does not build or install the former Agent99 agent or bridge.

## Run the service

Start the long-lived service:

```sh
./bin/huyang serve
```

The embedded provider is the default. Historical environment-variable aliases remain
accepted during migration. The default control socket is
`$XDG_RUNTIME_DIR/huyang/control.sock`; durable state defaults to
`$XDG_STATE_HOME/huyang` (or `~/.local/state/huyang`).

Connect an MCP client through the stdio adapter:

```sh
./bin/huyang mcp --profile full
```

Available profiles are:

- `full`: all 17 modern coding tools.
- `orient`: workspace, search, navigation, read, diagnostics, and evidence.
- `edit`: orientation plus code actions, transactions, verification, and revision diffs.
- `debug`: orientation plus the four debugger tools.

The adapter is intentionally thin. Workspace identity, receipts, providers, and recovery
state survive adapter reconnects in the service.

## User service

A typical systemd user unit is:

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
`~/.local/share/huyang/bin/huyang mcp --profile full`. Remove the old
`agent99-bridge mcp` registration so only one semantic workspace backend is advertised.

## Trust and repository commands

Huyang preserves bytes by default and never runs a discovered repository command merely
because it exists. Checked-in `.huyang.toml` declares formatter/check/test stages. User
policy under `$XDG_CONFIG_HOME/huyang/config.toml` must trust the workspace root before
those commands can execute. Verification always reports its exact revision, scope,
environment, writes, and coverage.

## Recovery

On startup Huyang scans its durable journals before opening a workspace. If canonical bytes
match a known journal image, it completes the safe recovery path. If a third-party write
matches neither preimage nor postimage, Huyang refuses further writes and reports
`RECOVERY_REQUIRED`; it does not guess or overwrite the external content.

Back up both the canonical workspace and Huyang state directory before manual recovery.
Never delete a journal to force progress.

## Versioning

The modern API, provider-kernel protocol, and durable record formats carry explicit
versions. Historical environment-variable aliases remain accepted for migration, but no
Agent99 executable, interactive UI, or harness endpoint is shipped.

See:

- [Implementation plan](docs/plans/huyang-transactional-semantic-workspace-implementation-plan.md)
- [Modern tool contract](docs/plans/huyang-tools-v1alpha1.md)
- [Release candidate and migration guide](docs/plans/huyang-s20-release-candidate.md)
- [Current implementation handoff](docs/plans/huyang-handoff.md)

## License

MIT. See [LICENSE](LICENSE).
