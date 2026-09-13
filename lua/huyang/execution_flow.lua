-- Bounded syntax slices for selected non-Go functions. Lexical edges are
-- explicitly syntax_next: they are never represented as executable order.
local M = {}
local MAX_NODES, MAX_VISITS, MAX_TEXT = 256, 4096, 1024
local branches = {if_statement=true, elif_clause=true, for_statement=true, while_statement=true,
    repeat_statement=true, conditional_expression=true, for_in_statement=true}
local exits = {return_statement=true, throw_statement=true, raise_statement=true, break_statement=true}
local async = {await_expression=true, yield_expression=true, yield_statement=true, ["await"]=true, ["yield"]=true}
local calls = {call_expression=true, call=true, function_call=true}

local function text(node, buf)
    return node and vim.treesitter.get_node_text(node, buf) or ""
end

local function contains(node, line, col)
    local first, column, last, endcol = node:range()
    return (line > first or line == first and col >= column)
        and (line < last or line == last and col < endcol)
end

local function function_node(root, line, col)
    local selected, stack, visits = nil, {root}, 0
    while #stack > 0 and visits < MAX_VISITS do
        visits = visits + 1
        local node = table.remove(stack)
        if contains(node, line, col) then
            local kind = node:type()
            if kind:find("function") or kind == "method_definition" then selected = node end
            for child in node:iter_children() do
                if #stack >= MAX_VISITS then return nil end
                stack[#stack + 1] = child
            end
        end
    end
    return selected
end

local function collect(buf, selection, out)
    local parser = vim.treesitter.get_parser(buf)
    local fn = function_node(parser:parse()[1]:root(), selection.line - 1, selection.col - 1)
    if not fn then out.gaps[#out.gaps + 1] = "function_source_ambiguous"; return end
    local stack, visits, count, previous = {fn}, 0, 0, nil
    while #stack > 0 do
        require("huyang.rpc").check_cancelled()
        visits = visits + 1
        if visits > MAX_VISITS or count >= MAX_NODES then out.capped = true; return end
        local node = table.remove(stack)
        local kind, row, col = node:type(), node:start()
        local role = branches[kind] and "condition" or exits[kind] and "exit"
            or async[kind] and "async_boundary" or calls[kind] and "call_site"
        if role then
            count = count + 1
            local endrow, endcol = node:end_()
            local id = "syntax:" .. selection.id .. ":" .. row .. ":" .. col .. ":" .. endrow .. ":" .. endcol .. ":" .. kind
            local label = text(node, buf)
            if calls[kind] and (label:match("^coroutine%.") or label:match("%.then%s*%(")) then role="async_boundary" end
            local fact = {id=id, owner=selection.id, kind=role, name=label:sub(1,MAX_TEXT),
                path=selection.path, line=row+1, column=col+1}
            if role == "condition" then
                local condition = node:field("condition")[1]
                local expression = text(condition, buf)
                fact.condition = {text=expression:sub(1,MAX_TEXT), content_hash=vim.fn.sha256(expression), variables={}}
                local seen = {}
                for name in expression:gmatch("[%a_][%w_]*") do
                    if not seen[name] and #fact.condition.variables < 128 then
                        seen[name]=true; fact.condition.variables[#fact.condition.variables+1]=name
                    end
                end
                table.sort(fact.condition.variables)
            end
            out.bytes = out.bytes + #vim.json.encode(fact) + 512
            if out.bytes > 1024 * 1024 then out.capped=true; return end
            out.nodes[#out.nodes+1] = fact
            if previous then out.edges[#out.edges+1] = {from=previous,to=id,kind="syntax_next"} end
            previous=id
        end
        -- Nested function bodies belong to their own selection.
        local nested = node ~= fn and (kind:find("function") or kind == "method_definition")
        if not nested then
            local children = {}
            for child in node:iter_children() do
                if #children + #stack >= MAX_VISITS then out.capped=true; return end
                children[#children+1] = child
            end
            for i=#children,1,-1 do stack[#stack+1]=children[i] end
        end
    end
end

function M.batch(args)
    if type(args.selections) ~= "table" or #args.selections > 16 then
        error({code="graph_batch_invalid",message="at most 16 flow selections",detail=""},0)
    end
    local out = {sources={}, nodes={}, edges={}, bytes=0, gaps={"syntax_slices_do_not_prove_control_flow"}, capped=false}
    for _, selection in ipairs(args.selections) do
        require("huyang.rpc").check_cancelled()
        local ok = pcall(function()
            local buf = require("huyang.core").load_buf(selection.file)
            local content = table.concat(vim.api.nvim_buf_get_lines(buf,0,-1,false),"\n")
            if vim.bo[buf].eol then content=content.."\n" end
            out.sources[selection.file]=vim.fn.sha256(content)
            collect(buf, selection, out)
        end)
        if not ok then out.gaps[#out.gaps+1]="syntax_flow_unavailable" end
    end
    return out
end
return M
