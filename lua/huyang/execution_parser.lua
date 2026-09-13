-- Tree-sitter supplies syntax candidates when hierarchy is unavailable. This
-- module never calls a textual match a resolved call, and never claims complete
-- dynamic coverage.
local M = {}
local MAX_VISITS, MAX_CANDIDATES = 4096, 128
local calls = {call_expression = true, call = true, function_call = true}

local function text(node, buf)
    if not node then return "" end
    return vim.treesitter.get_node_text(node, buf)
end

local function owner(symbols, line)
    local selected
    for _, symbol in ipairs(symbols) do
        if line >= symbol.line and line <= symbol.end_line
            and (not selected or symbol.line >= selected.line) then selected = symbol end
    end
    return selected
end

function M.collect(buf, symbols, append)
    local ok, parser = pcall(vim.treesitter.get_parser, buf)
    if not ok then return false end
    local trees = parser:parse()
    if not trees or not trees[1] then return false end
    local stack, visits = {trees[1]:root()}, 0
    while #stack > 0 do
        require("huyang.rpc").check_cancelled()
        visits = visits + 1
        if visits > MAX_VISITS then return false end
        local node = table.remove(stack)
        if calls[node:type()] then
            local row, col = node:start()
            local source = owner(symbols, row + 1)
            local target = node:field("function")[1] or node:field("name")[1]
            local target_text = text(target, buf)
            local name = target_text:match("([%w_]+)$") or "<dynamic>"
            if source then
                local found, count = false, 0
                for _, candidate in ipairs(symbols) do
                    if candidate.name == name then
                        count = count + 1
                        if count > MAX_CANDIDATES then return false end
                        found = true
                        if not append({source = source, target = candidate, line = row + 1,
                            col = col + 1, producer = "treesitter_calls", version = "1",
                            method = "parser_candidate"}) then return false end
                    end
                end
                if not found and not append({source = source, target = {
                    file = source.file, line = row + 1, col = col + 1, name = name, unresolved = true,
                }, line = row + 1, col = col + 1, producer = "treesitter_calls",
                    version = "1", method = "parser_candidate"}) then return false end
            end
        end
        for child in node:iter_children() do
            if #stack >= MAX_VISITS then return false end
            stack[#stack + 1] = child
        end
    end
    return true
end
return M
