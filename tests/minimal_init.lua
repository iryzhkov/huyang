-- Minimal Neovim config for the test harness: no user config, just the
-- Huyang runtime on the runtimepath and lua_ls enabled through the native
-- vim.lsp.config API (no lspconfig needed).

local here = debug.getinfo(1, "S").source:sub(2)
local repo = vim.fn.fnamemodify(here, ":h:h")
vim.opt.rtp:prepend(repo)

-- nvim-dap is a runtime dependency, not part of Huyang. The tests resolve it
-- from HUYANG_NVIM_DAP_PATH (authoritative when set, even when it holds
-- nothing), then the pinned clone tests/fetch-nvim-dap.sh makes under
-- tests/.deps, then the lazy.nvim clone of the user's Neovim. When none
-- exists the debugger suites skip; nothing here fails.
local function has_dap(path)
    return path ~= nil and path ~= "" and vim.fn.filereadable(path .. "/lua/dap.lua") == 1
end
local dap_candidates
local override = os.getenv("HUYANG_NVIM_DAP_PATH")
if override and override ~= "" then
    dap_candidates = { override }
else
    dap_candidates = {
        repo .. "/tests/.deps/nvim-dap",
        vim.fn.stdpath("data") .. "/lazy/nvim-dap",
    }
end
for _, candidate in ipairs(dap_candidates) do
    if has_dap(candidate) then
        vim.opt.rtp:prepend(candidate)
        vim.g.huyang_test_nvim_dap = candidate
        break
    end
end

-- --clean drops the data site directory, where nvim-treesitter installs its
-- parsers; put it back so the data-file tests see the machine's yaml and
-- toml parsers (they skip cleanly when the directory has none).
local site = vim.fn.stdpath("data") .. "/site"
if vim.fn.isdirectory(site .. "/parser") == 1 then
    vim.opt.rtp:append(site)
end

-- Headless test instances must never fight over swap files or shada.
vim.opt.swapfile = false
vim.opt.shadafile = "NONE"

vim.lsp.config("lua_ls", {
    cmd = { "lua-language-server" },
    filetypes = { "lua" },
    root_markers = { ".luarc.json", ".git" },
})
vim.lsp.enable("lua_ls")

-- pyright when present. A Python server is what makes the "a server reports
-- a variable by the range of its name alone" case testable: without one the
-- suite cannot see a multi-line constant come back as a one-line symbol,
-- which is how that bug shipped.
if vim.fn.executable("pyright-langserver") == 1 then
    vim.lsp.config("pyright", {
        cmd = { "pyright-langserver", "--stdio" },
        filetypes = { "python" },
        root_markers = { "pyproject.toml", "setup.py", "requirements.txt", ".git" },
        settings = { python = {} },
    })
    vim.lsp.enable("pyright")
end

-- gopls when present, so the debugger tests get symbol annotation on Go.
if vim.fn.executable("gopls") == 1 then
    vim.lsp.config("gopls", {
        cmd = { "gopls" },
        filetypes = { "go", "gomod" },
        root_markers = { "go.mod", ".git" },
    })
    vim.lsp.enable("gopls")
end
