-- Unit checks for install.lua's language-support administration: server
-- prerequisites, Mason package handling and the jdtls sandbox pinning that
-- language_server_setup relies on. Driven by stubs rather than by running
-- anything, so no toolchain is needed.
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

-- The Python interpreter comes from the project's venv, then VIRTUAL_ENV,
-- and pyright is pointed at it before it starts.
do
    local root = vim.fn.tempname()
    vim.fn.mkdir(root .. "/.venv/bin", "p")
    local original_env = vim.env.VIRTUAL_ENV
    vim.env.VIRTUAL_ENV = nil
    check("no interpreter without a venv", install._project_python_interpreter(root) == nil)
    vim.fn.writefile({ "#!/bin/sh" }, root .. "/.venv/bin/python")
    vim.fn.setfperm(root .. "/.venv/bin/python", "rwxr-xr-x")
    check("the project venv interpreter is found",
        install._project_python_interpreter(root) == root .. "/.venv/bin/python")
    local configured = install._configure_python_interpreter(root)
    local settings = (vim.lsp.config.pyright or {}).settings or {}
    check("pyright is pointed at the project interpreter",
        configured == root .. "/.venv/bin/python" and settings.python and settings.python.pythonPath == configured,
        settings)
    local other = vim.fn.tempname()
    vim.fn.mkdir(other .. "/bin", "p")
    vim.fn.writefile({ "#!/bin/sh" }, other .. "/bin/python")
    vim.fn.setfperm(other .. "/bin/python", "rwxr-xr-x")
    vim.env.VIRTUAL_ENV = other
    check("VIRTUAL_ENV is the fallback", install._project_python_interpreter(vim.fn.tempname()) == other .. "/bin/python")
    vim.env.VIRTUAL_ENV = original_env
    vim.fn.delete(root, "rf")
    vim.fn.delete(other, "rf")
end

-- A started client that has not initialized is reported as starting.
do
    local original = vim.lsp.get_clients
    vim.lsp.get_clients = function()
        return { { name = "pyright", initialized = false }, { name = "gopls", initialized = true } }
    end
    check("a starting server is named", install._starting_server({ "pyright" }) == "pyright")
    check("an initialized server is not starting", install._starting_server({ "gopls" }) == nil)
    vim.lsp.get_clients = original
end

do
    local original_exepath, original_expand, original_system = vim.fn.exepath, vim.fn.expand, vim.system
	local original_path, original_gem_home, original_gem_path = vim.env.PATH, vim.env.GEM_HOME, vim.env.GEM_PATH
    local calls = {}
    vim.fn.exepath = function(name) return "/test/bin/" .. name end
    vim.system = function(command)
        calls[#calls + 1] = command
        local code = #calls == 1 and 1 or 0
        return { wait = function() return { code = code, stdout = "", stderr = "" } end }
    end
	vim.fn.expand = function(path)
		if path == "~/.cargo/bin/cargo" then return "/home/test/.cargo/bin/cargo" end
		return original_expand(path)
	end
    local ok, why = install._ensure_ruby_lsp_bundler({
        get_install_path = function() return "/mason/packages/ruby-lsp" end,
    })
	local provider_path = vim.env.PATH
    vim.fn.exepath, vim.fn.expand, vim.system = original_exepath, original_expand, original_system
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

do
    local original_exepath, original_expand, original_system = vim.fn.exepath, vim.fn.expand, vim.system
    vim.fn.exepath = function(name)
        return name == "dotnet" and "/usr/bin/dotnet" or original_exepath(name)
    end
	vim.fn.expand = function(path)
		if path == "~/.cargo/bin/cargo" then return "/home/test/.cargo/bin/cargo" end
		return original_expand(path)
	end
    vim.system = function(command)
        if command[1] == "/usr/bin/dotnet" and command[2] == "--list-sdks" then
            return { wait = function() return { code = 0, stdout = "", stderr = "" } end }
        end
        return original_system(command)
    end
    local ok, why = install._server_prerequisite("csharp-language-server")
    vim.fn.exepath, vim.fn.expand, vim.system = original_exepath, original_expand, original_system
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
		install._support_attach_wait("java", nil, { "jdtls" }) == 15000
            and install._support_attach_wait("java", 1200, { "jdtls" }) == 1200
            and install._support_attach_wait("ruby", nil, { "ruby_lsp" }) == 8000
            and install._support_attach_wait("ruby", 1200, { "ruby_lsp" }) == 1200
            and install._support_attach_wait("rust", nil, { "rust_analyzer" }) == 2500)
