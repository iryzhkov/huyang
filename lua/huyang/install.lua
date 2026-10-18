-- What a workspace can and cannot do: the language support probe
-- workspace_open and language_server_status report. Huyang installs no
-- parsers or servers; they come from the user's Neovim setup (Mason,
-- nvim-treesitter) or the system, and the probe enables what it finds.

local M = {}

local core = require("huyang.core")
local err, sleep, load_buf = core.err, core.sleep, core.load_buf
local project_files, better_sample, has_parser = core.project_files, core.better_sample, core.has_parser
local enabled_lsp_configs_for, DATA_FILETYPES = core.enabled_lsp_configs_for, core.DATA_FILETYPES


-- Internal: what the editor can do for the languages in a workspace, so a
-- client that opened a headless instance learns up front when a language
-- has no parser (skim/find_symbol/ts_query blind) or no language server
-- (definition/references/hover/diagnostics blind). Counts files per
-- filetype from the git index, checks the parser instantly, and for the
-- significant filetypes (below) that have an enabled LSP config loads one
-- sample file and waits briefly for a client to attach (the binary may be
-- missing).
local SUPPORT_MAX_FILETYPES = 10

-- Which languages the probe starts a server for. Every provider used to
-- load a sample of every configured filetype, so a Go repository ran gopls
-- beside lua_ls, pyright, ts_ls and bashls for a handful of scripts, and a
-- Rust project ran ts_ls for two JavaScript files: about a gigabyte per
-- provider, most of it for languages nobody touched. The probe now starts a
-- server only for the project's significant languages: the largest source
-- filetype, any filetype holding at least EAGER_MIN_SHARE of the source
-- files, and any with at least EAGER_MIN_FILES files, a real part of a
-- large tree whatever its share. A fifth is the point below which a
-- language reads as tooling beside the project rather than a part of it
-- (huyang's Lua kernel is 7% of its source, e38-carpc's JavaScript 2%).
-- The other languages stay enabled, so the first tool call that loads one of
-- their files starts the server exactly as a probe would have, and the report
-- calls them on_demand. Data filetypes (JSON, YAML, Markdown) do not count
-- toward the shares, and only lead when the tree has no source at all.
local EAGER_MIN_SHARE = 0.2
local EAGER_MIN_FILES = 100

-- The filetypes whose servers the probe starts, as a set. by_ft counts files
-- per filetype; requested names filetypes a caller asked for explicitly (the
-- languages of a status request, the files a plan is about to diagnose),
-- which are always started.
local function eager_filetypes(by_ft, requested)
    local eager, source_total, top, top_any = {}, 0, nil, nil
    for ft, count in pairs(by_ft) do
        if not DATA_FILETYPES[ft] then
            source_total = source_total + count
            if not top or count > by_ft[top] or (count == by_ft[top] and ft < top) then top = ft end
        end
        if not top_any or count > by_ft[top_any] or (count == by_ft[top_any] and ft < top_any) then
            top_any = ft
        end
    end
    if top then
        eager[top] = true
    elseif top_any then
        eager[top_any] = true
    end
    for ft, count in pairs(by_ft) do
        if not DATA_FILETYPES[ft]
            and (count >= EAGER_MIN_FILES or count >= EAGER_MIN_SHARE * source_total) then
            eager[ft] = true
        end
    end
    for ft in pairs(requested or {}) do
        eager[ft] = true
    end
    return eager
end

local SUPPORT_ATTACH_MS = 2500
local JAVA_RESTART_ATTACH_MS = 15000
local RUBY_RESTART_ATTACH_MS = 8000

