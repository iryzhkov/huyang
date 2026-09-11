-- Unit checks for the commands check_project guesses from a project's
-- files. Driven by scratch directories rather than by running anything: the
-- point is which command is chosen, and running it would need the toolchain
-- of every language the guess covers.
--
-- Run with: nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_check.lua

local install = require("huyang.install")

local failures = 0

local function check(name, ok, detail)
    if ok then
        io.stdout:write("ok   " .. name .. "\n")
    else
        failures = failures + 1
        io.stdout:write("FAIL " .. name .. (detail and ("\n     " .. vim.inspect(detail)) or "") .. "\n")
    end
    io.stdout:flush()
end

local function scratch(files)
    local root = vim.fn.tempname()
    vim.fn.mkdir(root, "p")
    for name, contents in pairs(files) do
        local dir = vim.fn.fnamemodify(root .. "/" .. name, ":h")
        vim.fn.mkdir(dir, "p")
        if contents ~= false then
            vim.fn.writefile(vim.split(contents, "\n", { plain = true }), root .. "/" .. name)
        end
    end
    return root
end

local function commands(root)
    local out = {}
    for _, g in ipairs(install.guess_check_command(root)) do
        out[#out + 1] = g.cmd
    end
    return out
end

local function any(list, text)
    for _, cmd in ipairs(list) do
        if cmd:find(text, 1, true) then return true end
    end
    return false
end

do
    local original_exepath, original_system = vim.fn.exepath, vim.system
	local original_path, original_gem_home, original_gem_path = vim.env.PATH, vim.env.GEM_HOME, vim.env.GEM_PATH
    local calls = {}
    vim.fn.exepath = function(name) return "/test/bin/" .. name end
    vim.system = function(command)
        calls[#calls + 1] = command
        local code = #calls == 1 and 1 or 0
        return { wait = function() return { code = code, stdout = "", stderr = "" } end }
    end
    local ok, why = install._ensure_ruby_lsp_bundler({
        get_install_path = function() return "/mason/packages/ruby-lsp" end,
    })
	local provider_path = vim.env.PATH
    vim.fn.exepath, vim.system = original_exepath, original_system
	vim.env.PATH, vim.env.GEM_HOME, vim.env.GEM_PATH = original_path, original_gem_home, original_gem_path
    check("ruby-lsp installs a missing Bundler inside its Mason package",
        ok == true and why == nil and #calls == 3
            and calls[2][1] == "/test/bin/gem"
            and calls[2][5] == "/mason/packages/ruby-lsp"
            and calls[2][6] == "bundler"
            and provider_path:find("/mason/packages/ruby-lsp/bin", 1, true) == 1
            and vim.lsp.config.ruby_lsp.cmd_env.GEM_HOME == "/mason/packages/ruby-lsp"
            and vim.lsp.config.ruby_lsp.cmd_env.PATH:find("/mason/packages/ruby-lsp/bin", 1, true) == 1,
        { calls = calls, cmd_env = vim.lsp.config.ruby_lsp.cmd_env })
end

-- The configuration this Neovim actually loads is checked by starting
-- Neovim on it: its real breakage is load-time, and no static check sees it.
local this_config = vim.fn.stdpath("config")
if vim.fn.executable("nvim") == 0 or vim.uv.fs_stat(this_config .. "/init.lua") == nil then
    io.stdout:write("skip  no init.lua at " .. this_config .. "\n")
else
    local own = commands(this_config)
    check("the loaded configuration is checked by starting Neovim",
        any(own, "nvim --headless") and any(own, "messages") and not any(own, "-u init.lua"), own)
    check("it still gets the Lua syntax check",
        any(own, "luac") or any(own, "luacheck"), own)
end

-- A configuration-shaped tree that is NOT the one Neovim loads: starting
-- `nvim -u <root>/init.lua` there sources that init, but every require and
-- every plugin/after script still resolves out of stdpath("config"), so the
-- check would run the machine's own configuration and call a broken copy
-- clean. No nvim command is guessed for it.
local copy_root = scratch({
    ["init.lua"] = 'require("user.options")',
    ["lua/user/options.lua"] = "return {}",
    ["after/plugin/theme.lua"] = 'vim.cmd("colorscheme default")',
    ["lazy-lock.json"] = "{}",
})
local copy_cmds = commands(copy_root)
check("a configuration copy is not started as a config", not any(copy_cmds, "nvim --headless"), copy_cmds)
check("a configuration copy still gets the Lua syntax check",
    any(copy_cmds, "luac") or any(copy_cmds, "luacheck"), copy_cmds)
vim.fn.delete(copy_root, "rf")

-- A few vendored files of another language are not that language's project.
-- A TypeScript monorepo carrying seven Python fixtures was answered with
-- `pyright`, which passed in 0.2s and called 13k unchecked .ts files green.
-- The TypeScript side has to be big enough for seven files to be the
-- handful this is about: the guess is a share of the tree, and 7 of 209
-- is 3.3%, over the threshold. This tree used to pass for the wrong
-- reason - project_files walked with globpath, which skips a leading dot,
-- so the seven files under .repos/ were not counted at all and any share
-- would have done.
local mono = { ["tsconfig.base.json"] = "{}", ["package.json"] = "{}" }
for i = 1, 700 do
    mono[("apps/web/src/mod%d.ts"):format(i)] = "export const x = 1"
end
for i = 1, 7 do
    mono[(".repos/vendor/fixture%d.py"):format(i)] = "x = 1"
end
local mono_root = scratch(mono)
local mono_cmds = commands(mono_root)
check("a handful of vendored .py files is not a Python project",
    not any(mono_cmds, "pyright") and not any(mono_cmds, "mypy"), mono_cmds)
vim.fn.delete(mono_root, "rf")

-- The same files, without the TypeScript around them: one module and its
-- test still has no pyproject.toml and is still a Python project.
local py_root = scratch({
    ["app.py"] = "def run():\n    return 1\n",
    ["test_app.py"] = "from app import run\n",
})
local py_cmds = commands(py_root)
check("a small Python project is still checked",
    vim.fn.executable("pyright") == 0 and vim.fn.executable("mypy") == 0
    or any(py_cmds, "pyright") or any(py_cmds, "mypy"), py_cmds)
vim.fn.delete(py_root, "rf")

-- A Lua library with an init.lua of its own is not a configuration either.
local lib_root = scratch({
    ["init.lua"] = "return require('lib.core')",
    ["lua/lib/core.lua"] = "return {}",
})
local lib_cmds = commands(lib_root)
check("a plain Lua project is not started as a config",
    not any(lib_cmds, "nvim --headless"), lib_cmds)
vim.fn.delete(lib_root, "rf")

do
    local original_exepath, original_system = vim.fn.exepath, vim.system
    vim.fn.exepath = function(name)
        return name == "dotnet" and "/usr/bin/dotnet" or original_exepath(name)
    end
    vim.system = function(command)
        if command[1] == "/usr/bin/dotnet" and command[2] == "--list-sdks" then
            return { wait = function() return { code = 0, stdout = "", stderr = "" } end }
        end
        return original_system(command)
    end
    local ok, why = install._server_prerequisite("csharp-language-server")
    vim.fn.exepath, vim.system = original_exepath, original_system
    check("C# administration distinguishes a runtime-only dotnet install from an SDK",
        ok == nil and why:find("only the runtime is available", 1, true) ~= nil, why)
end

do
    local old_registry, old_mlsp = package.loaded["mason-registry"], package.loaded["mason-lspconfig"]
    local original_enable = vim.lsp.enable
    local enabled = {}
	vim.lsp.config("jdtls", { cmd = { "jdtls" }, filetypes = { "java" } })
    package.loaded["mason-registry"] = {
        get_package = function(name)
            return { is_installed = function() return name == "jdtls" end }
        end,
		get_installed_packages = function() return { { name = "jdtls" } } end,
    }
    package.loaded["mason-lspconfig"] = {
        get_mappings = function()
			return {
				lspconfig_to_package = { jdtls = "jdtls" },
				package_to_lspconfig = { jdtls = "jdtls" },
			}
		end,
        -- Fresh mason-lspconfig discovery can omit a configured server until
        -- it is enabled; the persisted installed package must still win.
        get_available_servers = function() return {} end,
    }
    vim.lsp.enable = function(name) enabled[#enabled + 1] = name end
    local restored = install._enable_installed_servers("java")
    vim.lsp.enable = original_enable
    package.loaded["mason-registry"], package.loaded["mason-lspconfig"] = old_registry, old_mlsp
    check("installed language servers are re-enabled after provider restart",
        #restored == 1 and restored[1] == "jdtls" and enabled[1] == "jdtls", restored)
end

do
    check("restored jdtls gets a bounded cold-start readiness window",
        install._support_attach_wait("java", nil, { "jdtls" }) == 6000
            and install._support_attach_wait("java", 1200, { "jdtls" }) == 1200
            and install._support_attach_wait("ruby", nil, { "ruby_lsp" }) == 8000
            and install._support_attach_wait("ruby", 1200, { "ruby_lsp" }) == 1200
            and install._support_attach_wait("rust", nil, { "rust_analyzer" }) == 2500)
end

do
    install._configure_jdtls_sandbox_safety()
    local markers = vim.lsp.config.jdtls.root_markers or {}
    check("jdtls recognizes trusted safe-copy sandbox roots",
        vim.deep_equal(markers[1], { ".huyang.toml", ".huyang/pipeline.json" }), markers)
end

do
    local old_registry, old_mlsp = package.loaded["mason-registry"], package.loaded["mason-lspconfig"]
    local original_enable, original_exepath, original_system = vim.lsp.enable, vim.fn.exepath, vim.system
    local original_path, original_gem_home, original_gem_path = vim.env.PATH, vim.env.GEM_HOME, vim.env.GEM_PATH
    local enabled = {}
    local pkg = {
        name = "ruby-lsp",
        get_install_path = function() return "/mason/packages/ruby-lsp" end,
        is_installed = function() return true end,
    }
    vim.lsp.config("ruby_lsp", { cmd = { "ruby-lsp" }, filetypes = { "ruby" } })
    package.loaded["mason-registry"] = {
        get_package = function() return pkg end,
        get_installed_packages = function() return { pkg } end,
    }
    package.loaded["mason-lspconfig"] = {
        get_mappings = function()
            return {
                lspconfig_to_package = { ruby_lsp = "ruby-lsp" },
                package_to_lspconfig = { ["ruby-lsp"] = "ruby_lsp" },
            }
        end,
        get_available_servers = function() return {} end,
    }
    vim.fn.exepath = function(name) return "/test/bin/" .. name end
    vim.system = function()
        return { wait = function() return { code = 0, stdout = "", stderr = "" } end }
    end
    vim.lsp.enable = function(name) enabled[#enabled + 1] = name end
    local restored = install._enable_installed_servers("ruby")
    local configured = vim.lsp.config.ruby_lsp.cmd_env or {}
    vim.lsp.enable, vim.fn.exepath, vim.system = original_enable, original_exepath, original_system
    vim.env.PATH, vim.env.GEM_HOME, vim.env.GEM_PATH = original_path, original_gem_home, original_gem_path
    package.loaded["mason-registry"], package.loaded["mason-lspconfig"] = old_registry, old_mlsp
    check("ruby-lsp runtime dependencies are restored after provider restart",
        restored[1] == "ruby_lsp" and enabled[1] == "ruby_lsp"
            and configured.GEM_HOME == "/mason/packages/ruby-lsp",
        { restored = restored, enabled = enabled, cmd_env = configured })
end


do
    local original_path = vim.env.PATH
    local root = vim.fn.tempname()
    local mason = root .. "/mason/bin"
    local broken = root .. "/broken/mise/shims"
    vim.fn.mkdir(mason, "p")
    vim.fn.mkdir(broken, "p")
    vim.fn.writefile({ "#!/bin/sh", "exit 0" }, mason .. "/rust-analyzer")
    vim.fn.writefile({ "#!/bin/sh", "exit 1" }, broken .. "/rust-analyzer")
    vim.fn.setfperm(mason .. "/rust-analyzer", "rwxr-xr-x")
    vim.fn.setfperm(broken .. "/rust-analyzer", "rwxr-xr-x")
    vim.env.PATH = broken .. ":/usr/bin"
    local before = vim.fn.exepath("rust-analyzer")
    local selected = install._ensure_mason_bin_on_path(mason)
    local after = vim.fn.exepath("rust-analyzer")
    local once = vim.env.PATH
    install._ensure_mason_bin_on_path(mason)
    local count = 0
    for _, entry in ipairs(vim.split(vim.env.PATH, ":", { plain = true })) do
        if entry == mason then count = count + 1 end
    end
    check("Mason launchers precede same-named version-manager shims",
        selected == mason and before == broken .. "/rust-analyzer"
            and after == mason .. "/rust-analyzer"
            and once:sub(1, #mason + 1) == mason .. ":"
            and vim.env.PATH == once and count == 1,
        { selected = selected, path = vim.env.PATH })
    vim.env.PATH = original_path
    vim.fn.delete(root, "rf")
end

do
    local original_exepath, original_system = vim.fn.exepath, vim.system
    vim.fn.exepath = function(name)
        if name == "cargo" then return "/home/test/.local/share/mise/shims/cargo" end
        return original_exepath(name)
    end
    vim.system = function(command)
        if command[1] == "/home/test/.local/share/mise/shims/cargo" and command[2] == "--version" then
            return {
                wait = function()
                    return { code = 1, stdout = "", stderr = "mise ERROR No version is set for shim: cargo" }
                end,
            }
        end
        return original_system(command)
    end
    local ok, why = install._server_prerequisite("rust-analyzer")
    vim.fn.exepath, vim.system = original_exepath, original_system
    check("Rust administration rejects an executable but broken cargo shim actionably",
        ok == nil
            and why:find("/home/test/.local/share/mise/shims/cargo", 1, true) ~= nil
            and why:find("No version is set", 1, true) ~= nil
            and why:find("mise use -g rust@stable", 1, true) ~= nil,
        why)
end

do
    local root = vim.fn.tempname()
    vim.fn.mkdir(root, "p")
    local sample = root .. "/lib.rs"
    vim.fn.writefile({ "pub fn reserve() {}" }, sample)
    local bufnr = vim.fn.bufadd(sample)
    vim.fn.bufload(bufnr)
    vim.bo[bufnr].filetype = "rust"

    local fired = false
    local group = vim.api.nvim_create_augroup("huyang_unit_rust_retry", { clear = true })
    vim.api.nvim_create_autocmd("FileType", {
        group = group,
        buffer = bufnr,
        callback = function() fired = true end,
    })
    local original_clients = vim.lsp.get_clients
    vim.lsp.get_clients = function(opts)
        if fired and opts and opts.bufnr == bufnr then
            return { { name = "rust-analyzer-test" } }
        end
        return {}
    end
    local client, why = install._attached_client(root, "rust")
    vim.lsp.get_clients = original_clients
    vim.api.nvim_del_augroup_by_id(group)
    vim.fn.delete(root, "rf")
    check("already-loaded samples replay FileType so a newly enabled server attaches",
        client == "rust-analyzer-test", why)
end

if failures > 0 then
    io.stdout:write(("unit_check: %d failed\n"):format(failures))
    vim.cmd("cquit 1")
end
io.stdout:write("unit_check: OK\n")
io.stdout:flush()
vim.cmd("quit")
