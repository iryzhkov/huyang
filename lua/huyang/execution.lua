-- Bounded call acquisition in one protocol-2 request. The batch returns
-- producer identities and partial facts; no server is credited with complete
-- dynamic call coverage merely because its hierarchy request succeeded.
local M = {}
local core = require("huyang.core")
local rpc = require("huyang.rpc")
local MAX_FILES, MAX_SYMBOLS, MAX_EDGES = 32, 128, 1024
local MAX_BYTES, MAX_ITEMS, REQUEST_MS = 1024 * 1024, 16, 1500

local function leaf(name)
    return (name or ""):match("([^/%.:]+)$") or name
end

local function version(client)
    local info = client.server_info or {}
    return tostring(info.version or "unreported")
end

local function position(buf, range, encoding)
    local line = vim.api.nvim_buf_get_lines(buf, range.start.line, range.start.line + 1, false)[1] or ""
    local col = range.start.character
    if encoding == "utf-16" then col = vim.str_byteindex(line, "utf-16", col, false) end
    if encoding == "utf-32" then col = vim.str_byteindex(line, "utf-32", col, false) end
    return range.start.line + 1, col + 1
end

local function append(out, key, value, maximum)
    if #out[key] >= maximum then out.capped = true; return false end
    local bytes = #vim.json.encode(value)
    if out.bytes + bytes > MAX_BYTES then out.capped = true; return false end
    out.bytes = out.bytes + bytes
    out[key][#out[key] + 1] = value
    return true
end

local function implementations(out, client, symbol, target, site_line, site_col)
    if not client:supports_method("textDocument/implementation") then return end
    local target_buf = core.load_buf(vim.uri_to_fname(target.uri))
    local locations = core.request(client, target_buf, "textDocument/implementation", {
        textDocument = {uri = target.uri}, position = target.selectionRange.start,
    }, REQUEST_MS) or {}
    if locations.uri or locations.targetUri then locations = {locations} end
    for _, location in ipairs(locations) do
        local uri = location.uri or location.targetUri
        local range = location.targetSelectionRange or location.range
        local buf = core.load_buf(vim.uri_to_fname(uri))
        local line, col = position(buf, range, client.offset_encoding)
        if not append(out, "edges", {source = symbol, target = {
            file = vim.uri_to_fname(uri), name = target.name, line = line, col = col,
        }, line = site_line, col = site_col, producer = client.name, version = version(client),
            method = "implementation"}, MAX_EDGES) then return end
    end
end

local function hierarchy(out, buf, client, symbol)
    local params = core.position_params(buf, client, {line = symbol.line, symbol = leaf(symbol.name)})
    local items = core.request(client, buf, "textDocument/prepareCallHierarchy", params, REQUEST_MS) or {}
    if #items > MAX_ITEMS then out.capped = true end
    for i, item in ipairs(items) do
        if i > MAX_ITEMS then break end
        rpc.check_cancelled()
        local calls = core.request(client, buf, "callHierarchy/outgoingCalls", {item = item}, REQUEST_MS) or {}
        for _, call in ipairs(calls) do
            local target = call.to
            local target_buf = core.load_buf(vim.uri_to_fname(target.uri))
            local line, col = position(target_buf, target.selectionRange, client.offset_encoding)
            for _, site in ipairs(call.fromRanges or {}) do
                local site_line, site_col = position(buf, site, client.offset_encoding)
                if not append(out, "edges", {source = symbol, target = {
                    file = vim.uri_to_fname(target.uri), name = target.name, line = line, col = col,
                }, line = site_line, col = site_col, producer = client.name, version = version(client),
                method = "call_hierarchy"}, MAX_EDGES) then return end
                if target.kind == 6 then
                    local ok = pcall(implementations, out, client, symbol, target, site_line, site_col)
                    if not ok then out.incomplete = true end
                end
            end
        end
    end
    return #items > 0
end

-- A references result identifies a use, not a call. Keep it as a dependency
-- candidate, and let the parser or a later hierarchy identify the call site.
local function references(out, buf, client, symbol)
    local params = core.position_params(buf, client, {line = symbol.line, symbol = leaf(symbol.name)})
    params.context = {includeDeclaration = false}
    local refs = core.request(client, buf, "textDocument/references", params, REQUEST_MS) or {}
    for _, ref in ipairs(refs) do
        if not append(out, "references", {target = symbol, file = vim.uri_to_fname(ref.uri),
            line = ref.range.start.line + 1, producer = client.name, version = version(client)}, MAX_EDGES) then return end
    end
end

local function file(out, path, with_lsp)
    local buf = core.load_buf(path)
    local content = table.concat(vim.api.nvim_buf_get_lines(buf, 0, -1, false), "\n")
    if vim.bo[buf].eol then content = content .. "\n" end
    out.sources[path] = vim.fn.sha256(content)
    local symbols = require("huyang.index").file_symbols({file = path})
    if symbols.note then out.capped = true end
    local functions = {}
    for _, entry in ipairs(symbols.matches or {}) do
        rpc.check_cancelled()
        local first = tonumber(tostring(entry.lines):match("^(%d+)")) or 1
        local text = vim.api.nvim_buf_get_lines(buf, first - 1, first, false)[1] or ""
        local name = leaf(entry.name_path)
        local column = text:find(name or "", 1, true) or 1
        local symbol = {file = path, name = name, line = first, col = column, kind = entry.kind,
            end_line = tonumber(tostring(entry.lines):match("%-(%d+)$")) or first}
        local kind = tostring(entry.kind):lower()
        if kind:find("function") or kind:find("method") then
        functions[#functions + 1] = symbol
        if not append(out, "nodes", symbol, MAX_SYMBOLS) then break end
        if with_lsp then
            local ok, client = pcall(core.get_client, buf, "textDocument/prepareCallHierarchy", 100)
            if ok and client:supports_method("callHierarchy/outgoingCalls", buf) then
                local answered, found = pcall(hierarchy, out, buf, client, symbol)
                if not answered or not found then
                    out.incomplete = true
                    if client:supports_method("textDocument/references", buf) then
                        pcall(references, out, buf, client, symbol)
                    end
                end
                out.producers[client.name] = version(client)
            else
                local attached = vim.lsp.get_clients({bufnr = buf})[1]
                if attached and attached:supports_method("textDocument/references", buf) then
                    local answered = pcall(references, out, buf, attached, symbol)
                    if not answered then out.incomplete = true end
                    out.producers[attached.name] = version(attached)
                else out.incomplete = true end
            end
        end
        if out.capped then break end
        end
    end
    if not path:match("%.go$") then
        local complete = require("huyang.execution_parser").collect(buf, functions, function(edge)
            return append(out, "edges", edge, MAX_EDGES)
        end)
        if not complete then out.incomplete = true end
    end
end

function M.batch(args)
    if type(args.files) ~= "table" or #args.files > MAX_FILES then
        error({code = "graph_batch_invalid", message = "execution batch accepts at most 32 files", detail = ""}, 0)
    end
    local out = {nodes = {}, edges = {}, references = {}, producers = {}, sources = {}, bytes = 0,
        capped = false, incomplete = false, complete = false}
    for _, path in ipairs(args.files) do
        rpc.check_cancelled()
        local ok = pcall(file, out, path, args.with_lsp ~= false)
        if not ok then out.incomplete = true end
        if out.capped then break end
    end
    rpc.check_cancelled()
    out.reason = "call hierarchy and references do not establish exhaustive dynamic dispatch coverage"
    return out
end

return M
