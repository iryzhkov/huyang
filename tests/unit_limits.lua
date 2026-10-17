-- Deterministic admission/lifecycle tests without installed language servers.
local here = debug.getinfo(1, "S").source:sub(2)
vim.opt.rtp:prepend(vim.fn.fnamemodify(here, ":h:h"))
local live, next_id, peak = {}, 0, 0
local callbacks = {}
local stop_delay, fail_create, nested = 0, false, nil
local function count()
    local n = 0
    for _ in pairs(live) do n = n + 1 end
    return n
end
local function exit(client)
    live[client.id] = nil
    callbacks[client.id](0, 0, client.id)
end
local fake = {}
fake.create = function(config)
    if fail_create then error("launch failed") end
    if nested then local fn = nested; nested = nil; fn() end
    next_id = next_id + 1
    local client = { id = next_id, name = config.name, initialized = true, config = config,
        requests = {}, attached_buffers = {}, flags = {}, progress = { pending = {} },
        handlers = { ["textDocument/publishDiagnostics"] = function() end },
        supports_method = function() return false end }
    callbacks[client.id] = config.on_exit
    function client:stop()
        self.stopping = true
        if stop_delay > 0 then vim.defer_fn(function() exit(self) end, stop_delay)
        elseif stop_delay == 0 then exit(self) end
    end
    live[client.id] = client
    peak = math.max(peak, count())
    return client
