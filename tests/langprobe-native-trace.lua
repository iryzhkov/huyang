-- Observation only. Loaded before vim.lsp.enable by the private probe init.
-- Local snapshots are correlation data, never producer version/clean proof.
local dir = assert(vim.env.HUYANG_LANGPROBE_NATIVE_TRACE)
local scope = assert(vim.env.HUYANG_LANGPROBE_NATIVE_SCOPE)
local pid = vim.fn.getpid()
local path = dir .. "/native-" .. scope .. "-" .. pid .. ".jsonl"
local MAX_BYTES, MAX_EVENTS, MAX_RECORD = 8 * 1024 * 1024, 4096, 256 * 1024
local MAX_DOCUMENT, MAX_BUFFERS = 64 * 1024, 32
local sequence, bytes, failed = 0, 0, false
local fd, open_error = vim.uv.fs_open(path, "wx", 384)

local function failure(reason)
    if failed then return end
    failed = true
    -- Separate, bounded failure marker also works after the JSONL cap or a
    -- partial write. A missing/malformed trace independently fails archival.
    pcall(function()
        local message = tostring(reason):sub(1, 2048)
        local f = io.open(path .. ".failure", "w")
        if f then f:write(message, "\n"); f:close() end
        io.stderr:write("LANGPROBE_OBSERVATION_FAILED ", path, ": ", message, "\n")
    end)
end
if not fd then failure(open_error) end

-- Separate the native Neovim log too. This changes only local logging,
-- never LSP trace/capabilities/settings or any producer message.
local lsp_log = dir .. "/lsp-" .. scope .. "-" .. pid .. ".log"
local log_ok, log_error = pcall(function()
    assert(type(vim.lsp.log._set_filename) == "function", "native LSP log path hook unavailable")
    vim.lsp.log._set_filename(lsp_log)
    vim.lsp.log.set_level("debug")
end)
if not log_ok then failure(log_error) end

