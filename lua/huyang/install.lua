-- What a workspace can and cannot do, and how to fix that: the language
-- support probe workspace_open reports, and install_language (tree-sitter
-- parser plus Mason server for one filetype).

local M = {}

local core = require("huyang.core")
local err, await, sleep, load_buf = core.err, core.await, core.sleep, core.load_buf
local project_files, better_sample, has_parser = core.project_files, core.better_sample, core.has_parser
local enabled_lsp_configs_for, DATA_FILETYPES = core.enabled_lsp_configs_for, core.DATA_FILETYPES


-- Internal: what the editor can do for the languages in a workspace, so a
-- client that opened a headless instance learns up front when a language
-- has no parser (skim/find_symbol/ts_query blind) or no language server
-- (definition/references/hover/diagnostics blind). Counts files per
-- filetype from the git index, checks the parser instantly, and for the
-- filetypes that have an enabled LSP config loads one sample file and
-- waits briefly for a client to attach (the binary may be missing).
local SUPPORT_MAX_FILETYPES = 10

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

-- A provider restart creates a fresh Neovim process, so vim.lsp.enable calls
-- made by language_server_setup are gone. Mason's installed-package state is
-- durable; re-enable installed servers before probing workspace buffers.
local ensure_ruby_lsp_bundler

-- Defined further down, used by workspace_support above their definition:
-- the preferred server per filetype and the system-installed server lookup.
local PREFERRED_SERVER, system_server, enable_system_server

-- The interpreter a Python project's own environment provides: a venv
-- directory under the root, else the one VIRTUAL_ENV names. pyright resolves
-- imports against it, so a package installed by uv or poetry is seen and a
-- test module's `import pytest` stops reading as unresolved.
local function project_python_interpreter(root)
    for _, venv in ipairs({ ".venv", "venv", "env" }) do
        local python = root .. "/" .. venv .. "/bin/python"
        if vim.fn.executable(python) == 1 then return python end
    end
    local active = vim.env.VIRTUAL_ENV
    if type(active) == "string" and active ~= "" and vim.fn.executable(active .. "/bin/python") == 1 then
        return active .. "/bin/python"
    end
    return nil
end

-- Point pyright and basedpyright at the project's interpreter before their
-- first buffer attaches; a server already running keeps its settings until
-- the provider restarts, which language_server_status says.
local function configure_python_interpreter(root)
    local python = project_python_interpreter(root)
    if not python then return nil end
    for _, name in ipairs({ "pyright", "basedpyright" }) do
        pcall(vim.lsp.config, name, { settings = { python = { pythonPath = python } } })
    end
    return python
end

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
    local out, blind, not_probed = {}, {}, {}
    for i, ft in ipairs(fts) do
        if i > SUPPORT_MAX_FILETYPES then
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
				interpreter = configure_python_interpreter(root)
			elseif ft == "ruby" then
				pcall(vim.lsp.config, "ruby_lsp", { root_dir = root })
			elseif ft == "java" then
				pcall(pin_jdtls_workspace_root, root)
			end
			local restored_servers = enable_installed_servers(ft)
			-- No Mason package and no enabled config: a server installed on the
			-- machine (gopls on PATH, pyright from the distro) is enabled the
			-- way language_server_setup would enable it, so the first edit does
			-- not answer lsp_not_configured for a language the machine serves.
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
            -- What could run this language under a debugger, so a client
            -- learns the option exists even when the debug tools are off.
            -- Probed before the server starts: for Java the probe also adds
            -- the java-debug bundle to the jdtls config, which only counts
            -- for a client that has not started yet.
            local debugger
            if not DATA_FILETYPES[ft] then
                local okd, dbg = pcall(function()
                    return require("huyang.dap").debugger_for(ft, root)
                end)
                if okd then debugger = dbg end
            end
            local clients = {}
            if #configs > 0 then
                local okb, bufnr = pcall(load_buf, root .. "/" .. sample[ft])
                if okb then
                    local deadline = vim.uv.now() + attach_wait_ms
                    while vim.uv.now() < deadline do
                        for _, c in ipairs(vim.lsp.get_clients({ bufnr = bufnr })) do
                            clients[#clients + 1] = c.name
                        end
                        if #clients > 0 then break end
                        sleep(100)
                    end
                end
            end
            local install_options = {}
            local okml, mlsp = pcall(require, "mason-lspconfig")
            if #clients == 0 and okml then
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
                debugger = debugger,
                install_options = install_options,
                prerequisite = prerequisite,
                interpreter = interpreter,
                enabled_from_system = system_enabled,
            }
            -- attach says where the server is: attached, still starting
            -- (a client exists but has not initialized), configured but
            -- absent after the wait, or not configured at all.
            entry.attach = #clients > 0 and "attached" or (#configs == 0 and "unconfigured" or "not_started")
            if #clients == 0 and #configs > 0 then
                local starting = starting_server(configs)
                if starting then
                    entry.attach = "starting"
                    entry.lsp = "starting (" .. starting .. ")"
                else
                    entry.lsp = "none (configured: " .. table.concat(configs, ",") .. ", did not attach)"
                end
            end
            if not parser and #clients == 0 and not DATA_FILETYPES[ft] then
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
                .. "language_server_setup(action=install, language=...) can add both")
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