end

do
    install._configure_jdtls_sandbox_safety()
    local root = "/tmp/huyang-jdtls-workspace-root"
    install._pin_jdtls_workspace_root(root)
    local markers = vim.lsp.config.jdtls.root_markers or {}
    local source_paths = (((vim.lsp.config.jdtls.settings or {}).java or {}).project or {}).sourcePaths
    check("jdtls recognizes and stays pinned to trusted safe-copy sandbox roots",
        vim.deep_equal(markers[1], { ".huyang.toml", ".huyang/pipeline.json" })
            and vim.deep_equal(source_paths, { "src/main/java", "src/test/java", "src" })
            and vim.lsp.config.jdtls.root_dir == root, {
                markers = markers,
                source_paths = source_paths,
                root_dir = vim.lsp.config.jdtls.root_dir,
            })
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
    local original_exepath, original_expand, original_system = vim.fn.exepath, vim.fn.expand, vim.system
    vim.fn.exepath = function(name)
        if name == "cargo" then return "/home/test/.local/share/mise/shims/cargo" end
        return original_exepath(name)
    end
	vim.fn.expand = function(path)
		if path == "~/.cargo/bin/cargo" then return "/home/test/.cargo/bin/cargo" end
		return original_expand(path)
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
    vim.fn.exepath, vim.fn.expand, vim.system = original_exepath, original_expand, original_system
    check("Rust administration rejects an executable but broken cargo shim actionably",
        ok == nil
            and why:find("/home/test/.local/share/mise/shims/cargo", 1, true) ~= nil
            and why:find("No version is set", 1, true) ~= nil
            and why:find("mise use -g rust@stable", 1, true) ~= nil,
        why)
end

