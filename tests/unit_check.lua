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

if failures > 0 then
    io.stdout:write(("unit_check: %d failed\n"):format(failures))
    vim.cmd("cquit 1")
end
io.stdout:write("unit_check: OK\n")
io.stdout:flush()
vim.cmd("quit")