-- install_language: add a treesitter parser (nvim-treesitter) and a language
-- server (Mason) for one filetype to the running instance, so the symbol and
-- navigation tools start working on files open_workspace flagged as blind.
-- Both installs are optional pieces of the user's setup; each step reports
-- what it did or why it could not.
local INSTALL_PARSER_MS = 5 * 60 * 1000

local INSTALL_SERVER_MS = 8 * 60 * 1000

local INSTALL_ATTACH_MS = 8000

-- Preferred language server per filetype where Mason offers several; the
-- lspconfig name, mapped to a Mason package through mason-lspconfig.
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

local function install_parser(ft, lang)
    if has_parser(ft) then
        return { language = lang, status = "already installed" }
    end
    local okts, ts = pcall(require, "nvim-treesitter")
    if not okts or type(ts.install) ~= "function" then
        return { language = lang, status = "skipped",
            note = "nvim-treesitter is not on the runtimepath; install the parser by hand" }
    end
    local available = {}
    pcall(function()
        for _, l in ipairs(ts.get_available()) do available[l] = true end
    end)
    if next(available) and not available[lang] then
        return { language = lang, status = "unsupported",
            note = "nvim-treesitter has no parser named " .. lang }
    end
    local task
    local okstart, start_err = pcall(function() task = ts.install({ lang }) end)
    if not okstart or type(task) ~= "table" or type(task.await) ~= "function" then
        return { language = lang, status = "failed",
            note = "could not start the install: " .. tostring(start_err or task) }
    end
    local timer = vim.uv.new_timer()
    local aerr, done = await(function(resume)
        timer:start(INSTALL_PARSER_MS, 0, vim.schedule_wrap(function()
            resume("timed out")
        end))
        task:await(function(e, r) resume(e, r) end)
    end)
    timer:stop()
    timer:close()
    if aerr then
        return { language = lang, status = "failed", note = tostring(aerr) }
    end
    -- Neovim caches "no such parser"; a fresh add() picks the new .so up.
    pcall(vim.treesitter.language.add, lang)
    if has_parser(ft) then
        return { language = lang, status = "installed" }
    end
    return { language = lang, status = "failed",
        note = done == false and "nvim-treesitter reported the install as failed"
            or "install finished but the parser still does not load" }
end

-- vim.lsp.get_log_path is deprecated from 0.11 in favour of
-- vim.lsp.log.get_filename; take whichever this Neovim has.
local function lsp_log_path()
    if vim.lsp.log and vim.lsp.log.get_filename then
        return vim.lsp.log.get_filename()
    end
    ---@diagnostic disable-next-line: deprecated
    return vim.lsp.get_log_path()
end