do
	local original_exepath, original_expand, original_system, original_stat = vim.fn.exepath, vim.fn.expand, vim.system, vim.uv.fs_stat
	local original_path = vim.env.PATH
	vim.fn.exepath = function(name)
		if name == "cargo" then return "/home/test/.local/share/mise/shims/cargo" end
		return original_exepath(name)
	end
	vim.fn.expand = function(path)
		if path == "~/.cargo/bin/cargo" then return "/home/test/.cargo/bin/cargo" end
		return original_expand(path)
	end
	vim.uv.fs_stat = function(path)
		if path == "/home/test/.cargo/bin/cargo" or path == "/home/test/.cargo/bin" then return { type = path:match("/bin$") and "directory" or "file" } end
		return original_stat(path)
	end
	vim.system = function(command)
		return { wait = function()
			if command[1] == "/home/test/.cargo/bin/cargo" then return { code = 0, stdout = "cargo 1.0", stderr = "" } end
			return { code = 1, stdout = "", stderr = "mise shim is unset" }
		end }
	end
	local ok = install._server_prerequisite("rust-analyzer")
	vim.fn.exepath, vim.fn.expand, vim.system, vim.uv.fs_stat = original_exepath, original_expand, original_system, original_stat
	check("Rust administration uses a working rustup cargo behind a broken service shim",
		ok == true and vim.env.PATH:sub(1, #"/home/test/.cargo/bin" + 1) == "/home/test/.cargo/bin:", vim.env.PATH)
	vim.env.PATH = original_path
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

-- rust-analyzer's cargo builds go to one on-disk directory per project
-- root, outside /tmp, and an existing CARGO_TARGET_DIR is left alone.
do
    local cargo_target = require("huyang.cargo_target")
    cargo_target._swept = true
    local scratch = vim.fn.tempname()
    local original = { xdg = vim.env.XDG_CACHE_HOME, target = vim.env.CARGO_TARGET_DIR }
    vim.env.XDG_CACHE_HOME = scratch .. "/cache"
    vim.env.CARGO_TARGET_DIR = nil
    local root = scratch .. "/proj"
    vim.fn.mkdir(root, "p")
    vim.fn.writefile({ "[package]" }, root .. "/Cargo.toml")

    local dir = cargo_target.dir_for(root)
    check("the cargo target directory is under XDG_CACHE_HOME",
        vim.startswith(dir, scratch .. "/cache/huyang/cargo-target/proj-"), dir)
    check("the cargo target directory is stable per root",
        cargo_target.dir_for(root) == dir and cargo_target.dir_for(root .. "/") == dir
            and cargo_target.dir_for(scratch .. "/other/proj") ~= dir)
    vim.env.XDG_CACHE_HOME = ""
    local default = cargo_target.dir_for(root)
    check("without XDG_CACHE_HOME the cache is ~/.cache, outside the tempdir",
        vim.startswith(default, vim.env.HOME .. "/.cache/huyang/cargo-target/")
            and not vim.startswith(default, vim.fn.fnamemodify(vim.fn.tempname(), ":h:h")), default)
    vim.env.XDG_CACHE_HOME = scratch .. "/cache"

    local chosen = cargo_target.prepare(root)
    check("prepare points CARGO_TARGET_DIR at the root's directory and creates it",
        chosen == dir and vim.env.CARGO_TARGET_DIR == dir and vim.fn.isdirectory(dir) == 1,
        { chosen = chosen, env = vim.env.CARGO_TARGET_DIR })
    check("prepare again on the same root reuses the directory", cargo_target.prepare(root) == dir)

    vim.env.CARGO_TARGET_DIR = scratch .. "/mine"
    check("an existing CARGO_TARGET_DIR is left alone",
        cargo_target.prepare(root) == nil and vim.env.CARGO_TARGET_DIR == scratch .. "/mine")
    vim.env.CARGO_TARGET_DIR = nil
    check("a root without Cargo.toml gets no target directory",
        cargo_target.prepare(scratch) == nil and vim.env.CARGO_TARGET_DIR == nil)

    vim.env.XDG_CACHE_HOME = original.xdg
    vim.env.CARGO_TARGET_DIR = original.target
    vim.fn.delete(scratch, "rf")
end

-- Per-root cargo directories not used for the configured number of days
-- are pruned; a recently used one and the current root's are kept.
do
    local cargo_target = require("huyang.cargo_target")
    local base = vim.fn.tempname()
    local now = os.time()
    local function make(name, age_days, stamped)
        local dir = base .. "/" .. name
        vim.fn.mkdir(dir .. "/debug", "p")
        local t = now - age_days * 86400
        if stamped then
            vim.fn.writefile({}, dir .. "/.huyang-last-used")
            vim.uv.fs_utime(dir .. "/.huyang-last-used", t, t)
            vim.uv.fs_utime(dir, now, now)
        else
            vim.uv.fs_utime(dir, t, t)
        end
        return dir
    end
    local stale = make("stale-aaaa", 40, true)
    local fresh = make("fresh-bbbb", 2, true)
    local unstamped = make("unstamped-cccc", 45, false)
    local current = make("current-dddd", 90, true)
    local removed = {}
    local function remove(path)
        removed[#removed + 1] = path
        vim.fn.delete(path, "rf")
    end
    cargo_target.prune({ base = base, now = now, max_age_days = 30, keep = current, remove = remove })
    table.sort(removed)
    check("stale per-root caches are pruned by the stamp's age, falling back to the directory's",
        vim.deep_equal(removed, { stale, unstamped }) and vim.fn.isdirectory(fresh) == 1
            and vim.fn.isdirectory(current) == 1, removed)
    removed = {}
    cargo_target.prune({ base = base, now = now, max_age_days = 1, remove = remove })
    check("the age limit is a setting", vim.deep_equal(removed, { current, fresh }) or vim.deep_equal(removed, { fresh, current }), removed)
    local original = vim.env.HUYANG_CARGO_TARGET_MAX_AGE_DAYS
    vim.env.HUYANG_CARGO_TARGET_MAX_AGE_DAYS = "7"
    local seven = cargo_target.max_age_days()
    vim.env.HUYANG_CARGO_TARGET_MAX_AGE_DAYS = "nonsense"
    local fallback = cargo_target.max_age_days()
    vim.env.HUYANG_CARGO_TARGET_MAX_AGE_DAYS = original
    check("HUYANG_CARGO_TARGET_MAX_AGE_DAYS sets the age, 30 otherwise", seven == 7 and fallback == 30,
        { seven, fallback })
    check("pruning a missing cache directory is a no-op",
        #cargo_target.prune({ base = base .. "/absent", remove = remove }) == 0)
    vim.fn.delete(base, "rf")
end

-- The sweep removes the cargo directory an earlier version left in a dead
-- Neovim's tempdir and keeps a live Neovim's and our own.
do
    local cargo_target = require("huyang.cargo_target")
    local scratch = vim.fn.tempname()
    local base = scratch .. "/nvim.test"
    local orphan = base .. "/DEAD01/0-huyang-cargo-target"
    local live = base .. "/LIVE01/3-huyang-cargo-target"
    local own = base .. "/OWN001"
    local mine = own .. "/0-huyang-cargo-target"
    local unrelated = base .. "/DEAD01/5"
    for _, dir in ipairs({ orphan .. "/debug", live .. "/debug", mine, unrelated }) do vim.fn.mkdir(dir, "p") end
    local asked = {}
    local function is_live(name)
        asked[name] = true
        return name == "LIVE01"
    end
    local removed = {}
    local function remove(path)
        removed[#removed + 1] = path
        vim.fn.delete(path, "rf")
    end
    cargo_target.sweep({ base = base, own = own, is_live = is_live, remove = remove })
    check("the sweep removes the orphan and keeps the live Neovim's and our own",
        vim.deep_equal(removed, { orphan }) and vim.fn.isdirectory(live) == 1 and vim.fn.isdirectory(mine) == 1
            and vim.fn.isdirectory(unrelated) == 1 and not asked.OWN001, { removed = removed, asked = asked })

    removed = {}
    local other = scratch .. "/not-nvim"
    vim.fn.mkdir(other .. "/X/0-huyang-cargo-target", "p")
    cargo_target.sweep({ base = other, own = other .. "/Y", is_live = function() return false end, remove = remove })
    check("the sweep only works in a Neovim temp root", #removed == 0, removed)

    -- The real /proc check: a process whose working directory is inside a
    -- tempdir keeps that tempdir's cargo directory.
    removed = {}
    vim.fn.delete(base .. "/LIVE01", "rf")
    local held = base .. "/HELD01/0-huyang-cargo-target"
    vim.fn.mkdir(held, "p")
    local sleeper = vim.system({ "sleep", "30" }, { cwd = base .. "/HELD01" })
    vim.fn.mkdir(orphan, "p")
    cargo_target.sweep({ base = base, own = own, remove = remove })
    sleeper:kill(9)
    check("the /proc check keeps a tempdir a live process holds",
        vim.deep_equal(removed, { orphan }) and vim.fn.isdirectory(held) == 1, removed)
    vim.fn.delete(scratch, "rf")
end

if failures > 0 then
    io.stdout:write(("unit_check: %d failed\n"):format(failures))
    vim.cmd("cquit 1")
end
io.stdout:write("unit_check: OK\n")
io.stdout:flush()
vim.cmd("quit")