end
package.loaded["vim.lsp.client"] = fake
vim.lsp.get_clients = function(filter)
    local out = {}
    for _, client in pairs(live) do
        if (not filter or not filter.bufnr or client.attached_buffers[filter.bufnr])
            and (not filter or not filter.name or client.name == filter.name) then
            out[#out + 1] = client
        end
    end
    table.sort(out, function(a,b) return a.id < b.id end)
    return out
end
vim.lsp.get_client_by_id = function(id) return live[id] end
vim.lsp.start = function(config)
    for _, client in pairs(live) do
        if client.name == config.name and not client.stopping then return client.id end
    end
    return fake.create(config).id
end
local limits = require("huyang.limits")
local function start(name, opts)
    return fake.create(vim.tbl_extend("force", { name = name }, opts or {}))
end
local function denied(fn, label)
    local ok, why = pcall(fn)
    assert(not ok and tostring(why):find("language_server_capacity", 1, true), tostring(label) .. ": " .. tostring(why))
end
local function reset(n, held)
    local all = {}
    for _, c in pairs(live) do all[#all + 1] = c end
    for _, c in ipairs(all) do exit(c) end
    stop_delay, fail_create, nested, peak = 0, false, nil, 0
    package.loaded["huyang.transaction"] = nil
    package.loaded["huyang.dap"] = nil
    limits.setup(n or 2, held)
end
limits.setup(2, false)

local a,b = start("a"), start("b")
assert(vim.lsp.start({ name = "a" }) == a.id)
local c = start("c")
assert(live[a.id] and not live[b.id] and live[c.id] and peak == 2, "LRU reuse or cap failed")

reset(1)
a = start("a")
local buf = vim.api.nvim_create_buf(false, true)
a.attached_buffers[buf] = true
local co = coroutine.create(function()
    limits.begin_request()
    limits.touch_buffer(buf)
    vim.lsp.get_clients({ bufnr = buf })
    coroutine.yield()
    limits.end_request()
end)
assert(coroutine.resume(co))
denied(function() start("busy") end)
assert(live[a.id], "active caller lost server")
assert(coroutine.resume(co))
assert(start("replacement") and not live[a.id], "release did not unpin server")
vim.api.nvim_buf_delete(buf, { force = true })

-- A recently used server survives enumeration regardless of client ID order.
-- Run the real baseline collector too: it visits every loaded workspace buffer.
local edit = require("huyang.edit")
for _, scan in ipairs({ "all", "name", "buffer", "raw", "baseline" }) do
    reset(3)
    local buffers = {}
    for i, ft in ipairs({ "go", "sh", "c" }) do
        local bnr = vim.api.nvim_create_buf(false, false)
        vim.api.nvim_buf_set_name(bnr, vim.fn.getcwd() .. "/huyang-limit-unit-" .. i .. "." .. ft)
        vim.bo[bnr].filetype = ft
        vim.bo[bnr].modified = false
        buffers[i] = bnr
    end
    local go = start("gopls")
    local bash = start("bashls")
    local clang = start("clangd")
    for i, client in ipairs({ go, bash, clang }) do client.attached_buffers[buffers[i]] = true end
    co = coroutine.create(function()
        limits.begin_request()
        limits.touch_buffer(buffers[1])
        limits.end_request()
        limits.begin_request()
        if scan == "all" then vim.lsp.get_clients()
        elseif scan == "name" then
            vim.lsp.get_clients({ name = "gopls" })
            vim.lsp.get_clients({ name = "bashls" })
            vim.lsp.get_clients({ name = "clangd" })
        elseif scan == "buffer" then
            for _, bnr in ipairs(buffers) do vim.lsp.get_clients({ bufnr = bnr }) end
        elseif scan == "raw" then
            for _, bnr in ipairs(buffers) do limits.list_clients({ bufnr = bnr }) end
        else edit.diagnostic_evidence({ baseline = true, wait_ms = 0 }) end
        coroutine.yield()
        limits.end_request()
    end)
    assert(coroutine.resume(co))
    assert(start("rust_analyzer") and not live[bash.id] and live[go.id] and live[clang.id],
        scan .. " enumeration changed recency or pinned an idle client")
    assert(start("lua_ls") and not live[clang.id] and live[go.id],
        scan .. " enumeration lost recently used gopls")
    assert(coroutine.resume(co))
    for _, bnr in ipairs(buffers) do vim.api.nvim_buf_delete(bnr, { force = true }) end
end

-- Loading a new language at capacity must survive the real diagnostic
-- collector's all-client reuse scan and FileType replay in one tool request.
reset(2)
local scratch = (vim.env.TMPDIR or (vim.fn.stdpath("cache") .. "/huyang-limits"))
    .. "/limits-" .. vim.fn.getpid()
vim.fn.mkdir(scratch, "p")
local path = scratch .. "/new.huyangunit"
vim.fn.writefile({ "new language" }, path)
buf = vim.fn.bufadd(path)
vim.fn.bufload(buf)
vim.bo[buf].filetype = "huyangunit"
local go = start("gopls", { filetypes = { "go" } })
local bash = start("bashls", { filetypes = { "sh" } })
local saved_enabled = vim.lsp._enabled_configs
vim.lsp.config("huyang_limit_unit_new", { cmd = { "sh" }, filetypes = { "huyangunit" } })
vim.lsp._enabled_configs = { huyang_limit_unit_new = {} }
local fresh
local new_group = vim.api.nvim_create_augroup("huyang_limit_new_language", {})
vim.api.nvim_create_autocmd("FileType", { group = new_group, buffer = buf, callback = function()
    fresh = start("new_language", { filetypes = { "huyangunit" } })
    fresh.attached_buffers[buf] = true
    vim.api.nvim_exec_autocmds("LspAttach", { buffer = buf, data = { client_id = fresh.id } })
end })
co = coroutine.create(function()
    limits.begin_request()
    local evidence = edit.diagnostic_evidence({ files = { path }, wait_ms = 0 })
    assert(fresh and not live[go.id] and live[bash.id] and live[fresh.id] and peak == 2,
        "new-language FileType replay could not evict an idle server")
    assert(#evidence.batches == 1 and evidence.batches[1].producer == "new_language")
    assert(limits.request_error() == nil, "enumeration caused a false capacity refusal")
    assert(start("other") and live[fresh.id] and not live[bash.id],
        "actually used diagnostic client was not pinned")
    denied(function() start("too_many") end, "used diagnostic client")
    limits.end_request()
end)
assert(coroutine.resume(co))
vim.lsp._enabled_configs = saved_enabled
vim.api.nvim_del_augroup_by_id(new_group)
vim.api.nvim_buf_delete(buf, { force = true })
vim.fn.delete(scratch, "rf")

-- A buffer awaited by a request protects asynchronously attached clients.
reset(1)
a = start("a")
buf = vim.api.nvim_create_buf(false, true)
co = coroutine.create(function()
    limits.begin_request()
    limits.touch_buffer(buf)
    coroutine.yield()
    limits.end_request()
end)
assert(coroutine.resume(co))
a.attached_buffers[buf] = true
denied(function() start("busy") end)
assert(coroutine.resume(co))
assert(start("replacement") and not live[a.id])
vim.api.nvim_buf_delete(buf, { force = true })

for _, hold in ipairs({ "starting", "request", "progress", "transaction", "modified", "debug" }) do
    reset(1, hold == "debug")
    a = start("a")
    if hold == "starting" then a.initialized = false
    elseif hold == "request" then a.requests[1] = { type = "pending" }
    elseif hold == "progress" then a.progress.pending.token = true
    elseif hold == "transaction" then package.loaded["huyang.transaction"] = { status = function() return { active = true } end }
    elseif hold == "modified" then
        buf = vim.api.nvim_create_buf(false, false)
        a.attached_buffers[buf] = true
        vim.api.nvim_buf_set_lines(buf, 0, -1, false, { "unsaved" })
        vim.bo[buf].modified = true
    end
    denied(function() start("busy") end, hold)
    assert(live[a.id], hold .. " client evicted")
    if hold == "modified" then vim.api.nvim_buf_delete(buf, { force = true }) end
end

-- Session/trace activity pins clients only while ownership is live.
for _, owner in ipairs({ "session", "trace_targets" }) do
    reset(1)
    a = start("a")
    local active = true
    package.loaded["huyang.dap"] = { active = function() return active end }
    denied(function() start("during_" .. owner) end, owner)
    assert(live[a.id], owner .. " lost client")
    active = false
    assert(start("after_" .. owner) and not live[a.id], owner .. " did not release client")
end

-- Stopping counts until the process-exit callback, not is_stopped().
reset(1)
a = start("a")
stop_delay = -1
denied(function() start("new") end)
denied(function() start("another") end)
assert(count() == 1 and a.stopping)
exit(a)
assert(start("after-exit") and count() == 1)

reset(1)
a = start("a")
stop_delay = 30
b = start("b")
assert(not live[a.id] and live[b.id] and peak == 1, "started before victim exited")

-- The reservation includes reentrant starts during custom cmd setup.
reset(1)
nested = function() denied(function() start("nested") end) end
a = start("outer")
assert(count() == 1 and live[a.id] and peak == 1)
reset(1)
fail_create = true
assert(not pcall(start, "fail"))
fail_create = false
assert(start("ok") and count() == 1, "failed launch leaked reservation")

-- Revisiting a still-loaded buffer restarts its evicted server on demand.
reset(1)
buf = vim.api.nvim_create_buf(false, false)
a = start("a")
a.attached_buffers[buf] = true
b = start("b")
local reattached, restored = 0, nil
local group = vim.api.nvim_create_augroup("huyang_limit_unit", {})
vim.api.nvim_create_autocmd("FileType", { group = group, buffer = buf, callback = function()
    reattached = reattached + 1
    restored = start("a")
    restored.attached_buffers[buf] = true
    vim.api.nvim_exec_autocmds("LspAttach", { buffer = buf, data = { client_id = restored.id } })
end })
co = coroutine.create(function()
    limits.begin_request()
    limits.touch_buffer(buf)
    vim.lsp.get_clients() -- housekeeping must not pin the idle replacement
    limits.reattach_buffer(buf)
    limits.reattach_buffer(buf)
    assert(limits.request_error() == nil, "reattachment hit false capacity")
    limits.end_request()
end)
assert(coroutine.resume(co))
assert(reattached == 1 and restored and not live[b.id] and count() == 1)
vim.api.nvim_del_augroup_by_id(group)
vim.api.nvim_buf_delete(buf, { force = true })

-- Refusal survives a Neovim auto-attach pcall and is visible to the RPC.
reset(1, true)
a = start("held")
co = coroutine.create(function()
    limits.begin_request()
    denied(function() start("denied") end)
    assert(limits.request_error():find("language_server_capacity", 1, true))
    limits.end_request()
    assert(limits.request_error() == nil)
end)
assert(coroutine.resume(co))
reset(1, true)
a = start("held")
buf = vim.api.nvim_create_buf(false, false)
vim.bo[buf].filetype = "lua"
co = coroutine.create(function()
    limits.begin_request()
    limits.touch_buffer(buf)
    coroutine.yield()
    assert(limits.request_error():find("language_server_capacity", 1, true))
    limits.end_request()
end)
assert(coroutine.resume(co))
denied(function() start("async", { filetypes = { "lua" } }) end)
assert(coroutine.resume(co))
vim.api.nvim_buf_delete(buf, { force = true })
reset()

-- Exercise the real debug ownership query without installing nvim-dap.
local user_session
package.loaded["dap"] = { session = function() return user_session end }
local debug_runtime = require("huyang.dap")
local state = debug_runtime._state()
assert(not debug_runtime.active(), "debug module discovery claimed ownership")
assert(debug_runtime.dispatch("debug_breakpoints", {}).count == 0)
assert(not debug_runtime.active(), "breakpoint listing claimed ownership")
assert(debug_runtime.dispatch("debug_stop", {}).stopped == false)
assert(not debug_runtime.active(), "no-op stop claimed ownership")

state.session = { closed = false }
state.stop = { reason = "breakpoint" }
assert(debug_runtime.active(), "paused session was not retained")
state.stop = nil
assert(debug_runtime.active(), "running session was not retained")
state.exit = { reason = "exited" }
assert(not debug_runtime.active(), "exited session retained ownership")
state.exit = nil
state.session.closed = true
assert(not debug_runtime.active(), "closed session retained ownership")
state.session = nil
state.claiming = true
assert(debug_runtime.active(), "starting session was not retained")
state.claiming = false
local child = { closed = false }
state.children[child] = true
assert(debug_runtime.active(), "live child session was not retained")
state.exit = { reason = "exited" }
user_session = child
assert(not debug_runtime.active(), "exited child retained ownership")
user_session = { closed = false }
assert(debug_runtime.active(), "unrelated live session lost protection after child exit")
user_session = child
state.session = { closed = true }
state.exit = nil
assert(debug_runtime.active(), "live child of a closed root was not retained")
state.exit = { reason = "exited" }
assert(not debug_runtime.active(), "exited child of a closed root retained ownership")
state.session, state.exit, user_session = nil, nil, nil
child.closed = true
assert(not debug_runtime.active(), "closed child retained ownership")
state.children = {}
user_session = { closed = false }
assert(debug_runtime.active(), "unadopted user session was not retained")
state.finished = user_session
assert(not debug_runtime.active(), "finished session was resurrected")
user_session = nil

-- Ordinary breakpoint configuration is not ownership. A temporary trace
-- target holds both provider and clients until its sign or owner is removed.
buf = vim.api.nvim_create_buf(false, false)
vim.fn.sign_define("huyang_activity_test", { text = "X" })
local sign = vim.fn.sign_place(0, "dap_breakpoints", "huyang_activity_test", buf, { lnum = 1 })
local target = { bufnr = buf, line = 1, sign_id = sign }
state.breakpoints = { target }
assert(not debug_runtime.active(), "ordinary breakpoints pinned provider")
target.trace_owner = "trace_test"
assert(debug_runtime.active(), "trace target did not retain provider")
limits.setup(1, false)
a = start("trace_held")
denied(function() start("trace_denied") end)
package.loaded["dap.breakpoints"] = { remove = function()
    vim.fn.sign_unplace("dap_breakpoints", { buffer = buf, id = sign })
    return true
end }
assert(debug_runtime.dispatch("debug_breakpoints", { clear_trace = true, trace_owner = "trace_test" }).cleared == 1)
assert(not debug_runtime.active(), "trace cleanup retained ownership")
assert(start("after_trace_cleanup") and not live[a.id], "trace cleanup did not unpin clients")
state.breakpoints = { target }
assert(not debug_runtime.active(), "removed trace sign retained ownership")
state.breakpoints = {}
vim.api.nvim_buf_delete(buf, { force = true })
reset()
print("huyang language-server limits: OK")
vim.cmd("qa!")
