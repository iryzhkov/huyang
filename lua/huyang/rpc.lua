-- RPC entry point for the embedded provider.
--
-- The Huyang service owns this Neovim over its embedded msgpack channel. It
-- calls start_notify(channel, id, payload), which runs the tool in a
-- coroutine and reports the completion back with a notification on that
-- channel, so the editor's main loop stays free while LSP work is in flight.
--
-- Payloads are plain tables ({tool = ..., args = {...}, context = {...}})
-- decoded by the msgpack transport, and completions are plain tables encoded
-- by it: vim.rpcnotify takes Lua tables directly, so nothing is JSON-encoded
-- on the way out.
--
-- Protocol version 2 (the Go side in internal/provider/embed accepts exactly
-- this range):
--
--   start_notify(channel, id, payload) -> id
--     payload.tool     the dispatch name (huyang.lsp)
--     payload.args     the tool's arguments
--     payload.context  request_id, actor, workspace_id, epoch, transaction_id,
--                      deadline_ms (Unix milliseconds, 0 when none) and
--                      cancellation, all optional
--   cancel(id, reason) -> true when the request was pending
--   rpcnotify(channel, "huyang/result", id, completion)
--     completion.ok        boolean
--     completion.result    the tool's value when ok
--     completion.error     { code, message, detail } when not ok
--     completion.touched   documents this request changed or staged:
--                          { uri, path, changedtick, sha256, disk_fingerprint,
--                            dirty, exists }
--     completion.evidence  diagnostic batches the tool collected, in the
--                          huyang_diagnostic_evidence shape
--     completion.health    { state, lsp_clients, transaction }
--
-- Cancellation is cooperative: cancel(id) marks the request and wakes the
-- coroutine if it is parked in core.await, and the next await raises a
-- structured cancelled error. Work that never yields (a long synchronous
-- loop) cannot be interrupted; the Go side falls back to replacing the
-- provider generation when no completion follows within its grace period.

local M = {}

local PROTOCOL_VERSION = 2
local RESULT_METHOD = "huyang/result"
local CANCELLED_CODE = "provider_cancelled"

-- Per-request state, keyed by the coroutine that runs the request so two
-- interleaved requests never see each other's context. Weak keys: a finished
-- coroutine takes its entry with it.
local by_co = setmetatable({}, { __mode = "k" })
-- Pending requests by id, for cancel().
local pending = {}

local function co_key()
    local co, main = coroutine.running()
    if co == nil or main == true then return nil end
    return co
end

local function state()
    local co = co_key()
    return co and by_co[co] or nil
end

--- The request context the Go side sent, or an empty table outside a request.
function M.context()
    local s = state()
    return s and s.context or {}
end

--- The transaction this request runs in, or nil for a canonical call. The
--- bridge sets it for staged-view reads and transaction operations; a call
--- without one reads and writes the canonical buffers.
function M.transaction_id()
    local id = M.context().transaction_id
    if type(id) == "string" and id ~= "" then return id end
    return nil
end

--- Whether the running request has been cancelled.
function M.cancelled()
    local s = state()
    return s ~= nil and s.cancelled ~= nil
end

--- Raise the structured cancelled error when the running request has been
--- cancelled. core.await calls this on every yield boundary.
function M.check_cancelled()
    local s = state()
    if s and s.cancelled then
        error({
            code = CANCELLED_CODE,
            message = "request " .. tostring(s.id) .. " was cancelled",
            detail = s.cancelled,
        }, 0)
    end
end

--- Park the running request: core.await registers the resume function it
--- yields on so cancel() can wake the coroutine without waiting for the LSP
--- reply or timer the await was for.
function M.suspend(wake)
    local s = state()
    if s then s.wake = wake end
end

--- The request is running again after a yield.
function M.resume()
    local s = state()
    if s then s.wake = nil end
    M.check_cancelled()
end

--- Record a buffer the running request changed or staged, so the completion
--- names it with the changedtick and content hash the Go side checks
--- revisions against.
function M.touch(bufnr)
    local s = state()
    if not s or type(bufnr) ~= "number" or not vim.api.nvim_buf_is_valid(bufnr) then return end
    s.touched = s.touched or {}
    -- The path is kept from the moment of touching: a rollback that unloads
    -- a buffer it had loaded still has to name the file in the completion.
    s.touched[bufnr] = vim.api.nvim_buf_get_name(bufnr)
end

