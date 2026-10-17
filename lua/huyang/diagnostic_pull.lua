-- Pull responses carry no producer document version. This module only fetches
-- observations; callers decide what the payload can prove.
local M = {}
local watches = {}
local MAX_WATCHES, MAX_ATTEMPTS, WATCH_MS = 64, 16, 30000

local function close(watch)
    if watches[watch.key] ~= watch then return end
    watches[watch.key] = nil
    watch.timer:stop()
    watch.timer:close()
    if watch.request_id and watch.client.cancel_request then
        watch.client:cancel_request(watch.request_id)
    end
end

function M.client_current(client, bufnr)
    return vim.lsp.get_client_by_id(client.id) == client
        and (not client.attached_buffers or client.attached_buffers[bufnr] == true)
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

pump = function(watch)
    if not current(watch) or watch.attempts >= MAX_ATTEMPTS then close(watch); return end
    if watch.inflight then return end
    watch.inflight, watch.again = true, false
    watch.attempts = watch.attempts + 1
    local sent, request_id = watch.client:request("textDocument/diagnostic", watch.params,
        function(lsp_err, result)
            watch.inflight, watch.request_id = false, nil
            if not current(watch) then close(watch); return end
            watch.receive(not lsp_err and result or nil)
            -- A refresh/progress event during the request may describe newer
            -- work than its response. Fetch again, without inferring completion.
            if watch.again then schedule(watch) end
        end, watch.bufnr)
    -- In-process servers can answer synchronously.
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
        at = vim.uv.now(), attempts = 0, timer = vim.uv.new_timer(),
    }
    watches[key] = watch
    -- Expiry bounds retained state and outstanding requests; it proves nothing.
    watch.timer:start(WATCH_MS, 0, vim.schedule_wrap(function() close(watch) end))
    pump(watch)
end

-- Only producer events and later evidence collections trigger another fetch.
-- No timer, silence or opaque result ID ever promotes a response to complete.
function M.wake(client_id)
    for _, watch in pairs(watches) do
        if client_id == nil or watch.client.id == client_id then schedule(watch) end
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
