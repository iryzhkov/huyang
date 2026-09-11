-- RPC entry point for the embedded provider.
--
-- The Huyang service owns this Neovim over its embedded msgpack channel. It
-- calls start_notify(channel, id, payload), which runs the tool in a
-- coroutine and reports the JSON reply back with a notification on that
-- channel, so the editor's main loop stays free while LSP work is in flight.
--
-- Payloads are plain tables ({tool = ..., args = {...}}) decoded by the
-- msgpack transport.

local M = {}

local PROTOCOL_VERSION = 1
local RESULT_METHOD = "agent99/result"

local function response_payload(ok, result)
    local response = ok and { ok = true, result = result }
        or { ok = false, error = tostring(result) }
    local okj, payload = pcall(vim.json.encode, response)
    if not okj then
        payload = vim.json.encode({
            ok = false,
            error = "result not encodable as JSON: " .. tostring(payload),
        })
    end
    return payload
end

local function dispatch(payload, complete)
    local co = coroutine.create(function()
        -- LuaJIT pcall is yield-safe, so this catches errors from any resume.
        local ok, result = pcall(function()
            return require("huyang.lsp").dispatch(payload.tool, payload.args)
        end)
        complete(response_payload(ok, result))
    end)
    local ok, cerr = coroutine.resume(co)
    if not ok then
        complete(response_payload(false, cerr))
    end
end

function M.handshake()
    return {
        protocol_version = PROTOCOL_VERSION,
        completion_method = RESULT_METHOD,
        cancellation = "provider_restart",
        capabilities = { "execute", "navigation", "rename", "diagnostics", "code_actions" },
    }
end

function M.start_notify(channel, id, payload)
    vim.schedule(function()
        dispatch(payload, function(response)
            vim.rpcnotify(channel, RESULT_METHOD, id, response)
        end)
    end)
    return id
end

return M
