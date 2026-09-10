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
