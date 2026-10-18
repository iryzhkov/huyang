-- Pull responses carry no producer document version. This module only fetches
-- observations; callers decide what the payload can prove.
local M = {}
local observer = require("huyang.diagnostic_observer")
local watches = {}
local MAX_WATCHES, MAX_ATTEMPTS, WATCH_MS = 64, 16, 30000

local function close(watch)
    if watches[watch.key] ~= watch then return end
    watches[watch.key] = nil
    watch.timer:stop()
    watch.timer:close()
    local request_id = watch.request_id
    watch.generation = watch.generation + 1
    watch.inflight, watch.request_id = false, nil
    if request_id and watch.client.cancel_request then
        watch.client:cancel_request(request_id)
    end
    if watch.interrupted then watch.interrupted() end
end

function M.client_current(client, bufnr)
    return vim.lsp.get_client_by_id(client.id) == client
        and (not client.attached_buffers or not not client.attached_buffers[bufnr])
end

local function current(watch)
    return watches[watch.key] == watch
        and M.client_current(watch.client, watch.bufnr)
        and watch.current()
end

local pump
local function schedule(watch)
    if watches[watch.key] ~= watch then return end
    if watch.inflight then watch.again = true; return end
    if watch.scheduled then return end
    watch.scheduled = true
    vim.schedule(function()
        watch.scheduled = false
        pump(watch)
    end)
end

-- Neovim's native refresh may cancel a request and discard its callback.
-- Retire that generation before scheduling its replacement; a late callback
-- from the old request must never clear the replacement's in-flight state.
local function interrupted(watch)
    if not watch.inflight then return end
    watch.generation = watch.generation + 1
    watch.inflight, watch.request_id, watch.again = false, nil, false
    if watch.interrupted then watch.interrupted() end
    schedule(watch)
end

function M.request_cancelled(client_id, request_id)
    for _, watch in pairs(watches) do
        if watch.client.id == client_id and watch.request_id == request_id then interrupted(watch) end
    end
end

pump = function(watch)
    if not current(watch) or watch.attempts >= MAX_ATTEMPTS then close(watch); return end
    if watch.inflight then return end
    watch.inflight, watch.again = true, false
    watch.attempts = watch.attempts + 1
    watch.generation = watch.generation + 1
    local generation = watch.generation
    local sent, request_id = watch.client:request("textDocument/diagnostic", watch.params,
        function(lsp_err, result)
            if generation ~= watch.generation or not watch.inflight then return end
            watch.inflight, watch.request_id = false, nil
            if not current(watch) then close(watch); return end
            observer.receive({ client_id = watch.client.id, document = watch.document,
                generation = generation }, not lsp_err and result or nil, watch.receive)
            -- A refresh/progress event during the request may describe newer
            -- work than its response. Fetch again, without inferring completion.
            if watch.again then schedule(watch) end
        end, watch.bufnr)
    -- In-process servers can answer synchronously.
    if generation ~= watch.generation then return end
    if sent and watch.inflight then watch.request_id = request_id end
    if not sent then
        watch.inflight = false
        if current(watch) then watch.receive(nil) end
        close(watch)
    end
end

function M.start(args)
    local key = tostring(args.client.id) .. "\0" .. args.document
    if watches[key] then close(watches[key]) end
    local count, oldest = 0, nil
    for _, watch in pairs(watches) do
        count = count + 1
        if not oldest or watch.at < oldest.at then oldest = watch end
    end
    if count >= MAX_WATCHES then close(oldest) end
    local watch = {
        key = key, document = args.document, client = args.client, bufnr = args.bufnr,
        params = args.params, current = args.current, receive = args.receive,
        interrupted = args.interrupted, generation = 0,
        at = vim.uv.now(), attempts = 0, timer = vim.uv.new_timer(),
    }
    watches[key] = watch
    -- Expiry bounds retained state and outstanding requests; it proves nothing.
    watch.timer:start(WATCH_MS, 0, vim.schedule_wrap(function() close(watch) end))
    pump(watch)
end

-- Only producer events and later evidence collections trigger another fetch.
-- No timer, silence or opaque result ID ever promotes a response to complete.
function M.wake(client_id, reason)
    for _, watch in pairs(watches) do
        if (client_id == nil or watch.client.id == client_id)
            and (reason ~= "progress" or not watch.progress_woken) then
            -- Pulls can themselves provoke progress. Permit one progress
            -- follow-up per start/explicit wake, never a recursive chain.
            -- Explicit collection/refresh re-arms even an in-flight watch.
            watch.progress_woken = reason == "progress"
            local requests = watch.client.requests
            local request = requests and requests[watch.request_id]
            if watch.inflight and watch.request_id and requests
                and (not request or request.type == "cancel") then
                interrupted(watch)
            else
                schedule(watch)
            end
        end
    end
end

function M.cancel_document(document, client_id)
    local cancelled = {}
    for _, watch in pairs(watches) do
        if watch.document == document and (client_id == nil or watch.client.id == client_id) then
            cancelled[#cancelled + 1] = watch
        end
    end
    for _, watch in ipairs(cancelled) do close(watch) end
end

return M