local function attached_client(root, ft)
    local files = vim.fn.systemlist({ "git", "-C", root,
        "ls-files", "--cached", "--others", "--exclude-standard" })
    if vim.v.shell_error ~= 0 then
        files = vim.tbl_map(function(f) return f:sub(#root + 2) end,
            vim.fn.globpath(root, "**/*", true, true))
    end
    local sample
    for _, rel in ipairs(files) do
        if vim.filetype.match({ filename = rel }) == ft then
            sample = rel
            break
        end
    end
    if not sample then
        return nil, "no " .. ft .. " file in the workspace to try"
    end
    -- Server configs explain a refusal to start through vim.notify
    -- ("cargo not found"); collect those so the reply says why.
    local notices, orig_notify, orig_once = {}, vim.notify, vim.notify_once
    local function collect(msg, level)
        if type(msg) == "string" and (level or 0) >= vim.log.levels.WARN then
            notices[#notices + 1] = msg:gsub("%s+", " ")
        end
    end
    ---@diagnostic disable-next-line: duplicate-set-field
    vim.notify = function(msg, level, opts)
        collect(msg, level)
        return orig_notify(msg, level, opts)
    end
    ---@diagnostic disable-next-line: duplicate-set-field
    vim.notify_once = function(msg, level, opts)
        collect(msg, level)
        return orig_once(msg, level, opts)
    end
    -- Everything between the swap and the restore runs under pcall, so an
    -- error (or a bad sample file) cannot leave vim.notify hijacked for the
    -- rest of the session; the error is re-raised once the originals are
    -- back. The wait yields the coroutine, and LuaJIT allows that inside
    -- pcall.
    local okb, bufnr = pcall(load_buf, root .. "/" .. sample)
    local client
    local okw, werr = pcall(function()
        if not okb then return end
        -- The sample may have been loaded before the server existed (the
        -- open_workspace probe does that); explicitly replay FileType so
        -- vim.lsp.enable's autocmd gets a second chance even when the option is unchanged.
        if #vim.lsp.get_clients({ bufnr = bufnr }) == 0 then
            pcall(function()
                vim.bo[bufnr].filetype = ft
                vim.api.nvim_exec_autocmds("FileType", { buffer = bufnr, modeline = false })
            end)
        end
        local deadline = vim.uv.now() + INSTALL_ATTACH_MS
        while vim.uv.now() < deadline do
            local clients = vim.lsp.get_clients({ bufnr = bufnr })
            if #clients > 0 then
                client = clients[1].name
                break
            end
            sleep(200)
        end
    end)
    vim.notify, vim.notify_once = orig_notify, orig_once
    if not okw then
        error(werr, 0)
    end
    if client then
        return client
    end
    if not okb then
        return nil, "could not load " .. sample
    end
    local why = "no client attached to " .. sample .. " within "
        .. (INSTALL_ATTACH_MS / 1000) .. "s"
    if #notices > 0 then
        why = why .. ": " .. table.concat(notices, "; ")
    else
        why = why .. "; the server's config may need a toolchain on PATH "
            .. "(rust_analyzer wants cargo, jdtls a JDK); see " .. lsp_log_path()
    end
    return nil, why
end

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
-- the command that actually exists, and report whether it attached.
enable_system_server = function(ft, name, cmd, root, why)
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
    out.note = ("%s; %s is installed on this machine and was enabled")
        :format(why or ("Mason has no server for " .. ft), cmd)
    if type(root) == "string" and root ~= "" then
        local client, cwhy = attached_client(root, ft)
        if client then
            out.attached = client
        else
            out.attached = false
            out.note = out.note .. ", but " .. cwhy
        end
    end
    return out
end

local function server_prerequisite(package)
    if package == "rust-analyzer" then
        local cargo = vim.fn.exepath("cargo")
        if cargo == "" then
            return nil, "rust-analyzer requires Cargo, but cargo is absent from the embedded Neovim PATH; "
                .. "install Rust or add its bin directory to the Huyang user service PATH, then restart the provider"
        end
        local result = vim.system({ cargo, "--version" }, { text = true }):wait(3000)
        if not result then
            return nil, ("rust-analyzer requires a working Cargo toolchain, but `%s --version` did not finish within 3s; "
                .. "fix the Huyang user service PATH, then restart the provider"):format(cargo)
        end
        if result.code ~= 0 then
			-- A service manager PATH commonly exposes a broken version-manager
			-- shim before rustup's real user installation. Prefer the usable
			-- local toolchain when it exists, and make it visible to the server.
			local fallback = vim.fn.expand("~/.cargo/bin/cargo")
			if fallback ~= cargo and vim.uv.fs_stat(fallback) then
				local fallback_result = vim.system({ fallback, "--version" }, { text = true }):wait(3000)
				if fallback_result and fallback_result.code == 0 then
					ensure_mason_bin_on_path(vim.fs.dirname(fallback))
					return true
				end
			end
            local detail = vim.trim((result.stderr or "") .. " " .. (result.stdout or "")):gsub("%s+", " ")
            if #detail > 240 then detail = detail:sub(1, 237) .. "..." end
            return nil, ("rust-analyzer requires a working Cargo toolchain, but `%s --version` exited %s%s; "
                .. "make cargo usable in the Huyang user service PATH "
                .. "(for mise, run `mise use -g rust@stable`), then restart the provider")
                :format(cargo, tostring(result.code), detail ~= "" and (": " .. detail) or "")
        end
        return true
    end
    if package ~= "csharp-language-server" then return true end
    local dotnet = vim.fn.exepath("dotnet")
    if dotnet == "" then
        return nil, "csharp-language-server requires the .NET SDK; install an SDK so `dotnet --list-sdks` is non-empty, then retry"
    end
    local result = vim.system({ dotnet, "--list-sdks" }, { text = true }):wait(3000)
    if not result or result.code ~= 0 or vim.trim(result.stdout or "") == "" then
        return nil, "csharp-language-server requires the .NET SDK, but only the runtime is available; install an SDK so `dotnet --list-sdks` is non-empty, then retry"
    end
    return true
end

local function install_server(ft, wanted, root)
    ensure_mason_bin_on_path()
    local okreg, registry = pcall(require, "mason-registry")
    local okml, mlsp = pcall(require, "mason-lspconfig")
    if not okreg or not okml then
        return { status = "skipped",
            note = "mason.nvim and mason-lspconfig.nvim are needed to install servers; "
                .. "install one by hand and enable it with vim.lsp.enable()" }
    end
    local maps = mlsp.get_mappings()
    -- Resolve the wanted server: an explicit name may be a Mason package or
    -- an lspconfig name; otherwise the preferred server for the filetype,
    -- else whatever Mason offers for it.
    local candidates = {}
    pcall(function()
        candidates = mlsp.get_available_servers({ filetype = ft })
    end)
    table.sort(candidates)
    local unique_candidates = {}
    for _, candidate in ipairs(candidates) do
        if unique_candidates[#unique_candidates] ~= candidate then
            unique_candidates[#unique_candidates + 1] = candidate
        end
    end
    candidates = unique_candidates
    local lspname, package
    if wanted and wanted ~= "" then
        if maps.package_to_lspconfig[wanted] then
            package, lspname = wanted, maps.package_to_lspconfig[wanted]
        elseif maps.lspconfig_to_package[wanted] then
            lspname, package = wanted, maps.lspconfig_to_package[wanted]
        else
            local sysname, syscmd = system_server(ft, wanted)
            if sysname then
                return enable_system_server(ft, sysname, syscmd, root)
            end
            return {
                status = "unknown",
                note = ("Mason has no package or server named %s; servers for %s: %s")
                    :format(wanted, ft, #candidates > 0 and table.concat(candidates, ", ") or "none")
            }
        end
    else
        lspname = PREFERRED_SERVER[ft]
        if not lspname or not maps.lspconfig_to_package[lspname] then
            -- An already enabled config for this filetype wins over a fresh pick.
            for _, name in ipairs(enabled_lsp_configs_for(ft)) do
                if maps.lspconfig_to_package[name] then
                    lspname = name
                    break
                end
            end
        end
        if not lspname or not maps.lspconfig_to_package[lspname] then
            lspname = candidates[1]
        end
        if not lspname then
            local sysname, syscmd = system_server(ft)
            if sysname then
                return enable_system_server(ft, sysname, syscmd, root)
            end
            return {
                status = "unsupported",
                note = "Mason offers no language server for filetype " .. ft
            }
        end
        package = maps.lspconfig_to_package[lspname]
    end
    local out = { package = package, lspconfig = lspname }
    if #candidates > 1 then
        out.alternatives = vim.tbl_filter(function(c) return c ~= lspname end, candidates)
    end
    local ready, prerequisite = server_prerequisite(package)
    if not ready then
        out.status = "prerequisite missing"
        out.attached = false
        out.note = prerequisite
        return out
    end
    -- Mason lists a package it cannot always supply: qmlls has no build for
    -- every platform, and an install that fails leaves the language with no
    -- server at all. When the machine already carries one, enabling it is a
    -- better answer than reporting the failure and stopping there.
    local function or_system(failed)
        local sysname, syscmd = system_server(ft, lspname)
        if not sysname then
            return failed
        end
        local res = enable_system_server(ft, sysname, syscmd, root,
            ("Mason could not install a server for %s (%s)"):format(ft, failed.note))
        res.mason_package = failed.package
        return res
    end
    local pkg
    local okp = pcall(function() pkg = registry.get_package(package) end)
    if not okp or not pkg then
        out.status = "unknown"
        out.note = "Mason registry has no package " .. package
        return or_system(out)
    end
    if pkg:is_installed() then
        out.status = "already installed"
    else
        await(function(resume)
            pcall(registry.refresh, function() resume() end)
        end)
        local timer = vim.uv.new_timer()
        local timed_out = await(function(resume)
            timer:start(INSTALL_SERVER_MS, 0, vim.schedule_wrap(function()
                resume(true)
            end))
            local okh, herr = pcall(function()
                pkg:install():once("closed", vim.schedule_wrap(function() resume(false) end))
            end)
            if not okh then
                out.start_error = tostring(herr)
                resume(false)
            end
        end)
        timer:stop()
        timer:close()
        if timed_out then
            out.status = "failed"
            out.note = "install did not finish within " .. (INSTALL_SERVER_MS / 60000) .. " minutes"
            return or_system(out)
        end
        if not pkg:is_installed() then
            out.status = "failed"
            out.note = (out.start_error or "Mason reported the install as failed")
                .. "; see :MasonLog (" .. vim.fn.stdpath("log") .. "/mason.log)"
            out.start_error = nil
            return or_system(out)
        end
        out.status = "installed"
    end
    -- mason-lspconfig enables freshly installed servers itself when its
    -- automatic_enable is on; doing it here too is idempotent and covers
    -- the "already installed but never enabled" case.
    if lspname == "jdtls" then
        local java_home, why = ensure_java_home()
        if not java_home then
            out.status = "prerequisite missing"
            out.attached = false
            out.note = why
            return out
        end
        out.java_home = java_home
    elseif lspname == "ruby_lsp" then
        local bundler_ready, why = ensure_ruby_lsp_bundler(pkg)
        if not bundler_ready then
            out.status = "prerequisite missing"
            out.attached = false
            out.note = why
            return out
        end
        out.runtime_dependency = "bundler"
    end
    local enabled, enable_err = pcall(vim.lsp.enable, lspname)
    if not enabled then
        out.attached = false
        out.note = "could not enable " .. lspname .. ": " .. tostring(enable_err)
        return out
    end
    local cmd = vim.tbl_get(vim.lsp.config, lspname, "cmd")
    if type(cmd) == "table" and type(cmd[1]) == "string" then
        local executable = vim.fn.exepath(cmd[1])
        if executable == "" then
            out.attached = false
            out.note = cmd[1] .. " is not executable from this Neovim (Huyang prepended Mason's bin directory before enabling it)"
            return out
        end
        out.executable = executable
    end
    if type(root) == "string" and root ~= "" then
        local client, why = attached_client(root, ft)
        if client then
            out.attached = client
        else
            out.attached = false
            out.note = why
        end
    end
    return out
end

local function install_language(args)
    local ft = args.language
    if type(ft) ~= "string" or ft == "" then
        err("missing required argument: language")
    end
    ft = ft:lower()
    -- Accept a file extension or a treesitter name too ("ts", "cpp", "c++").
    ft = vim.filetype.match({ filename = "x." .. ft }) or ft
    local lang = vim.treesitter.language.get_lang(ft) or ft
    local result = { language = ft }
    if args.parser ~= false then
        result.parser = install_parser(ft, lang)
    else
        result.parser = { status = "skipped" }
    end
    if args.server ~= "none" then
        result.server = install_server(ft, args.server, args.root)
    else
        result.server = { status = "skipped" }
    end
    result.note = "installed pieces live in Neovim's data directory and survive restarts; "
        .. "add them to the editor config's ensure_installed lists to keep them on a fresh machine"
    return result
end
M.workspace_support = workspace_support
M.install_language = install_language
M._project_python_interpreter = project_python_interpreter
M._configure_python_interpreter = configure_python_interpreter
M._starting_server = starting_server
M._ensure_ruby_lsp_bundler = ensure_ruby_lsp_bundler
M._enable_installed_servers = enable_installed_servers
M._support_attach_wait = support_attach_wait
M._server_prerequisite = server_prerequisite
M._ensure_mason_bin_on_path = ensure_mason_bin_on_path
M._attached_client = attached_client
M._configure_jdtls_sandbox_safety = configure_jdtls_sandbox_safety
M._pin_jdtls_workspace_root = pin_jdtls_workspace_root

return M
