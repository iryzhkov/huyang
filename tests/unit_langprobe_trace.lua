-- Synthetic observer regressions. No language server, disk fixtures or user config.
-- Run in a scratch HOME: nvim --clean --headless -u NONE -l tests/unit_langprobe_trace.lua
local hook = vim.fn.fnamemodify(debug.getinfo(1, "S").source:sub(2), ":h") .. "/langprobe-native-trace.lua"
local host = vim
local function pack(...) return { n = select("#", ...), ... } end
local function fixture()
    local s = { events = {}, markers = {}, files = {}, buffers = {}, reads = 0, opens = 0,
        forwarded = {}, fast = false, unsafe_calls = 0, callbacks = {} }
    local function api(fn)
        return function(...)
            if s.fast then s.unsafe_calls = s.unsafe_calls + 1; error("API in fast callback") end
            return fn(...)
        end
    end
    local client = { id = 1, name = "trace_test", attached_buffers = {}, rpc = { pid = 7 },
        config = { root_dir = "/project", cmd = { "fake" }, settings = {}, capabilities = {} } }
    s.client = client
    local function handler(...)
        s.forwarded[#s.forwarded + 1] = pack(...)
        if s.handle then s.handle(...) end
        if s.throw then error(s.throw, 0) end
        return "returned", nil, 9, nil
    end
    client.request = function() return true, 3 end
    client.cancel_request = function() return true end
    client.notify = function() return true end
    client._resolve_handler = function(self, method)
        assert(self == client and type(method) == "string", "handler resolution arguments changed")
        return handler, nil, "resolver-tail"
    end
    client._on_error, client._on_exit = function() end, function() end
    local fake = {
        env = { HUYANG_LANGPROBE_NATIVE_TRACE = "/trace", HUYANG_LANGPROBE_NATIVE_SCOPE = "probe" },
        NIL = host.NIL, json = { encode = function(event)
            if s.encoded_size then return string.rep("x", s.encoded_size) end
            return host.json.encode(event)
        end },
        in_fast_event = function() return s.fast end,
        lsp = { util = { buf_versions = {} }, get_client_by_id = api(function() return client end),
            log = { _set_filename = function() end, set_level = function() end,
                get_filename = function() return "/trace/lsp.log" end } },
        api = {}, fn = {}, bo = setmetatable({}, { __index = function(_, b)
            return { endofline = s.buffers[b].eol or false, buftype = s.buffers[b].buftype or "" }
        end }),
        diagnostic = { get = api(function() return {} end) },
        tbl_extend = function(_, _, config)
            local copy = {}; for k, v in pairs(config) do copy[k] = v end; return copy
        end,
        rpcnotify = function() end,
    }
    s.vim = fake
    fake.fn.getpid = function() return 123 end
    fake.fn.sha256 = api(host.fn.sha256)
    fake.fn.bufnr = api(function(path)
        for b, buf in pairs(s.buffers) do if buf.path == path then return b end end
        return -1
    end)
    fake.uri_to_fname = api(function(uri)
        assert(uri:sub(1, 7) == "file://", "non-file URI conversion")
        return uri:sub(8)
    end)
    fake.api.nvim_buf_is_valid = api(function(b) return s.buffers[b] ~= nil end)
    fake.api.nvim_buf_get_name = api(function(b) return s.buffers[b].path end)
    fake.api.nvim_buf_is_loaded = api(function(b) return s.buffers[b].loaded ~= false end)
    fake.api.nvim_buf_get_changedtick = api(function() return 2 end)
    fake.api.nvim_buf_line_count = api(function(b) return #(s.buffers[b].lines or { "" }) end)
    fake.api.nvim_buf_get_offset = api(function(b) return s.buffers[b].size or 0 end)
    fake.api.nvim_buf_get_lines = api(function(b) return s.buffers[b].lines or { "" } end)
    fake.api.nvim_create_autocmd = api(function(name, opts) s.callbacks[name] = opts.callback end)
    local function denied(file, step)
        local code = file and file[step]
        if code then return nil, code, code end
    end
    fake.uv = {
        hrtime = function() return 1 end, gettimeofday = function() return 1, 0 end,
        fs_stat = function(path)
            local file = s.files[path]
            if not file then return nil, "ENOENT", "ENOENT" end
            if file.stat_error then return denied(file, "stat_error") end
            return { type = file.type or "file", size = file.size or #(file.body or "") }
        end,
        fs_open = function(path)
            if path:sub(1, 7) == "/trace/" then return "trace" end
            s.opens = s.opens + 1
            local file = s.files[path]
            if not file then return nil, "ENOENT", "ENOENT" end
            if file.open_error then return denied(file, "open_error") end
            return file
        end,
        fs_fstat = function(file)
            if file.fstat_error then return denied(file, "fstat_error") end
            return { type = file.opened_type or file.type or "file", size = file.size or #(file.body or "") }
        end,
        fs_read = function(file, n)
            s.reads = s.reads + 1
            if file.read_error then return denied(file, "read_error") end
            return (file.body or ""):sub(1, n)
        end,
        fs_close = function(file)
            if file.close_error then return denied(file, "close_error") end
            return true
        end,
        fs_write = function(_, line)
            s.events[#s.events + 1] = line
            return s.partial_write and #line - 1 or #line
        end,
    }
    local Client = { create = function(config)
        for _, callback in ipairs(config.on_init) do callback(client) end
        return client
    end }
    local env = setmetatable({ vim = fake, io = {
        open = function(path)
            return { write = function(_, message) s.markers[path] = message end, close = function() return true end }
        end,
        stderr = { write = function() end },
    }, require = function(name) assert(name == "vim.lsp.client"); return Client end }, { __index = _G })
    env._G = env
    setfenv(assert(loadfile(hook)), env)()
    Client.create(client.config)
    function s.publish(uri, ...)
        local resolved = pack(client:_resolve_handler("textDocument/publishDiagnostics"))
        assert(resolved.n == 3 and resolved[2] == nil and resolved[3] == "resolver-tail")
        local params = { uri = uri, diagnostics = {} }
        local context, config = { client_id = 1 }, {}
        local args = pack(nil, params, context, config, ...)
        local result = pack(resolved[1](unpack(args, 1, args.n)))
        local forwarded = s.forwarded[#s.forwarded]
        assert(forwarded.n == args.n, "callback argument count changed")
        for i = 1, args.n do assert(forwarded[i] == args[i], "callback argument identity changed") end
        assert(result.n == 4 and result[1] == "returned" and result[2] == nil
            and result[3] == 9 and result[4] == nil, "callback returns changed")
    end
    function s.snapshot()
        for i = #s.events, 1, -1 do
            local event = host.json.decode(s.events[i])
            if event.kind == "producer" then return event.snapshots[1], event end
        end
        error("producer event missing")
    end
    function s.failed() return next(s.markers) ~= nil end
    function s.regular(body)
        s.files["/project/a"] = { body = body or "" }
        s.buffers[2] = { path = "/project/a" }
    end
    return s
end

local function test(name, fn)
    fn()
    io.stdout:write("ok   " .. name .. "\n")
end
local function unavailable(s, reason)
    local image = s.snapshot()
    assert(image.snapshot_unavailable_reason == reason, host.inspect(image))
    assert(image.disk_sha256 == nil and image.postimage_sha256 == nil, "unavailable image invented hashes")
    assert(not s.failed(), "correlation unavailability disabled observer")
    local before = #s.events
    s.publish("untitled:next")
    assert(#s.events == before + 2, "observer stopped after unavailable snapshot")
end

test("repeated_directory_publication_keeps_observer_and_callback", function()
    local s = fixture()
    s.files["/project"] = { type = "directory" }
    s.handle = function() s.buffers[4] = { path = "/project", loaded = false } end
    s.publish("file:///project", nil, "tail", nil)
    unavailable(s, "buffer_unavailable")
    s.publish("file:///project", nil, "tail", nil)
    unavailable(s, "disk_not_regular_file")
    assert(s.opens == 0 and s.reads == 0, "directory opened for reading")
end)
test("empty_regular_file_hashes_empty_bytes", function()
    local s = fixture(); s.regular("")
    s.publish("file:///project/a")
    local image = s.snapshot()
    assert(image.disk_sha256 == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
    assert(image.postimage_sha256 == image.disk_sha256 and not s.failed())
end)
test("non_file_uri_does_not_convert_or_read", function()
    local s = fixture(); s.publish("jdt://contents/project")
    unavailable(s, "non_file_uri")
    assert(s.opens == 0 and s.reads == 0)
end)
test("unloaded_regular_buffer_has_explicit_reason", function()
    local s = fixture(); s.regular(""); s.buffers[2].loaded = false
    s.publish("file:///project/a"); unavailable(s, "buffer_unloaded")
    assert(s.opens == 0 and s.reads == 0)
end)
test("missing_and_unreadable_files_do_not_disable_observer", function()
    for _, code in ipairs({ "ENOENT", "EACCES", "EPERM" }) do
        local s = fixture(); s.regular(""); s.files["/project/a"].stat_error = code
        s.publish("file:///project/a")
        unavailable(s, code == "ENOENT" and "disk_missing" or "disk_unreadable")
    end
end)
test("guarded_open_read_and_close_failures_have_reasons", function()
    for _, step in ipairs({ "open", "fstat", "read", "close" }) do
        local s = fixture(); s.regular(""); s.files["/project/a"][step .. "_error"] = "EACCES"
        s.publish("file:///project/a")
        unavailable(s, step == "open" and "disk_unreadable" or "disk_" .. step .. "_failed")
    end
end)
test("descriptor_reclassification_prevents_directory_read", function()
    local s = fixture(); s.regular(""); s.files["/project/a"].opened_type = "directory"
    s.publish("file:///project/a"); unavailable(s, "disk_not_regular_file")
    assert(s.reads == 0)
end)
test("unnamed_and_special_buffers_have_reasons", function()
    for _, case in ipairs({ { path = "", reason = "unnamed_buffer" },
        { path = "/project/a", buftype = "nofile", reason = "non_file_buffer" } }) do
        local s = fixture(); s.regular(""); s.buffers[2] = case
        s.client.attached_buffers[2] = true
        local handler = s.client:_resolve_handler("$/progress")
        handler(nil, {}, {})
        unavailable(s, case.reason)
    end
end)
test("fast_producer_callback_uses_no_synchronous_neovim_api", function()
    local s = fixture(); s.regular(""); s.fast = true
    s.publish("file:///project/a", nil, "tail", nil)
    s.callbacks.DiagnosticChanged({ buf = 2, data = { diagnostics = {} } })
    s.fast = false
    local cache = host.json.decode(s.events[#s.events])
    local event = host.json.decode(s.events[#s.events - 2])
    assert(cache.fast_event and cache.kind == "diagnostic_cache")
    assert(event.fast_event and event.snapshot_skipped == "fast_event")
    assert(s.unsafe_calls == 0 and s.opens == 0 and not s.failed())
end)
test("callback_error_object_is_preserved", function()
    local s = fixture(); s.throw = {}
    local handler = s.client:_resolve_handler("textDocument/publishDiagnostics")
    local ok, err = pcall(handler, nil, { uri = "untitled:x" }, {}, nil)
    assert(not ok and err == s.throw and not s.failed())
end)
test("document_and_buffer_count_limits_accept_exact_boundary", function()
    local s = fixture(); s.regular(string.rep("x", 65536))
    s.buffers[2].lines, s.buffers[2].size = { string.rep("x", 65536) }, 65536
    s.publish("file:///project/a")
    local image = s.snapshot()
    assert(image.disk_sha256 == image.postimage_sha256 and not s.failed())
    s = fixture()
    for b = 1, 32 do
        s.client.attached_buffers[b] = true
        s.buffers[b] = { path = "" }
    end
    local handler = s.client:_resolve_handler("$/progress")
    handler(nil, {}, {})
    assert(not s.failed(), "exact attached-buffer limit rejected")
end)
test("document_and_buffer_count_bounds_remain_hard_failures", function()
    local s = fixture(); s.regular(""); s.files["/project/a"].size = 65537
    s.publish("file:///project/a"); assert(s.failed())
    s = fixture(); s.regular(""); s.buffers[2].size = 65537
    s.publish("file:///project/a"); assert(s.failed(), "buffer byte cap weakened")
    s = fixture(); s.regular(string.rep("x", 65537)); s.files["/project/a"].size = 0
    s.publish("file:///project/a"); assert(s.failed(), "read byte cap weakened")
    s = fixture(); s.regular(""); s.buffers[2].lines = { string.rep("x", 65537) }
    s.publish("file:///project/a"); assert(s.failed(), "postimage byte cap weakened")
    s = fixture()
    for b = 1, 33 do s.client.attached_buffers[b] = true end
    local handler = s.client:_resolve_handler("$/progress")
    handler(nil, {}, {})
    assert(s.failed(), "attached-buffer cap was weakened")
end)
test("malformed_partial_and_overcap_capture_remain_hard_failures", function()
    local s = fixture()
    local handler = s.client:_resolve_handler("textDocument/publishDiagnostics")
    handler(nil, { uri = "untitled:x", malformed = function() end }, {})
    assert(s.failed(), "malformed JSON capture accepted")
    s = fixture(); s.partial_write = true; s.publish("untitled:x"); assert(s.failed())
    s = fixture(); s.encoded_size = 262144; s.publish("untitled:x"); assert(s.failed(), "record cap weakened")
    s = fixture(); s.encoded_size = 131072
    for _ = 1, 34 do s.publish("untitled:x") end
    assert(s.failed(), "byte cap weakened")
    s = fixture()
    for _ = 1, 2049 do s.publish("untitled:x") end
    assert(s.failed(), "event cap weakened")
end)
io.stdout:write("unit_langprobe_trace: OK\n")
vim.cmd("quit")
