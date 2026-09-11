# Huyang rollout record — 2026-09-10

## Published repositories

- GitHub: https://github.com/iryzhkov/huyang
- Forgejo: https://git.ryzhkov.dev/igor/huyang
- Both public repositories default to `main` and were verified at
  `4f87241a2cff95b6eb5f87275076e65fa9f38047`.

The original Agent99 upstream was retained as historical source and was not rewritten.

## Product split

- Go module: `github.com/iryzhkov/huyang`.
- Shipped executable: `bin/huyang` only.
- Embedded semantic kernel: `lua/huyang`.
- Removed: interactive Agent99 UI, LLM agent loop, Agent99 bridge executable, plugin entry
  point, and their obsolete UI/legacy smoke drivers.
- The embedded Neovim provider is now the default. Huyang environment names take precedence;
  historical aliases remain accepted only where required for state/protocol migration.

## Host rollout

| Host | Install | Service | MCP check | Harnesses | Agent99 plugin |
|---|---|---|---|---|---|
| normandy | commit `4f87241` | enabled, active | Huyang 0.4.0, 17 tools | Claude, Codex, OpenCode | removed |
| homelab | commit `4f87241` | enabled, active | Huyang 0.4.0, 17 tools | Claude, Codex, OpenCode | removed |
| gaming-pc | commit `4f87241` | enabled, active | Huyang 0.4.0, 17 tools | Claude, Codex, OpenCode | removed |
| laptop | explicitly deferred | not changed | not run | not changed | not changed |

Each completed host uses `~/.local/share/huyang/bin/huyang mcp --profile full`. The
`agent99` MCP registration is absent from all three harness configurations. Claude marks
Huyang always-loaded. The old plugin checkout and temporary rollout config copies were moved
to the desktop trash after successful verification.

## Instruction and Neovim configuration

- Replaced Agent99 guidance with the modern Huyang workspace/revision/transaction/evidence
  workflow in `CLAUDE.md`, `AGENTS.md`, and generated
  `~/.config/agents/huyang.md` on all three reachable hosts.
- Updated OpenCode instruction references from `agent99.md` to `huyang.md`.
- Removed the Agent99 lazy.nvim plugin, lock entry, and setup-time MCP registration from
  `iryzhkov/nvim-configuration`; commit `a8c430a` was pushed to both configured upstreams
  and pulled on Homelab and Gaming PC.

## Verification

- Source gates: `make smoke`, `go test ./...`, `go vet ./...`, and
  `git diff --check` exited 0.
- Per reachable host: user service enabled and active; control socket present; MCP
  `initialize` identified `huyang`; `tools/list` returned exactly 17 full-profile tools;
  installed checkout matched `4f87241`; Claude/Codex/OpenCode contained Huyang and no
  Agent99 endpoint; Huyang instruction files existed and contained no Agent99/open-workspace
  guidance.
- Neovim configuration: `bash -n setup.sh`, headless config load, and
  `git diff --check` exited 0 before commit.

## Deferred host

The laptop deliberately accepts no inbound SSH from Normandy, Homelab, or Gaming PC, and
Normandy has no laptop SSH alias for `t3-backlog --host`. On 2026-09-10 the user explicitly
directed that Laptop be skipped for now. It remains on Agent99 and is outside this completed
rollout; no laptop task was queued.

## Addendum 2026-09-11: S20b redeploy

The three deployed hosts above were installed at `4f87241`. Normandy was redeployed on
2026-09-11 to the S20b tip, `fe143fe97dc7ac0060489c0d0840086a6c93122d` on
`feature/huyang`. Homelab and gaming-pc are still at `4f87241` and need the same
procedure; laptop remains deferred.

The install location `~/.local/share/huyang` is a Git checkout of the repository, and the
service loads the Lua kernel from `lua/` relative to the executable's parent directory
(`shippedRuntimePath` in `internal/bridge/provider_factory.go`). Copying only the binary
therefore leaves a stale kernel; on normandy that produced
`provider_incompatible: kernel protocol version 1 is outside the accepted range 2-2` until
the checkout itself was updated. The procedure that worked:

1. `git -C ~/.local/share/huyang fetch <repo> feature/huyang`
2. `git -C ~/.local/share/huyang checkout --detach FETCH_HEAD`
3. `make -C ~/.local/share/huyang build`
4. `systemctl --user restart huyang.service`
5. Wait for `$XDG_RUNTIME_DIR/huyang/control.sock` to reappear.
6. Confirm with a `workspace_open` and a `language_server_status` call through the
   registered MCP command.

On its first start the S20b build migrated the state directory: 2,237 legacy idempotency
receipts were moved out of `registry.json` into per-workspace receipt files (6 dropped),
and every plan file was converted to per-plan records. The state directory went from
789 MB to 32 MB and the service's resident set from 4.2 GB to 60 MB after settling. The
MCP `tools/list` check now expects 19 full-profile tools, not the 17 recorded in the host
table above.
