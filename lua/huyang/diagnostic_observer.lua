-- Optional observation sidecar. Nothing here is diagnostic proof or wire data.
-- The observer sees detached snapshots; its failure never changes delivery.
local M = {}
local sink
local main = {}
local function key() return coroutine.running() or main end
local raw_context, origin = {}, {}
local sequence, errors = 0, {}
local batches = setmetatable({}, { __mode = "k" })
local emissions = setmetatable({}, { __mode = "k" })
local unpack_values = table.unpack or unpack

local function failed(detail)
    errors[#errors + 1] = type(detail) == "string" and detail or "observer failure (non-string error)"
end

local function emit(event)
    if not sink then return end
    local ok, detail = pcall(function()
        event.telemetry_errors = #errors
        sink(vim.deepcopy(event))
    end)
    if not ok then failed(detail) end
end

-- Installing is an explicit test/debug action; normal sessions have no sink.
function M.install(observer)
    sink, raw_context, origin = observer, {}, {}
    sequence, errors = 0, {}
    batches = setmetatable({}, { __mode = "k" })
    emissions = setmetatable({}, { __mode = "k" })
end

function M.enabled() return not not sink end

function M.status()
    return { installed = sink ~= nil, errors = vim.deepcopy(errors) }
end

local function scoped(which, value, fn, ...)
    local contexts = which == "raw" and raw_context or origin
    local co, previous = key(), contexts[key()]
    contexts[co] = value
    local function pack(...) return { n = select("#", ...), ... } end
    local result = pack(pcall(fn, ...))
    contexts[co] = previous
    if not result[1] then error(result[2], 0) end
    return unpack_values(result, 2, result.n)
end

-- Only the test's real request/callback observer supplies raw identity.
function M.with_raw(context, fn, ...)
    if not sink then return fn(...) end
    local copy
    local ok, detail = pcall(function() copy = vim.deepcopy(context) end)
    if not ok then failed(detail) end
    return scoped("raw", copy, fn, ...)
end

-- Called after the watch has accepted this generation/current/inflight state.
-- No raw context means unattributed (including native/non-observed callbacks).
function M.receive(context, result, fn)
    if not sink then return fn(result) end
    local accepted
    local ok, detail = pcall(function()
        sequence = sequence + 1
        accepted = { origin_id = sequence, raw = vim.deepcopy(raw_context[key()]),
            watch = vim.deepcopy(context) }
        emit({ stage = "accepted_receive", origin = accepted, result = result })
    end)
    if not ok then failed(detail) end
    return scoped("origin", accepted, fn, result)
end

function M.construct(batch)
    if not sink then return end
    local ok, detail = pcall(function()
        sequence = sequence + 1
        batches[batch] = { batch_id = sequence, origin = vim.deepcopy(origin[key()]) }
        emit({ stage = "constructed", identity = batches[batch], batch = batch })
    end)
    if not ok then failed(detail) end
end

function M.enqueue(batch, queue_sequence)
    if not sink then return end
    local ok, detail = pcall(function()
        local identity = batches[batch]
        if identity then identity.queue_sequence = queue_sequence end
        emit({ stage = "enqueued", identity = identity, queue_sequence = queue_sequence,
            batch = batch })
    end)
    if not ok then failed(detail) end
end

function M.emission(stage, batch, normalized, queue_sequence, context)
    if not sink then return end
    local ok, detail = pcall(function()
        emissions[normalized] = batches[batch]
        emit({ stage = stage, identity = batches[batch], queue_sequence = queue_sequence,
            batch = normalized, context = context })
    end)
    if not ok then failed(detail) end
end

-- The test wrapper calls this at the actual provider notification boundary.
-- Exact normalized table identity links emission to transport, out of band.
function M.transport(rpc_id, completion)
    if not sink then return end
    local ok, detail = pcall(function()
        local list = completion.evidence
        if type(list) ~= "table" or #list == 0 then
            list = type(completion.result) == "table" and completion.result.batches or {}
        end
        for index, batch in ipairs(list) do
            emit({ stage = "transport_emission", identity = emissions[batch],
                batch = batch, provider_rpc_id = rpc_id, transport_ok = completion.ok == true,
                transport_index = index })
        end
    end)
    if not ok then failed(detail) end
end

return M
