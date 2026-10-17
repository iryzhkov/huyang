-- Process bounds installed before the user's init, including auto-attach.
local M = {}
local entries, requests, evicted_buffers = {}, {}, {}
local limit, pinned, installed = 4, false, false
local sequence = 0
local raw_clients

local function current()
    local co = coroutine.running()
    return co and requests[co] or nil
end

local function touch(client, request)
    local entry = entries[client.id]
    if not entry then return end
    sequence = sequence + 1
    entry.used = sequence
    if request then request.clients[client.id] = true end
end

function M.begin_request()
    requests[coroutine.running()] = { clients = {}, buffers = {} }
end

function M.end_request()
    requests[coroutine.running()] = nil
end

function M.touch_buffer(bufnr)
    local request = current()
    if request then request.buffers[bufnr] = true end
    if raw_clients then
        for _, client in ipairs(raw_clients({ bufnr = bufnr })) do touch(client, request) end
    end
end

-- A loaded buffer does not fire FileType on its next load. Replay it when
-- revisiting a buffer whose server was evicted, until that server reattaches.
function M.reattach_buffer(bufnr)
    if evicted_buffers[bufnr] then
        vim.api.nvim_exec_autocmds("FileType", { buffer = bufnr, modeline = false })
    end
end

local function held(entry)
    local client = entry.client
    if pinned or not client or entry.stopping or not client.initialized then return true end
    if next(client.requests or {}) then return true end
    if client.progress and next(client.progress.pending or {}) then return true end
    local transaction = package.loaded["huyang.transaction"]
    if transaction and transaction.status().active then return true end
    for _, request in pairs(requests) do
        if request.clients[client.id] then return true end
        for buf in pairs(client.attached_buffers or {}) do
            if request.buffers[buf] then return true end
        end
    end
    for buf in pairs(client.attached_buffers or {}) do
        if vim.api.nvim_buf_is_valid(buf) and vim.bo[buf].modified then return true end
    end
    return false
end

local function count()
    local n = 0
    for _ in pairs(entries) do n = n + 1 end
    return n
end

local function capacity_error(message, config)
    local caller = current()
    if caller then
        caller.error = message
    else
        -- FileType/root callbacks can run outside the tool coroutine.
        -- Only callers awaiting a matching buffer inherit that refusal.
        for _, request in pairs(requests) do
            for buf in pairs(request.buffers) do
                if vim.api.nvim_buf_is_valid(buf) and (not config.filetypes
                    or vim.tbl_contains(config.filetypes, vim.bo[buf].filetype)) then
                    request.error = message
                end
            end
        end
    end
    error(message, 0)
end

function M.request_error()
    local request = current()
    return request and request.error or nil
end

local function reserve(config)
    while count() >= limit do
        local victim
        for _, entry in pairs(entries) do
            if not held(entry) and (not victim or entry.used < victim.used) then victim = entry end
        end
        if not victim then
            capacity_error("language_server_capacity: all " .. limit
                .. " server slots are active, starting, stopping or held; finish work or increase --max-language-servers", config)
        end
        victim.stopping = true
        for buf in pairs(victim.client.attached_buffers or {}) do
            evicted_buffers[buf] = evicted_buffers[buf] or {}
            evicted_buffers[buf][victim.client.name] = true
        end
        victim.client:stop(true)
        -- A stop request alone is not proof of process exit. Keep counting
        -- this slot until on_exit, including during nested event-loop work.
        if not vim.wait(1000, function() return victim.exited end, 10) then
            capacity_error("language_server_capacity: evicted server has not exited; retry after it stops", config)
        end
    end
    sequence = sequence + 1
    local entry = { used = sequence }
    -- A unique temporary key reserves capacity even if custom cmd code reenters.
    entries[entry] = entry
    return entry
end

function M.setup(max_servers, hold)
    assert(type(max_servers) == "number" and max_servers >= 1 and max_servers % 1 == 0)
    limit, pinned = max_servers, hold == true
    if installed then return end
    installed = true
    raw_clients = vim.lsp.get_clients
    vim.lsp.get_clients = function(filter)
        local clients = raw_clients(filter)
        local request = current()
        if request then
            for _, client in ipairs(clients) do touch(client, request) end
        end
        return clients
    end
    local Client = require("vim.lsp.client")
    local create = Client.create
    Client.create = function(config)
        local entry = reserve(config)
        config = vim.tbl_extend("force", {}, config)
        local on_exit = config.on_exit
        config.on_exit = function(...)
            entries[entry] = nil
            if entry.client then entries[entry.client.id] = nil end
            entry.exited = true
            if type(on_exit) == "function" then on_exit(...)
            elseif type(on_exit) == "table" then
                for _, callback in ipairs(on_exit) do pcall(callback, ...) end
            end
        end
        local ok, client = pcall(create, config)
        if not ok or not client then
            entries[entry] = nil
            if not ok then error(client, 0) end
            return client
        end
        entries[entry] = nil
        if not entry.exited then entries[client.id] = entry end
        entry.client = client
        touch(client, current())
        return client
    end
    local start = vim.lsp.start
    vim.lsp.start = function(config, opts)
        local id = start(config, opts)
        if id then
            local client = vim.lsp.get_client_by_id(id)
            if client then touch(client, current()) end
        end
        return id
    end
    vim.api.nvim_create_autocmd("BufWipeout", { callback = function(ev) evicted_buffers[ev.buf] = nil end })
    vim.api.nvim_create_autocmd("LspAttach", { callback = function(ev)
        local client = vim.lsp.get_client_by_id(ev.data.client_id)
        if not client then return end
        local evicted = evicted_buffers[ev.buf]
        if evicted then
            evicted[client.name] = nil
            if not next(evicted) then evicted_buffers[ev.buf] = nil end
        end
        for _, request in pairs(requests) do
            if request.buffers[ev.buf] then touch(client, request) end
        end
    end })
end

return M
