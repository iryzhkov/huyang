-- Unit checks for install.lua's language-support probe: Mason package
-- handling, runtime prerequisites of installed servers and the jdtls sandbox
-- pinning that workspace_support and a provider restart rely on. Driven by
-- stubs rather than by running anything, so no toolchain is needed.
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

-- Discovery comes from Go's shared pipeline selector. User settings remain
-- authoritative, and no Lua-side environment commands are permitted.
do
    local python = require("huyang.python_environment")
    local original_config, original_system = vim.lsp.config, vim.system
    local config = setmetatable({
        pyright = { settings = { python = { pythonPath = "/user/python" }, pyright = { disableOrganizeImports = true } } },
        basedpyright = { settings = { basedpyright = { analysis = { typeCheckingMode = "basic" } } } },
    }, { __call = function(self, name, patch)
        self[name] = vim.tbl_deep_extend("force", self[name] or {}, patch)
    end })
    vim.lsp.config = config
    vim.system = function() error("Lua discovery must never execute a process") end
    python.configure({ interpreter = "/project/.venv/bin/python", source = ".venv" })
    check("an explicit pythonPath wins", config.pyright.settings.python.pythonPath == "/user/python")
    check("basedpyright gets the detected interpreter",
        config.basedpyright.settings.python.pythonPath == "/project/.venv/bin/python")
    check("other settings survive interpreter configuration",
        config.pyright.settings.pyright.disableOrganizeImports
            and config.basedpyright.settings.basedpyright.analysis.typeCheckingMode == "basic")
    config.basedpyright.settings.python.pythonPath = "/later/user/python"
    python.configure({ interpreter = "/other/python" })
    check("a later user setting also wins", config.basedpyright.settings.python.pythonPath == "/later/user/python")
    python.configure({ configured = true, source = "project_config" })
    check("project configuration does not force an automatic interpreter", config.pyright.settings.python.pythonPath == "/user/python")
    vim.lsp.config = setmetatable({}, { __call = function() error("configuration refused") end })
    python.configure({ interpreter = "/project/python" })
    check("configuration failure remains explicit", python.reason() == "interpreter_configuration_failed")
    python.configure({})
    vim.lsp.config, vim.system = original_config, original_system
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
    check("the stamp records the project root",
        vim.fn.readfile(dir .. "/.huyang-last-used")[1] == vim.fs.normalize(root))
    local sandbox = scratch .. "/sandbox-1234/tree"
    vim.fn.mkdir(sandbox, "p")
    vim.fn.writefile({ "[package]" }, sandbox .. "/Cargo.toml")
    check("a plan sandbox shares its source root's directory",
        cargo_target.prepare(sandbox, root) == dir and vim.env.CARGO_TARGET_DIR == dir)

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
    removed = {}
    local project = base .. "/project"
    vim.fn.mkdir(project, "p")
    local kept = make("kept-eeee", 0, true)
    vim.fn.writefile({ project }, kept .. "/.huyang-last-used")
    local gone = make("gone-ffff", 0, true)
    vim.fn.writefile({ base .. "/deleted-attempt/workspace" }, gone .. "/.huyang-last-used")
    cargo_target.prune({ base = base, now = now, max_age_days = 30, remove = remove })
    check("a cache whose project root is gone is pruned at once; a fresh one whose root exists is kept",
        vim.deep_equal(removed, { gone }) and vim.fn.isdirectory(kept) == 1, removed)
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

