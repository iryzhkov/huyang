# Huyang S01 package-boundary record

Captured: 2026-09-10
Stage: S01 — Mechanical package boundary
Starting commit: `d375d70842f5519cfe1862f0d34fd7401848203f`

## Resulting layout

- `cmd/agent99-bridge` owns only process entry. It delegates immediately to the internal
  compatibility runtime and continues to produce `bin/agent99-bridge`.
- `internal/bridge` owns the extracted legacy runtime as one temporary package. Moving the
  existing files and their same-package tests together keeps the current unexported
  relationships and behavior intact.
- `lua/agent99` remains the semantic editor kernel. S01 does not change Lua behavior or its
  wire contract with the Go runtime.

## Ownership for the next boundaries

The following ownership is fixed before S02 introduces any interface:

| Boundary | Owner | Current S01 files |
| --- | --- | --- |
| MCP/agent adapters | Go adapter layer: protocol negotiation, schemas, result rendering, and process-facing dispatch only | `mcp.go`, `schemas.go`, `agent.go`, and `main.go` in `internal/bridge` |
| Workspace core | Go workspace layer: canonical root registry, routing, client attribution, lifecycle, and policy; it must not own a concrete provider transport | `headless.go`, `routing.go`, `lifecycle.go`, `client.go`, and `friction.go` pending the S02 seam |
| Provider transport | Go provider backend: Neovim process/socket lifecycle and request transport behind the S02 provider contract | socket/process portions of `headless.go`, `nvim.go`, and platform helpers pending S02 extraction |
| Semantic operations | Neovim/Lua kernel: buffers, Tree-sitter, LSP, DAP, edit application, and semantic evidence | `lua/agent99/**` |

The adapter may translate and dispatch, the workspace core may schedule and hold identity, and
the provider transport may execute provider calls. Dependencies flow from adapters through
the workspace core to a provider contract; no adapter or workspace code may depend directly
on socket commands after S02.

## Compatibility constraints

S01 intentionally does not:

- rename tools, schemas, subcommands, the output binary, or response fields;
- introduce a provider interface, embedded RPC, service lifecycle, workspace IDs, revisions,
  transactions, or new concurrency;
- change the package's standard-library-only dependency set;
- install or deploy any binary.

The S00 contract and result fixtures are therefore expected to remain byte-identical.