--- Attach diagnostic evidence batches (huyang_diagnostic_evidence shape) to
--- the completion.
function M.report_evidence(batches)
    local s = state()
    if not s or type(batches) ~= "table" then return end
    s.evidence = s.evidence or {}
    for _, batch in ipairs(batches) do
        s.evidence[#s.evidence + 1] = batch
    end
end

local function snapshot(bufnr, touched_path)
    if not vim.api.nvim_buf_is_valid(bufnr) then
        -- The buffer went away after the touch (a rollback unloaded it):
        -- report the file as the disk has it.
        if touched_path == "" then return nil end
        return {
            uri = vim.uri_from_fname(touched_path),
            path = touched_path,
            changedtick = 0,
            dirty = false,
            exists = vim.uv.fs_stat(touched_path) ~= nil,
            disk_fingerprint = require("huyang.core").disk_fingerprint(touched_path),
        }
    end
    local path = vim.api.nvim_buf_get_name(bufnr)
    local snap = {
        uri = vim.uri_from_bufnr(bufnr),
        path = path,
        changedtick = vim.api.nvim_buf_get_changedtick(bufnr),
        dirty = vim.bo[bufnr].modified,
        exists = vim.b[bufnr].huyang_deleted ~= true,
    }
    if vim.api.nvim_buf_is_loaded(bufnr) then
        local lines = vim.api.nvim_buf_get_lines(bufnr, 0, -1, false)
        local content = table.concat(lines, "\n")
        if vim.bo[bufnr].eol then content = content .. "\n" end
        snap.sha256 = vim.fn.sha256(content)
    end
    if path ~= "" then
        local ok, fingerprint = pcall(require("huyang.core").disk_fingerprint, path)
        if ok and fingerprint then snap.disk_fingerprint = fingerprint end
    end
    return snap
end

local function touched_snapshots(s)
    local out = {}
    if not s.touched then return out end
    local bufs = vim.tbl_keys(s.touched)
    table.sort(bufs)
    for _, bufnr in ipairs(bufs) do
        local snap = snapshot(bufnr, s.touched[bufnr])
        if snap then out[#out + 1] = snap end
    end
    return out
end

local function health()
    local out = { state = "healthy", lsp_clients = #vim.lsp.get_clients() }
    local ok, status = pcall(function() return require("huyang.transaction").status() end)
    if ok and type(status) == "table" and status.active then
        out.transaction = status.plan_id
    end
    return out
end

-- Tools raise strings ("lsp_not_configured: ...", through core.err) and
-- occasionally tables with a message. Both become {code, message, detail}:
-- a table keeps its own code, and a string whose first word is a
-- snake_case token followed by a colon uses that token as the code.
local function structured_error(err)
    if type(err) == "table" then
        local message = err.message or err.msg
        if type(message) ~= "string" then message = vim.inspect(err) end
        return {
            code = type(err.code) == "string" and err.code or "lua_error",
            message = message,
            detail = err.detail,
        }
    end
    local message = tostring(err)
    local code = message:match("^([%l%d_]+):")
    return { code = code or "lua_error", message = message }
end

local function completion(s, ok, result)
    local out = { ok = ok, touched = touched_snapshots(s), health = health() }
    if ok then
        out.result = result
    else
        out.error = structured_error(result)
    end
    if s.evidence then out.evidence = s.evidence end
    return out
end

local function dispatch(id, payload, complete)
    local s = { id = id, context = type(payload.context) == "table" and payload.context or {} }
    -- A request with no arguments arrives as vim.NIL, which is truthy.
    local args = type(payload.args) == "table" and payload.args or {}
    local co = coroutine.create(function()
        -- LuaJIT pcall is yield-safe, so this catches errors from any resume.
        local ok, result = pcall(function()
            return require("huyang.lsp").dispatch(payload.tool, args)
        end)
        pending[id] = nil
        complete(completion(s, ok, result))
    end)
    by_co[co] = s
    pending[id] = s
    local ok, cerr = coroutine.resume(co)
    if not ok then
        pending[id] = nil
        complete(completion(s, false, cerr))
    end
end

function M.handshake()
    return {
        protocol_version = PROTOCOL_VERSION,
        completion_method = RESULT_METHOD,
        cancellation = "cooperative",
        methods = { "handshake", "start_notify", "cancel" },
        capabilities = { "execute", "navigation", "rename", "diagnostics", "code_actions" },
    }
end

function M.start_notify(channel, id, payload)
    vim.schedule(function()
        dispatch(id, payload or {}, function(response)
            vim.rpcnotify(channel, RESULT_METHOD, id, response)
        end)
    end)
    return id
end

--- Mark a pending request cancelled and wake it if it is parked in await.
--- Returns true when the request was still pending.
function M.cancel(id, reason)
    local s = pending[id]
    if not s then return false end
    s.cancelled = reason or "cancelled by the service"
    local wake = s.wake
    if wake then
        s.wake = nil
        vim.schedule(function() wake() end)
    end
    return true
end

--- Number of requests in flight, for tests and the health probe.
function M.pending_count()
    return vim.tbl_count(pending)
end

M.PROTOCOL_VERSION = PROTOCOL_VERSION
M.RESULT_METHOD = RESULT_METHOD
M.CANCELLED_CODE = CANCELLED_CODE

return M