-- The support probe starts servers only for a project's significant
-- languages. The counts are the ones observed on this project and on a Rust
-- project whose two JavaScript files used to cost a ts_ls per provider.
do
    local function names(set)
        local list = vim.tbl_keys(set)
        table.sort(list)
        return list
    end
    local huyang = { go = 355, json = 236, markdown = 139, lua = 30, python = 15, typescript = 12, sh = 11, gomod = 13 }
    check("a Go project starts only its Go server",
        vim.deep_equal(names(install._eager_filetypes(huyang)), { "go" }), names(install._eager_filetypes(huyang)))
    local carpc = { rust = 104, toml = 19, markdown = 9, wgsl = 4, html = 2, javascript = 2, css = 2 }
    check("two JavaScript files in a Rust project start no ts_ls",
        vim.deep_equal(names(install._eager_filetypes(carpc)), { "rust" }), names(install._eager_filetypes(carpc)))
    local mixed = { go = 50, typescript = 20, python = 5, json = 400 }
    check("a fifth of the source files is significant, data files do not dilute it",
        vim.deep_equal(names(install._eager_filetypes(mixed)), { "go", "typescript" }), names(install._eager_filetypes(mixed)))
    local monorepo = { go = 2000, typescript = 150, python = 40 }
    check("a hundred files are significant whatever their share",
        vim.deep_equal(names(install._eager_filetypes(monorepo)), { "go", "typescript" }), names(install._eager_filetypes(monorepo)))
    check("a tree of data files starts the server of its largest filetype",
        vim.deep_equal(names(install._eager_filetypes({ yaml = 10, json = 3 })), { "yaml" }))
    check("a requested language is started however small",
        vim.deep_equal(names(install._eager_filetypes(huyang, { python = true })), { "go", "python" }))
    local requested = install._requested_filetypes({ languages = { "lua", "" }, files = { "bench/x.py", "notes.nosuchext" } })
    check("requests name languages directly or through files",
        vim.deep_equal(names(requested), { "lua", "python" }), names(requested))
end

-- A server counts as running for a language once any buffer started it, and
-- only a configured server with a runnable command can start on first use.
do
    local original = vim.lsp.get_clients
    vim.lsp.get_clients = function()
        return { { name = "ts_ls", initialized = true }, { name = "pyright", initialized = false },
            { name = "ts_ls", initialized = true } }
    end
    check("an initialized server is running once",
        vim.deep_equal(install._running_clients({ "ts_ls", "pyright" }), { "ts_ls" }))
    vim.lsp.get_clients = original
    vim.lsp.config("huyang_unit_runnable", { cmd = { "sh" }, filetypes = { "huyangunit" } })
    vim.lsp.config("huyang_unit_missing", { cmd = { "huyang-no-such-server" }, filetypes = { "huyangunit" } })
    check("a server on PATH can start", install._any_startable({ "huyang_unit_missing", "huyang_unit_runnable" }))
    check("a server off PATH cannot", not install._any_startable({ "huyang_unit_missing" }))
end

-- End to end: an incidental Lua file is reported on_demand and starts no
-- lua_ls, and naming the language starts it. COBOL leads the tree because
-- nothing on this machine serves it, so the probe has no server to start.
do
    local root = vim.fn.tempname()
    vim.fn.mkdir(root, "p")
    for i = 1, 9 do
        vim.fn.writefile({ "       IDENTIFICATION DIVISION." }, ("%s/p%d.cob"):format(root, i))
    end
    vim.fn.writefile({ "return {}" }, root .. "/tool.lua")
    local function probe(args)
        local result, done
        local co = coroutine.create(function()
            result = install.workspace_support(args)
            done = true
        end)
        local ok, e = coroutine.resume(co)
        if not ok then error(e) end
        vim.wait(20000, function() return done end, 50)
        return result
    end
    local function lua_entry(report)
        for _, entry in ipairs(report and report.languages or {}) do
            if entry.filetype == "lua" then return entry end
        end
    end
    local entry = lua_entry(probe({ root = root, attach_wait_ms = 250 }))
    if vim.fn.executable("lua-language-server") == 1 then
        check("an incidental language is reported on demand",
            entry and entry.attach == "on_demand" and vim.deep_equal(entry.configured, { "lua_ls" }), entry)
        check("and its server is not started", #vim.lsp.get_clients({ name = "lua_ls" }) == 0)
        entry = lua_entry(probe({ root = root, languages = { "lua" }, attach_wait_ms = 10000 }))
        check("naming the language starts its server",
            entry and (entry.attach == "attached" or entry.attach == "starting")
            and #vim.lsp.get_clients({ name = "lua_ls" }) > 0, entry)
        entry = lua_entry(probe({ root = root, attach_wait_ms = 250 }))
        check("a started server is reported attached, not on demand",
            entry and entry.attach ~= "on_demand", entry)
        for _, client in ipairs(vim.lsp.get_clients({ name = "lua_ls" })) do client:stop(true) end
    else
        check("without lua-language-server the language is not promised",
            entry and entry.attach ~= "on_demand", entry)
    end
    vim.fn.delete(root, "rf")
end

if failures > 0 then
    io.stdout:write(("unit_check: %d failed\n"):format(failures))
    vim.cmd("cquit 1")
end
io.stdout:write("unit_check: OK\n")
io.stdout:flush()
vim.cmd("quit")
