local here = debug.getinfo(1, "S").source:sub(2)
local tests = vim.fn.fnamemodify(here, ":h")
dofile(tests .. "/minimal_init.lua")

if vim.fn.executable("typescript-language-server") == 1 then
    vim.lsp.config("ts_ls", {
        cmd = { "typescript-language-server", "--stdio" },
        filetypes = { "javascript", "javascriptreact", "typescript", "typescriptreact" },
        root_markers = { "tsconfig.json", "package.json", ".git" },
    })
    vim.lsp.enable("ts_ls")
end

if vim.fn.executable("bash-language-server") == 1 then
    vim.lsp.config("bashls", {
        cmd = { "bash-language-server", "start" },
        filetypes = { "sh" },
        root_markers = { ".git" },
    })
    vim.lsp.enable("bashls")
end
