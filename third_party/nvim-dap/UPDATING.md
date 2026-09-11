# Refreshing the vendored nvim-dap

Huyang ships the nvim-dap Lua runtime inside its own runtimepath so the debugger tools
work without a plugin manager. The copy lives in three places that must move together:

- `lua/dap.lua` and `lua/dap/` — the library modules
- `plugin/dap.lua` — the user commands nvim-dap registers at startup
- `third_party/nvim-dap/LICENSE.txt` — the upstream license

`third_party/nvim-dap/README.md` records the upstream commit the copy was taken from.
`.luarc.json` lists `lua/dap` as a library so editor diagnostics come from the shipped
code, not from a developer checkout under `tests/.deps`.

To advance the dependency:

1. Clone or fetch upstream and check out the commit you want:
   `git clone https://github.com/mfussenegger/nvim-dap /tmp/nvim-dap && git -C /tmp/nvim-dap checkout <commit>`.
2. Replace the shipped files with the upstream ones:
   `rm -r lua/dap lua/dap.lua plugin/dap.lua && cp -r /tmp/nvim-dap/lua/dap /tmp/nvim-dap/lua/dap.lua lua/ && cp /tmp/nvim-dap/plugin/dap.lua plugin/`.
3. Copy the upstream license over `third_party/nvim-dap/LICENSE.txt` if it changed.
4. Update the commit hash in `third_party/nvim-dap/README.md`.
5. Run `make smoke`; `tests/unit_dap.lua` loads the shipped runtime through
   `tests/minimal_init.lua`, and the Go debugger tests in `internal/provider/embed` and
   `internal/bridge` start it with `Debug: true`.

Huyang carries no local patches to nvim-dap. If one ever becomes necessary, record it in
this file so the next refresh reapplies it.