-- A warning when a JavaScript or TypeScript project's dependencies are not
-- where the language server will look, or are a symlink escaping the root:
-- the two ways its diagnostics turn authoritative and wrong.
local function node_modules_note(root, by_ft)
    local js = (by_ft.typescript or 0) + (by_ft.typescriptreact or 0)
        + (by_ft.javascript or 0) + (by_ft.javascriptreact or 0)
    if js == 0 then return nil end
    local nm = root .. "/node_modules"
    local lstat = vim.uv.fs_lstat(nm)
    if not lstat then
        -- A pnpm/yarn workspace keeps packages in the repo root, not the
        -- package dir, so only warn when there is a manifest here to install.
        if vim.uv.fs_stat(root .. "/package.json") then
            return "node_modules is absent under this root: the language server will report "
                .. "unresolved-import errors that are about the missing install, not the code. "
                .. "Install dependencies (npm/pnpm/yarn install) before trusting diagnostics."
        end
        return nil
    end
    if lstat.type == "link" then
        local target = vim.uv.fs_realpath(nm)
        local real_root = vim.uv.fs_realpath(root) or root
        if target and target:sub(1, #real_root + 1) ~= real_root .. "/" then
            return ("node_modules is a symlink to %s, outside this root: a package may resolve "
                .. "into a different checkout and produce a plausible but wrong type error. "
                .. "Verify a suspicious import diagnostic against the real dependency."):format(target)
        end
    end
    return nil
end

-- Resolve a real JDK home before jdtls starts. The Mason launcher eventually
-- execs Java without preserving argv[0]; that is harmless for a JVM binary
-- but makes version-manager shims (notably mise) interpret the first JVM flag
-- as a shim name. JAVA_HOME makes the launcher select the real binary and also
-- gives administration calls an actionable prerequisite failure.
local function configure_jdtls_sandbox_safety()
    local current = vim.lsp.config.jdtls or {}
    ---@type table
    local settings = vim.deepcopy(current.settings or {})
    local root_markers = vim.deepcopy(current.root_markers or {})
    settings.java = settings.java or {}
    settings.java.import = settings.java.import or {}
    settings.java.project = settings.java.project or {}
    -- Eclipse metadata is editor state, not a source change. Ask jdtls not
    -- to generate it at the project root; provider shutdown also quiesces
    -- any background import before sandbox command auditing begins.
    settings.java.import.generatesMetadataFilesAtProjectRoot = false
    -- Unmanaged Java fixtures have no Maven/Gradle metadata from which jdtls
    -- can infer source roots. Keep an explicit conventional fallback so the
    -- package declaration is checked relative to src/main/java (or src/test/java)
    -- instead of relative to the workspace root. Respect an existing project
    -- configuration when the user supplied one.
    if settings.java.project.sourcePaths == nil then
        settings.java.project.sourcePaths = { "src/main/java", "src/test/java", "src" }
    end
    -- Safe-copy sandboxes intentionally omit .git. The trusted project policy
    -- remains at the sandbox root, so let it anchor jdtls there; otherwise
    -- jdtls roots each source package too deeply and emits false "expected
    -- package ''" diagnostics for conventional src/main/java trees.
    local huyang_markers = { ".huyang.toml", ".huyang/pipeline.json" }
    local already_present = false
    for _, markers in ipairs(root_markers) do
        if vim.deep_equal(markers, huyang_markers) then already_present = true end
    end
    if not already_present then table.insert(root_markers, 1, huyang_markers) end
    vim.lsp.config("jdtls", { settings = settings, root_markers = root_markers })
end

local function pin_jdtls_workspace_root(root)
    configure_jdtls_sandbox_safety()
    vim.lsp.config("jdtls", { root_dir = root })
end

-- Sandbox providers may run diagnostics without first serving a support or
-- installation request. Configure jdtls before any Java buffer can attach so
-- conventional src/main/java package roots are interpreted from the sandbox
-- root rather than from an individual package directory.
configure_jdtls_sandbox_safety()

local function ensure_java_home()
    configure_jdtls_sandbox_safety()
    local configured = vim.env.JAVA_HOME
    if type(configured) == "string" and configured ~= ""
        and vim.uv.fs_stat(configured .. "/bin/java") then
        return configured
    end
    local java = vim.fn.exepath("java")
    if java == "" then
        return nil, "Java is not executable in the embedded Neovim environment; install JDK 21+ "
            .. "or set JAVA_HOME for the huyang user service"
    end
    local result = vim.system(
        { java, "-XshowSettings:properties", "-version" },
        { text = true }
    ):wait(3000)
    if not result then
        return nil, "Java prerequisite probe did not finish within 3s; set JAVA_HOME to a JDK 21+"
    end
    local output = (result.stdout or "") .. "\n" .. (result.stderr or "")
    local home = output:match("[\r\n]%s*java%.home%s*=%s*([^\r\n]+)")
    home = home and vim.trim(home) or nil
    if result.code ~= 0 or not home or not vim.uv.fs_stat(home .. "/bin/java") then
        local detail = vim.trim(output):gsub("%s+", " ")
        if #detail > 240 then detail = detail:sub(1, 237) .. "..." end
        return nil, "Java prerequisite probe failed"
            .. (detail ~= "" and (": " .. detail) or "")
            .. "; install JDK 21+ or set JAVA_HOME for the huyang user service"
    end
    vim.env.JAVA_HOME = home
    return home
end

-- A provider restart creates a fresh Neovim process, so earlier vim.lsp.enable
-- calls are gone. Mason's installed-package state is
-- durable; re-enable installed servers before probing workspace buffers.
local ensure_ruby_lsp_bundler

-- Defined further down, used by workspace_support above their definition:
-- the preferred server per filetype and the system-installed server lookup.
local PREFERRED_SERVER, system_server, enable_system_server

local python_environment = require("huyang.python_environment")

-- A started client that has not finished initializing yet: the server is
-- on its way, which is neither attached nor absent.
local function starting_server(configured)
    for _, client in ipairs(vim.lsp.get_clients()) do
        if vim.tbl_contains(configured, client.name) and not client.initialized then
            return client.name
        end
    end
    return nil
end

-- Mason packages are durable, but an owned headless provider may start
-- without mason.setup() having prepended its launcher directory. Put that
-- directory first explicitly so a version-manager shim with the same name
-- cannot win merely because it exists.
local function ensure_mason_bin_on_path(bin)
    bin = bin or (vim.fn.stdpath("data") .. "/mason/bin")
    local stat = vim.uv.fs_stat(bin)
    if not stat or stat.type ~= "directory" then return nil end
    local separator = package.config:sub(1, 1) == "\\" and ";" or ":"
    local kept = {}
    for _, entry in ipairs(vim.split(vim.env.PATH or "", separator, { plain = true, trimempty = true })) do
        if entry ~= bin then kept[#kept + 1] = entry end
    end
    vim.env.PATH = bin .. (#kept > 0 and (separator .. table.concat(kept, separator)) or "")
    return bin
end

local function enable_installed_servers(ft)
    ensure_mason_bin_on_path()
    local okreg, registry = pcall(require, "mason-registry")
    local okml, mlsp = pcall(require, "mason-lspconfig")
    if not okreg or not okml then return {} end
    local maps = mlsp.get_mappings()
    local candidates = enabled_lsp_configs_for(ft)
    pcall(function() candidates = mlsp.get_available_servers({ filetype = ft }) end)
    for _, name in ipairs(enabled_lsp_configs_for(ft)) do
        if not vim.tbl_contains(candidates, name) then candidates[#candidates + 1] = name end
    end
	local installed = {}
	pcall(function() installed = registry.get_installed_packages() end)
	for _, pkg in ipairs(installed) do
		local package = pkg.name or (pkg.get_name and pkg:get_name())
		local name = package and maps.package_to_lspconfig[package]
		local cfg = name and vim.lsp.config[name]
		if cfg and (not cfg.filetypes or vim.tbl_contains(cfg.filetypes, ft))
			and not vim.tbl_contains(candidates, name) then
			candidates[#candidates + 1] = name
		end
	end
    local enabled, seen = {}, {}
    for _, name in ipairs(candidates) do
        if not seen[name] then
            seen[name] = true
            local package = maps.lspconfig_to_package[name]
            local pkg
            local okp = package and pcall(function() pkg = registry.get_package(package) end)
            if okp and pkg and pkg:is_installed() then
                local runtime_ready = true
                if name == "ruby_lsp" and ensure_ruby_lsp_bundler then
                    runtime_ready = ensure_ruby_lsp_bundler(pkg) == true
                end
                if runtime_ready then
                    if pcall(vim.lsp.enable, name) then
                        enabled[#enabled + 1] = name
                    end
                end
            end
        end
    end
    table.sort(enabled)
    return enabled
end

local function support_attach_wait(ft, requested, restored)
    local wait_ms = math.min(15000, math.max(250, tonumber(requested) or SUPPORT_ATTACH_MS))
    -- jdtls commonly needs longer than the generic probe window to import a
    -- workspace after a fresh provider process. Only extend the implicit
    -- status probe when this call actually restored installed jdtls; explicit
    -- callers retain their requested bound.
    if requested == nil and ft == "java" and vim.tbl_contains(restored or {}, "jdtls") then
        wait_ms = math.max(wait_ms, JAVA_RESTART_ATTACH_MS)
    end
    if requested == nil and ft == "ruby" and vim.tbl_contains(restored or {}, "ruby_lsp") then
        wait_ms = math.max(wait_ms, RUBY_RESTART_ATTACH_MS)
    end
    return wait_ms
end

-- The filetypes a caller asked to have started, as a set: args.languages
-- names filetypes, args.files names files (relative to the root or
-- absolute) whose filetypes are wanted, such as the files a plan is about
-- to diagnose.
local function requested_filetypes(args)
    local requested = {}
    for _, ft in ipairs(type(args.languages) == "table" and args.languages or {}) do
        if type(ft) == "string" and ft ~= "" then requested[ft] = true end
    end
    for _, file in ipairs(type(args.files) == "table" and args.files or {}) do
        local ft = type(file) == "string" and vim.filetype.match({ filename = file }) or nil
        if ft then requested[ft] = true end
    end
    return requested
end

-- Load the sample file and wait up to wait_ms for a client to attach to it;
-- the names of the clients that did.
local function attach_sample(path, wait_ms)
    local clients = {}
    local okb, bufnr = pcall(load_buf, path)
    if not okb then return clients end
    local deadline = vim.uv.now() + wait_ms
    while vim.uv.now() < deadline do
        for _, c in ipairs(vim.lsp.get_clients({ bufnr = bufnr })) do
            clients[#clients + 1] = c.name
        end
        if #clients > 0 then break end
        sleep(100)
    end
    return clients
end

-- The initialized clients among the configured servers, whatever buffer
-- started them: a server is running for a language once any of its files
-- has been loaded, and a new buffer of that language attaches to it.
local function running_clients(configured)
    local names = {}
    for _, client in ipairs(vim.lsp.get_clients()) do
        if client.initialized and vim.tbl_contains(configured, client.name)
            and not vim.tbl_contains(names, client.name) then
            names[#names + 1] = client.name
        end
    end
    table.sort(names)
    return names
end

-- Whether one of the configured servers has a command that can run: a
-- function (it builds its own transport) or an executable on PATH.
local function any_startable(configured)
    for _, name in ipairs(configured) do
        local okc, cfg = pcall(function() return vim.lsp.config[name] end)
        local cmd = okc and type(cfg) == "table" and cfg.cmd or nil
        if type(cmd) == "function" then return true end
        if type(cmd) == "table" and type(cmd[1]) == "string" and vim.fn.executable(cmd[1]) == 1 then
            return true
        end
    end
    return false
end

local function workspace_support(args)
	local requested_attach_wait = tonumber(args.attach_wait_ms)
    local root = args.root
    if type(root) ~= "string" or root == "" then
        err("missing project root")
    end
    -- rust-analyzer's builds go to a shared on-disk directory per root, not
    -- the project's target/ and not /tmp; see cargo_target.lua. A plan
    -- sandbox passes its source root as cargo_root so it shares that
    -- root's build instead of starting one per sandbox.
    require("huyang.cargo_target").prepare(root, args.cargo_root)
    local files = project_files(root)
    local by_ft, sample, ext_cache = {}, {}, {}
    -- Reading a shebang costs a file open, so only for the files that have no
    -- extension to go on, and only for the first few: a repository's shell
    -- tooling is `scripts/lint`, `scripts/test`, and those were reported as
    -- no language at all - not even as unsupported.
    local sniffed, SNIFF_MAX = 0, 40
    for _, rel in ipairs(files) do
        local ext = rel:match("%.([%w_]+)$")
        local ft = ext and ext_cache[ext] or nil
        if ft == nil then
            ft = vim.filetype.match({ filename = rel }) or false
            if not ft and not ext and sniffed < SNIFF_MAX then
                sniffed = sniffed + 1
                local okl, lines = pcall(vim.fn.readfile, root .. "/" .. rel, "", 1)
                if okl and lines and lines[1] then
                    ft = vim.filetype.match({ filename = rel, contents = lines }) or false
                end
            end
            if ext then ext_cache[ext] = ft end
        end
        if ft then
            by_ft[ft] = (by_ft[ft] or 0) + 1
            if better_sample(sample[ft], rel) then
                sample[ft] = rel
            end
        end
    end
    local fts = vim.tbl_keys(by_ft)
    table.sort(fts, function(a, b) return by_ft[a] > by_ft[b] end)
    local requested = requested_filetypes(args)
    local eager = eager_filetypes(by_ft, requested)
    local out, blind, not_probed = {}, {}, {}
    for i, ft in ipairs(fts) do
        if i > SUPPORT_MAX_FILETYPES and not requested[ft] then
            -- Silently stopping here reported a repository's shell scripts as
            -- absent rather than as unprobed, and an agent following "install
            -- what says none" never learned they were covered.
            not_probed[#not_probed + 1] = ("%s (%d)"):format(ft, by_ft[ft])
        else
            local parser = has_parser(ft)
            local prerequisite
            if ft == "java" then
                local _, why = ensure_java_home()
                prerequisite = why
            end
			-- Each embedded provider owns exactly one workspace. Pin servers whose
			-- upstream root discovery can fall back to Neovim's daemon cwd so they
			-- attach to this workspace (and to an isolated preparation sandbox).
			local interpreter
			if ft == "typescript" or ft == "typescriptreact"
				or ft == "javascript" or ft == "javascriptreact" then
				local current = vim.lsp.config.ts_ls or {}
				local init_options = vim.deepcopy(current.init_options or {})
				-- The project's own TypeScript first, so the diagnostics match
				-- the version it builds with; Mason's bundled copy otherwise.
				local project_tsserver = root .. "/node_modules/typescript/lib/tsserver.js"
				local bundled_tsserver = vim.fn.stdpath("data")
					.. "/mason/packages/typescript-language-server/node_modules/typescript/lib/tsserver.js"
				for _, tsserver in ipairs({ project_tsserver, bundled_tsserver }) do
					if vim.uv.fs_stat(tsserver) then
						init_options.tsserver = vim.tbl_extend("force", init_options.tsserver or {}, {
							path = tsserver,
						})
						break
					end
				end
				pcall(vim.lsp.config, "ts_ls", { root_dir = root, init_options = init_options })
			elseif ft == "python" then
				interpreter = python_environment.interpreter()
			elseif ft == "ruby" then
				pcall(vim.lsp.config, "ruby_lsp", { root_dir = root })
			elseif ft == "java" then
				pcall(pin_jdtls_workspace_root, root)
			end
			local restored_servers = enable_installed_servers(ft)
			-- No Mason package and no enabled config: a server installed on the
			-- machine (gopls on PATH, pyright from the distro) is enabled here,
			-- so the first edit does not answer lsp_not_configured for a
			-- language the machine serves.
			local system_enabled
			if #enabled_lsp_configs_for(ft) == 0 and not DATA_FILETYPES[ft] and system_server then
				local okn, sysname, syscmd = pcall(system_server, ft, PREFERRED_SERVER and PREFERRED_SERVER[ft])
				if okn and sysname then
					local oke, enabled = pcall(enable_system_server, ft, sysname, syscmd)
					if oke and enabled and enabled.lspconfig then
						system_enabled = sysname .. " (" .. syscmd .. ")"
						restored_servers[#restored_servers + 1] = sysname
					end
				end
			end
			local attach_wait_ms = support_attach_wait(ft, requested_attach_wait, restored_servers)
            local configs = enabled_lsp_configs_for(ft)
            local clients, on_demand = {}, false
            -- A server that cannot start is probed the old way even for an
            -- incidental language: nothing is spawned, and the report says
            -- it did not attach rather than promising a start on first use.
            if #configs > 0 and (eager[ft] or not any_startable(configs)) then
                clients = attach_sample(root .. "/" .. sample[ft], attach_wait_ms)
            elseif #configs > 0 then
                -- Not started for the probe, but a tool call may already have
                -- started it by loading one of this language's files.
                clients = running_clients(configs)
                on_demand = #clients == 0 and starting_server(configs) == nil
            end
            local install_options = {}
            local okml, mlsp = pcall(require, "mason-lspconfig")
            if #clients == 0 and not on_demand and okml then
                pcall(function()
                    install_options = mlsp.get_available_servers({ filetype = ft })
                    table.sort(install_options)
                    local unique_options = {}
                    for _, option in ipairs(install_options) do
                        if unique_options[#unique_options] ~= option then
                            unique_options[#unique_options + 1] = option
                        end
                    end
                    install_options = unique_options
                end)
            end
            local entry = {
                filetype = ft,
                files = by_ft[ft],
                treesitter_parser = parser,
                lsp = #clients > 0 and table.concat(clients, ",") or "none",
                install_options = install_options,
                prerequisite = prerequisite,
                interpreter = interpreter,
                environment_reason = ft == "python" and python_environment.reason() or nil,
                enabled_from_system = system_enabled,
            }
            -- attach says where the server is: attached, still starting
            -- (a client exists but has not initialized), left to start on
            -- first use (on_demand), configured but absent after the wait,
            -- or not configured at all.
            entry.attach = #clients > 0 and "attached" or (#configs == 0 and "unconfigured" or "not_started")
            if on_demand then
                -- Enabled and startable, deliberately not started: the first
                -- tool call that loads one of these files starts it.
                entry.attach = "on_demand"
                entry.lsp = "on demand (" .. table.concat(configs, ",") .. ")"
                entry.configured = configs
            elseif #clients == 0 and #configs > 0 then
                local starting = starting_server(configs)
                if starting then
                    entry.attach = "starting"
                    entry.lsp = "starting (" .. starting .. ")"
                else
                    entry.lsp = "none (configured: " .. table.concat(configs, ",") .. ", did not attach)"
                end
            end
            if not parser and #clients == 0 and not on_demand and not DATA_FILETYPES[ft] then
                blind[#blind + 1] = ft
            end
            out[#out + 1] = entry
        end
    end
    local notes = {}
    if #not_probed > 0 then
        notes[#notes + 1] = ("%d further filetypes are in this tree and were not probed for a "
                .. "parser or a server (%s); skim or find_symbol on one of their files says what "
                .. "it has"):format(#not_probed, table.concat(not_probed, ", "))
    end
    if #blind > 0 then
        notes[#notes + 1] = ("no parser and no language server for %s: symbol, navigation and "
                .. "diagnostic tools will not work on those files; grep and read_file will. "
                .. "Install a parser and a server outside Huyang (nvim-treesitter and Mason in "
                .. "your Neovim setup, or the system package manager), then call "
                .. "language_server_setup action=restart")
            :format(table.concat(blind, ", "))
    end
    -- A JS/TS project whose dependencies are not installed makes the language
    -- server report a wall of unresolved-import errors that say nothing about
    -- the code, and the failure looks authoritative. Worse, a node_modules
    -- symlink escaping the root can resolve a package into another checkout,
    -- producing one plausible-but-wrong type error. Say so up front.
    local ok_dep, dep_note = pcall(node_modules_note, root, by_ft)
    if ok_dep and dep_note then
        notes[#notes + 1] = dep_note
        core.note_deps_missing(root, dep_note)
    end
    return { languages = out, note = #notes > 0 and table.concat(notes, " ") or nil }
end

-- Preferred language server per filetype where several are on the machine:
-- the lspconfig name workspace_support enables when no config is enabled yet.
PREFERRED_SERVER = {
    c = "clangd", cpp = "clangd", objc = "clangd", objcpp = "clangd",
    go = "gopls", gomod = "gopls",
    python = "pyright",
    javascript = "ts_ls", javascriptreact = "ts_ls",
    typescript = "ts_ls", typescriptreact = "ts_ls",
    lua = "lua_ls",
    rust = "rust_analyzer",
    java = "jdtls",
    kotlin = "kotlin_language_server",
    ruby = "ruby_lsp",
    php = "intelephense",
    cs = "omnisharp",
    swift = "sourcekit",
    zig = "zls",
    sh = "bashls",
    html = "html", css = "cssls", json = "jsonls", yaml = "yamlls", toml = "taplo",
    dockerfile = "dockerls",
    terraform = "terraformls",
    elixir = "elixirls",
    haskell = "hls",
    scala = "metals",
    dart = "dartls",
    ocaml = "ocamllsp",
    nix = "nil_ls",
    vim = "vimls",
}

-- Mason's ruby-lsp package relies on Bundler at runtime but does not install
-- it on Ruby distributions that omit the default Bundler gem. Keep the
-- dependency inside the Mason package so the generated launcher, whose
-- GEM_PATH points there, can load it without mutating the user's gem home.
ensure_ruby_lsp_bundler = function(pkg)
    local ruby, gem = vim.fn.exepath("ruby"), vim.fn.exepath("gem")
    if ruby == "" or gem == "" then
        return nil, "ruby-lsp requires RubyGems and Bundler; install ruby and gem, then retry"
    end
    local install_path = tostring(pkg:get_install_path())
    local env = { GEM_HOME = install_path, GEM_PATH = install_path .. ":" .. (vim.env.GEM_PATH or "") }
	-- The ruby-lsp launcher execs Bundler from a child shell. Keep the provider's
	-- isolated process environment aligned with cmd_env so that child sees the
	-- package-local bundle executable as well.
	vim.env.GEM_PATH = env.GEM_PATH
	vim.env.GEM_HOME = env.GEM_HOME
	if not (vim.env.PATH or ""):find(install_path .. "/bin", 1, true) then
		vim.env.PATH = install_path .. "/bin:" .. (vim.env.PATH or "")
	end
	local configured, config_err = pcall(function()
		local current = vim.deepcopy((vim.lsp.config.ruby_lsp or {}).cmd_env or {})
		current.GEM_HOME = env.GEM_HOME
		current.GEM_PATH = env.GEM_PATH
		current.PATH = install_path .. "/bin:" .. (vim.env.PATH or "")
		vim.lsp.config("ruby_lsp", { cmd_env = current })
	end)
	if not configured then
		return nil, "ruby-lsp Bundler is installed but its runtime environment could not be configured: "
			.. tostring(config_err) .. "; add " .. install_path .. "/bin to ruby_lsp cmd_env.PATH"
	end
    local function probe()
        local result = vim.system({ ruby, "-e", "require 'bundler'" }, { text = true, env = env }):wait(3000)
        return result and result.code == 0
    end
    if probe() then return true end
    local result = vim.system({ gem, "install", "--no-document", "--install-dir", install_path, "bundler" },
        { text = true }):wait(120000)
    if not result then
        return nil, "installing ruby-lsp dependency Bundler did not finish within 120s; "
            .. "run `gem install --no-document --install-dir " .. install_path .. " bundler` and retry"
    end
    if result.code ~= 0 or not probe() then
        local detail = vim.trim((result.stderr or "") .. " " .. (result.stdout or "")):gsub("%s+", " ")
        if #detail > 240 then detail = detail:sub(1, 237) .. "..." end
        return nil, "ruby-lsp cannot load Bundler; run `gem install --no-document --install-dir "
            .. install_path .. " bundler` and retry"
            .. (detail ~= "" and (" (" .. detail .. ")") or "")
    end
    return true
end

-- A language server Mason does not package can still be on the machine:
-- Qt ships qmlls with the distro, and some toolchains carry their own. Look
-- through the lspconfig configs on the runtimepath for one that covers the
-- filetype and whose command is executable here, including the versioned
-- name a distro may install it under (qmlls6 for qmlls, clangd-19 for
-- clangd), so a language is called unsupported only when nothing can run it.
system_server = function(ft, wanted)
    local seen, versioned = {}, nil
    for _, path in ipairs(vim.api.nvim_get_runtime_file("lsp/*.lua", true)) do
        local name = vim.fn.fnamemodify(path, ":t:r")
        if not seen[name] and (not wanted or wanted == "" or name == wanted) then
            seen[name] = true
            local okc, cfg = pcall(function() return vim.lsp.config[name] end)
            if okc and type(cfg) == "table" and vim.tbl_contains(cfg.filetypes or {}, ft)
                and type(cfg.cmd) == "table" and type(cfg.cmd[1]) == "string" then
                local cmd = cfg.cmd[1]
                if vim.fn.executable(cmd) == 1 then
                    return name, cmd
                end
                if not versioned then
                    local variants = vim.fn.getcompletion(cmd, "shellcmd")
                    table.sort(variants, function(a, b) return #a < #b end)
                    for _, v in ipairs(variants) do
                        if v:sub(1, #cmd) == cmd and vim.fn.executable(v) == 1 then
                            versioned = { name = name, cmd = v }
                            break
                        end
                    end
                end
            end
        end
    end
    if versioned then
        return versioned.name, versioned.cmd
    end
end

-- Enable a server that is already on the machine, pointing its config at
-- the command that actually exists.
enable_system_server = function(ft, name, cmd)
    local out = { lspconfig = name, cmd = cmd, status = "on the system" }
    local okcfg = pcall(function()
        local current = (vim.lsp.config[name] or {}).cmd
        if type(current) ~= "table" then
            vim.lsp.config(name, { cmd = { cmd } })
        elseif current[1] ~= cmd then
            -- Replace only the executable: a versioned binary (clangd-18)
            -- stands in for the plain name, and the config's own arguments
            -- ("--stdio", "--background-index") still apply to it.
            local replaced = vim.list_extend({ cmd }, vim.list_slice(current, 2))
            vim.lsp.config(name, { cmd = replaced })
        end
    end)
    if not okcfg then
        out.note = "could not set the command of " .. name .. " to " .. cmd
        return out
    end
    pcall(vim.lsp.enable, name)
    out.note = ("Mason has no server for %s; %s is installed on this machine and was enabled")
        :format(ft, cmd)
    return out
end

M.workspace_support = workspace_support
M._starting_server = starting_server
M._ensure_ruby_lsp_bundler = ensure_ruby_lsp_bundler
M._enable_installed_servers = enable_installed_servers
M._support_attach_wait = support_attach_wait
M._eager_filetypes = eager_filetypes
M._requested_filetypes = requested_filetypes
M._running_clients = running_clients
M._any_startable = any_startable
M._ensure_mason_bin_on_path = ensure_mason_bin_on_path
M._configure_jdtls_sandbox_safety = configure_jdtls_sandbox_safety
M._pin_jdtls_workspace_root = pin_jdtls_workspace_root

return M