local function pack(...) return { n = select("#", ...), ... } end
local function values(p, first)
    local out = {}
    for i = first or 1, p.n do out[#out + 1] = p[i] == nil and vim.NIL or p[i] end
    return out
end
local function write(event)
    if failed then return end
    sequence = sequence + 1
    event.trace_seq, event.process_id, event.scope = sequence, pid, scope
    event.monotonic_ns = tostring(vim.uv.hrtime())
    local sec, usec = vim.uv.gettimeofday()
    if type(sec) == "table" then usec, sec = sec.usec, sec.sec end
    event.unix_us = sec * 1000000 + usec
    local line = vim.json.encode(event) .. "\n"
    if sequence > MAX_EVENTS or #line > MAX_RECORD or bytes + #line > MAX_BYTES then
        failure("trace truncated: events/record/bytes bound exceeded at event " .. sequence)
        return
    end
    if not fd then failure("trace file is not open"); return end
    local n, err = vim.uv.fs_write(fd, line, -1)
    if n ~= #line then failure(err or "partial JSONL write"); return end
    bytes = bytes + n
end
-- Observation errors never escape into a native callback or replace its result.
local function observe(make)
    if failed then return end
    local ok, err = pcall(function() write(make()) end)
    if not ok then failure(err) end
end

local function snapshot(client, bufnr, params)
    local doc = type(params) == "table" and params.textDocument
    local uri = type(doc) == "table" and doc.uri
        or (type(params) == "table" and params.uri)
    if (not bufnr or bufnr == 0) and uri then bufnr = vim.fn.bufnr(vim.uri_to_fname(uri)) end
    local out = { uri = uri, bufnr = bufnr, local_observation = true,
        client_current = vim.lsp.get_client_by_id(client.id) == client }
    if not bufnr or not vim.api.nvim_buf_is_valid(bufnr) then return out end
    out.path = vim.api.nvim_buf_get_name(bufnr)
    out.loaded = vim.api.nvim_buf_is_loaded(bufnr)
    out.attached = not not client.attached_buffers[bufnr]
    out.changedtick = vim.api.nvim_buf_get_changedtick(bufnr)
    out.local_version = vim.lsp.util.buf_versions[bufnr]
    if out.loaded then
        local size = vim.api.nvim_buf_get_offset(bufnr, vim.api.nvim_buf_line_count(bufnr))
        if size < 0 or size > MAX_DOCUMENT then error("snapshot buffer exceeds 64 KiB or size unavailable") end
        local body = table.concat(vim.api.nvim_buf_get_lines(bufnr, 0, -1, false), "\n")
        if vim.bo[bufnr].endofline then body = body .. "\n" end
        if #body > MAX_DOCUMENT then error("snapshot postimage exceeds 64 KiB") end
        out.postimage_sha256 = vim.fn.sha256(body)
    end
    if out.path ~= "" then
        local f, err = io.open(out.path, "rb")
        if not f then error("snapshot disk open failed: " .. tostring(err)) end
        local body = f:read(MAX_DOCUMENT + 1)
        local closed, close_error = f:close()
        if not closed then error(close_error) end
        if not body then error("snapshot disk read failed") end
        if #body > MAX_DOCUMENT then error("snapshot disk exceeds 64 KiB") end
        out.disk_sha256 = vim.fn.sha256(body)
    end
    return out
end
local function snapshots(client, bufnr, params)
    if bufnr or (type(params) == "table" and (params.textDocument or params.uri)) then
        return { snapshot(client, bufnr, params) }
    end
    local out = {}
    for buf in pairs(client.attached_buffers) do
        if #out >= MAX_BUFFERS then error("snapshot exceeds 32 attached buffers") end
        out[#out + 1] = snapshot(client, buf)
    end
    return out
end
local function client_event(client, kind, method, params, bufnr, extra)
    local event = extra or {}
    event.kind, event.method, event.client_id, event.client_name = kind, method, client.id, client.name
    event.server_process_id = client.rpc and client.rpc.pid or vim.NIL
    if vim.in_fast_event() then
        event.fast_event, event.snapshots, event.snapshot_skipped = true, vim.NIL, "fast_event"
        return event
    end
    event.snapshots = snapshots(client, bufnr, params)
    return event
end
-- pcall is yield-safe in Neovim's LuaJIT. Preserve all returns, nil slots,
-- thrown error objects and original argument counts for every wrapped call.
local function forward(fn, after, ...)
    local result = pack(pcall(fn, ...))
    observe(function() return after(result) end)
    if not result[1] then error(result[2], 0) end
    return unpack(result, 2, result.n)
end

local observed = setmetatable({}, { __mode = "k" })
local function on_init(client)
    if observed[client] then return end
    observed[client] = true
    local request, cancel, notify, resolve = client.request, client.cancel_request, client.notify, client._resolve_handler
    local request_sequence = 0
    client.request = function(...)
        local args = pack(...)
        -- Keep deprecated dot calls in the original bound method wrapper.
        if args[1] ~= client then return request(unpack(args, 1, args.n)) end
        local self, method, params, handler, bufnr = unpack(args, 1, 5)
        request_sequence = request_sequence + 1
        local local_id = request_sequence
        local rpc_id
        observe(function()
            return client_event(self, "request", method, params, bufnr,
                { request_sequence = local_id, params = params, argument_count = args.n })
        end)
        local inner = handler or self:_resolve_handler(method)
        if inner then
            handler = function(...)
                local callback_args = pack(...)
                observe(function()
                    return client_event(self, "callback", method, params, bufnr,
                        { request_sequence = local_id, request_id = rpc_id or vim.NIL,
                          callback = values(callback_args), err = callback_args[1] == nil and vim.NIL or callback_args[1],
                          result = callback_args[2] == nil and vim.NIL or callback_args[2], context = callback_args[3] })
                end)
                return forward(inner, function(result)
                    return { kind = "callback_return", client_id = self.id, request_sequence = local_id,
                        request_id = rpc_id or vim.NIL, ok = result[1], returns = values(result, 2) }
                end, unpack(callback_args, 1, callback_args.n))
            end
        end
        if inner then args[4] = handler end
        return forward(request, function(result)
            if result[1] then rpc_id = result[3] end
            return client_event(self, "dispatch", method, params, bufnr,
                { request_sequence = local_id, request_id = rpc_id or vim.NIL,
                  ok = result[1], returns = values(result, 2), callback_observed = inner ~= nil and args.n >= 4 })
        end, unpack(args, 1, args.n))
    end
    client.cancel_request = function(...)
        local args = pack(...)
        if args[1] ~= client then return cancel(unpack(args, 1, args.n)) end
        local self, id = args[1], args[2]
        observe(function() return client_event(self, "cancel", nil, nil, nil, { request_id = id }) end)
        return forward(cancel, function(result)
            return { kind = "cancel_return", client_id = self.id, request_id = id,
                ok = result[1], returns = values(result, 2) }
        end, unpack(args, 1, args.n))
    end
    client.notify = function(...)
        local args = pack(...)
        if args[1] ~= client then return notify(unpack(args, 1, args.n)) end
        local self, method, params = unpack(args, 1, 3)
        observe(function() return client_event(self, "notify", method, params, nil, { params = params }) end)
        return forward(notify, function(result)
            return client_event(self, "notify_return", method, params, nil,
                { ok = result[1], returns = values(result, 2) })
        end, unpack(args, 1, args.n))
    end
    -- Resolve the current handler each time. Do not replace the configured
    -- handler table or root/settings/capabilities, including Huyang's wrappers.
    client._resolve_handler = function(self, method, ...)
        local resolved = pack(resolve(self, method, ...))
        local handler = resolved[1]
        if not handler or (method ~= "textDocument/publishDiagnostics"
            and method ~= "workspace/diagnostic/refresh" and method ~= "$/progress") then
            return unpack(resolved, 1, resolved.n)
        end
        resolved[1] = function(...)
            local args = pack(...)
            observe(function()
                return client_event(self, "producer", method, args[2], nil,
                    { err = args[1] == nil and vim.NIL or args[1],
                      params = args[2] == nil and vim.NIL or args[2], context = args[3] })
            end)
            return forward(handler, function(result)
                return { kind = "producer_return", method = method, client_id = self.id,
                    ok = result[1], returns = values(result, 2) }
            end, unpack(args, 1, args.n))
        end
        return unpack(resolved, 1, resolved.n)
    end
    for _, method in ipairs({ "_on_error", "_on_exit" }) do
        local original = client[method]
        client[method] = function(...)
            local args = pack(...)
            observe(function()
                return client_event(client, method, nil, nil, nil, { arguments = values(args, 2) })
            end)
            return forward(original, function(result)
                return { kind = method .. "_return", client_id = client.id,
                    ok = result[1], returns = values(result, 2) }
            end, unpack(args, 1, args.n))
        end
    end
    observe(function()
        return client_event(client, "client_init", nil, nil, nil,
            { root = client.config.root_dir, command = type(client.config.cmd) == "table" and client.config.cmd or "<function>",
              settings = client.config.settings,
              capabilities = client.config.capabilities, server_capabilities = client.server_capabilities,
              server_info = client.server_info })
    end)
end

-- Add the same generic on_init callback to every actual client creation.
-- Keep the resolved config intact and let Neovim invoke existing callbacks.
local Client = require("vim.lsp.client")
---@type fun(config: vim.lsp.ClientConfig, ...): vim.lsp.Client?
local create = Client.create
Client.create = function(config, ...)
    local copy = vim.tbl_extend("force", {}, config)
    ---@type function[]
    local callbacks = { function(client)
        local ok, err = pcall(on_init, client)
        if not ok then failure(err) end
    end }
    local existing = config.on_init
    if type(existing) == "table" then
        for _, callback in ipairs(existing --[[@as function[] ]]) do callbacks[#callbacks + 1] = callback end
    elseif existing ~= nil then
        callbacks[#callbacks + 1] = existing --[[@as function]]
    end
    copy.on_init = callbacks
    return create(copy, ...)
end

for _, event in ipairs({ "LspAttach", "LspDetach", "LspProgress" }) do
    vim.api.nvim_create_autocmd(event, { callback = function(ev)
        observe(function()
            local client = vim.lsp.get_client_by_id(ev.data.client_id)
            if client then return client_event(client, event, nil, nil,
                ev.buf and ev.buf > 0 and ev.buf or nil, { data = ev.data }) end
            return { kind = event, client_id = ev.data.client_id, data = ev.data, bufnr = ev.buf }
        end)
    end })
end

vim.api.nvim_create_autocmd("DiagnosticChanged", { callback = function(ev)
    observe(function() return { kind = "diagnostic_cache", bufnr = ev.buf,
        changedtick = vim.api.nvim_buf_get_changedtick(ev.buf),
        event_diagnostics = ev.data and ev.data.diagnostics,
        diagnostics = vim.diagnostic.get(ev.buf) } end)
end })

-- Observe the existing RPC envelope at its actual send boundary, including
-- structured failures/cancellation. Do not require production modules early.
local late, late_sequence = {}, 0
local original_require = _G.require
local rpc_observed = false
_G.require = function(...)
    local args = pack(...)
    local result = pack(original_require(unpack(args, 1, args.n)))
    if args[1] == "huyang.rpc" and not rpc_observed then
        rpc_observed = true
        local ok, err = pcall(function()
            local rpc = result[1]
            local start, cancel = rpc.start_notify, rpc.cancel
            rpc.start_notify = function(...)
                local call_args = pack(...)
                local channel, id, payload = unpack(call_args, 1, 3)
                if type(payload) == "table" and payload.tool == "huyang_late_evidence" then
                    late_sequence = late_sequence + 1
                    late[id] = late_sequence
                    observe(function() return { kind = "late_start", request_id = id,
                        collection_sequence = late[id], channel = channel, payload = payload } end)
                end
                return forward(start, function(ret)
                    return { kind = "rpc_start_return", request_id = id, ok = ret[1], returns = values(ret, 2) }
                end, unpack(call_args, 1, call_args.n))
            end
            rpc.cancel = function(id, ...)
                if late[id] then
                    local rest = pack(...)
                    observe(function() return { kind = "late_cancel", request_id = id,
                        collection_sequence = late[id], arguments = values(rest) } end)
                end
                return cancel(id, ...)
            end
            observe(function() return { kind = "rpc_observer_ready" } end)
        end)
        if not ok then failure(err) end
    end
    return unpack(result, 1, result.n)
end
local rpcnotify = vim.rpcnotify
-- Intentional observation wrapper around the existing send boundary.
---@diagnostic disable-next-line: duplicate-set-field
vim.rpcnotify = function(...)
    local args = pack(...)
    local channel, method, id, response = unpack(args, 1, 4)
    if method ~= "huyang/result" or not late[id] then
        return rpcnotify(unpack(args, 1, args.n))
    end
    local collection = late[id]
    observe(function() return { kind = "late_result", request_id = id, collection_sequence = collection,
        channel = channel, response = response } end)
    return forward(rpcnotify, function(result)
        late[id] = nil
        return { kind = "late_send", request_id = id, collection_sequence = collection,
            ok = result[1], returns = values(result, 2) }
    end, unpack(args, 1, args.n))
end
observe(function() return { kind = "observer_ready", lsp_log = vim.lsp.log.get_filename(),
    limits = { bytes = MAX_BYTES, events = MAX_EVENTS, record_bytes = MAX_RECORD,
        document_bytes = MAX_DOCUMENT, attached_buffers = MAX_BUFFERS } } end)
