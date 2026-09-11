-- LSP query helpers for huyang.
--
-- Every function here runs inside the embedded Neovim instance, invoked over
-- RPC by the Huyang service (internal/provider/embed). The point of this
-- module is to reuse the LSP clients that are already running and warm
-- instead of making the agent spawn its own language servers.
--
-- Addressing convention for the agent: a position is (file, line, symbol).
-- `line` is 1-based; `symbol` is a piece of text on that line whose first
-- occurrence marks the column. This is far more robust for an LLM than
-- asking it to produce a correct UTF-16 column. An explicit 1-based byte
-- `col` is accepted as an alternative.
--
-- Concurrency model: every tool runs inside a coroutine started by
-- huyang.rpc. Anything that must wait (LSP replies, attach polling) yields
-- via `await` and is resumed from a callback, so the editor's main loop never
-- blocks while a tool call is in flight; the result reaches the service as an
-- RPC notification.

local M = {}

local core = require("huyang.core")
local cap = require("huyang.cap")
local err, sleep = core.err, core.sleep
local load_buf, rel_path, fresh_buf = core.load_buf, core.rel_path, core.fresh_buf
local get_client, request, resync_open_buffers = core.get_client, core.request, core.resync_open_buffers
local position_params, line_preview = core.position_params, core.line_preview
local MAX_LOCATIONS, FRESH_RETRY_MS = core.MAX_LOCATIONS, core.FRESH_RETRY_MS

local index = require("huyang.index")
local symbol_kind = index.symbol_kind
local annotate_locations = index.annotate_locations
local document_symbols = index.document_symbols
local workspace_tree = index.workspace_tree
local workspace_symbols, find_symbol = index.workspace_symbols, index.find_symbol

local edit = require("huyang.edit")
local code_actions = edit.code_actions

local install = require("huyang.install")
local workspace_support, install_language = install.workspace_support, install.install_language
-- Normalize Location | Location[] | LocationLink[] into a compact list.
local function format_locations(result)
    if result == nil then
        return { locations = {}, note = "no results" }
    end
    if result.uri or result.targetUri then
        result = { result }
    end
    local out, total = {}, #result
    -- Every file the result names, in the order they first appear, counted
    -- before the cut. The cap is on locations, and the unit that matters for
    -- the task this tool is advertised for - "check before changing a
    -- signature" - is the file: a reply saying "truncated to first 100 of
    -- 201 locations" left five whole files out and named none of them, and
    -- read as "I have seen the files, just not every line".
    local file_order, seen_file = {}, {}
    for _, loc in ipairs(result) do
        local path = vim.uri_to_fname(loc.uri or loc.targetUri)
        if not seen_file[path] then
            seen_file[path] = true
            file_order[#file_order + 1] = path
        end
    end
    local shown_file = {}
    for i, loc in ipairs(result) do
        if i > MAX_LOCATIONS then break end
        local uri = loc.uri or loc.targetUri
        local range = loc.targetSelectionRange or loc.range
        local path = vim.uri_to_fname(uri)
        local lnum = range.start.line + 1
        local item = { file = path, line = lnum, text = line_preview(path, lnum) }
        if range["end"].line ~= range.start.line then
            item.end_line = range["end"].line + 1
        end
        shown_file[path] = true
        out[#out + 1] = item
    end
    local res = { count = total, locations = out }
    if total > MAX_LOCATIONS then
        local missing = {}
        for _, path in ipairs(file_order) do
            if not shown_file[path] then missing[#missing + 1] = path end
        end
        local cut = cap.fields(#out, total, {
            unit = "locations",
            reach = "narrow with a more specific position, or read the files named below",
        })
        res.shown, res.total, res.dropped, res.note = cut.shown, cut.total, cut.dropped, cut.note
        if #missing > 0 then
            res.files = cap.fields(#file_order - #missing, #file_order, { unit = "files" })
            res.files_omitted = cap.list(missing, 20, "files",
                "none of their locations are above")
        end
    end
    return res
end

local function location_tool(method, extra_params)
    return function(args)
        local bufnr = load_buf(args.file)
        local client = get_client(bufnr, method)
        local params = position_params(bufnr, client, args)
        if extra_params then
            params = vim.tbl_deep_extend("force", params, extra_params)
        end
        return format_locations(request(client, bufnr, method, params))
    end
end

-- Some servers answer hover with a progress placeholder while they are
-- still indexing (lua_ls: "Workspace loading: 3 / 120"). That is not an
-- answer; wait a little and ask again.
local HOVER_RETRY_MS = 500
local HOVER_RETRY_DEADLINE_MS = 8000

local function placeholder_hover(text)
    return text:find("^%s*Workspace loading") ~= nil
        or text:find("^%s*Loading workspace") ~= nil
end

local function hover(args)
    local bufnr = load_buf(args.file)
    local client = get_client(bufnr, "textDocument/hover")
    local params = position_params(bufnr, client, args)
    local deadline = vim.uv.now() + HOVER_RETRY_DEADLINE_MS
    local text
    while true do
        local result = request(client, bufnr, "textDocument/hover", params)
        if not (result and result.contents) then
            return { hover = nil, note = "no hover information at this position" }
        end
        local lines = vim.lsp.util.convert_input_to_markdown_lines(result.contents)
        text = table.concat(lines, "\n")
        if not placeholder_hover(text) or vim.uv.now() >= deadline then
            break
        end
        sleep(HOVER_RETRY_MS)
    end
    if placeholder_hover(text) then
        return { hover = nil, note = "the language server is still indexing (" .. text .. "); retry shortly" }
    end
    -- qmlls answers with an empty payload rather than with nothing, and a
    -- bare `"hover": ""` reads as "the position was wrong".
    if vim.trim(text or "") == "" then
        return { hover = nil, note = "the language server returned no hover text for this symbol" }
    end
    return { hover = text }
end

-- Titles of the code actions available for one diagnostic, so the agent
-- knows when the language server can fix a problem itself.
local function quick_fix_titles(bufnr, client, d)
    local lsp_diags = {}
    pcall(function()
        lsp_diags = vim.lsp.diagnostic.from({ d })
    end)
    -- vim.diagnostic columns are 0-based bytes; the server wants characters
    -- in its own offset encoding, the conversion make_position does for a
    -- requested column. Past the end of the line the byte index clamps.
    local encoding = client.offset_encoding or "utf-16"
    local function character(lnum, byte0)
        local text = vim.api.nvim_buf_get_lines(bufnr, lnum, lnum + 1, false)[1] or ""
        local okx, ch = pcall(vim.str_utfindex, text, encoding, math.min(byte0 or 0, #text), false)
        return okx and ch or (byte0 or 0)
    end
    local end_lnum = d.end_lnum or d.lnum
    local ok, actions = pcall(request, client, bufnr, "textDocument/codeAction", {
        textDocument = { uri = vim.uri_from_bufnr(bufnr) },
        range = {
            start = { line = d.lnum, character = character(d.lnum, d.col) },
            ["end"] = { line = end_lnum, character = character(end_lnum, d.end_col or d.col) },
        },
        context = { diagnostics = lsp_diags, triggerKind = 1 },
    })
    if not ok or not actions or #actions == 0 then
        return nil
    end
    local titles, seen = {}, {}
    for _, a in ipairs(actions) do
        -- Only actions the server itself calls a fix. A server that answers
        -- a diagnostic-scoped request with its whole refactor menu offered
        -- "Convert default export to named export" as the fix for a type
        -- error, and the note above tells the caller to apply it. lua_ls and
        -- friends also offer "Disable diagnostics ...", which silences the
        -- problem rather than fixing it.
        -- gopls answers a diagnostic-scoped request with its whole line menu:
        -- "Add test for lastChar" and "Browse arm64 assembly" came back as
        -- quick fixes for an unused variable. Only quickfix kinds, and the
        -- servers that send no kind at all.
        local kind = a.kind or ""
        local fixes = kind == "" or kind:sub(1, 8) == "quickfix"
        if fixes and not seen[a.title]
            and not a.title:find("^Disable diagnostics") and not a.title:find("^Ignore ") then
            seen[a.title] = true
            titles[#titles + 1] = a.title
            if #titles == 3 then break end
        end
    end
    if #titles == 0 then
        return nil
    end
    return titles
end

-- Diagnostics sharing a severity and code beyond this many are folded:
-- the first DIAG_GROUP_SHOW stay verbose, the rest become a line list.
local DIAG_GROUP_MIN = 4
local DIAG_GROUP_SHOW = 3

local function diagnostics(args)
    local bufnr = load_buf(args.file)
    -- A verdict still owed on an earlier edit is settled first: reading the
    -- diagnostics before the server has answered would show the old set.
    edit.flush_deferred(true)
    -- Same reason as before an edit: a file changed by another tool is stale
    -- in the server until it is told, and this file's diagnostics may depend
    -- on it.
    if #resync_open_buffers() > 0 then
        sleep(300)
    end
    -- A file no server will ever attach to (a filetype with no enabled LSP
    -- config and no running client for it) gets an answer at once instead
    -- of the attach timeout and an error: whatever a linter left in
    -- vim.diagnostic, plus a note that no server was consulted.
    if #vim.lsp.get_clients({ bufnr = bufnr }) == 0 then
        local ft = vim.bo[bufnr].filetype
        local serves = #core.enabled_lsp_configs_for(ft) > 0
        if not serves then
            for _, c in ipairs(vim.lsp.get_clients()) do
                ---@diagnostic disable-next-line: undefined-field
                local fts = (c.config or {}).filetypes
                if vim.tbl_contains(fts or {}, ft) then
                    serves = true
                    break
                end
            end
        end
        if not serves then
            local diags = vim.diagnostic.get(bufnr)
            local out = {}
            for _, d in ipairs(diags) do
                out[#out + 1] = {
                    line = d.lnum + 1,
                    col = d.col + 1,
                    severity = vim.diagnostic.severity[d.severity],
                    message = d.message,
                    source = d.source,
                    code = d.code,
                }
            end
            return {
                count = #diags,
                diagnostics = out,
                note = ("no language server is enabled for filetype %s, so nothing checks this "
                    .. "file; install_language(%q) adds one"):format(ft ~= "" and ft or "(none)",
                    ft ~= "" and ft or vim.fn.fnamemodify(args.file, ":e")),
            }
        end
    end
    -- Give a freshly attached server a moment to publish.
    get_client(bufnr, "textDocument/didOpen")
    local deadline = vim.uv.now() + 1000
    while #vim.diagnostic.get(bufnr) == 0 and vim.uv.now() < deadline do
        sleep(100)
    end
    local okc, action_client = pcall(get_client, bufnr, "textDocument/codeAction", 1000)
    local diags = vim.diagnostic.get(bufnr)
    table.sort(diags, function(a, b)
        if a.severity ~= b.severity then return a.severity < b.severity end
        if a.lnum ~= b.lnum then return a.lnum < b.lnum end
        return a.col < b.col
    end)
    -- Many diagnostics with one code (an unresolved dependency reported on
    -- every import) collapse into the first few plus a line list.
    local by_code, groups = {}, {}
    for _, d in ipairs(diags) do
        local key = tostring(d.severity) .. ":" .. tostring(d.code or d.message)
        if not by_code[key] then
            by_code[key] = { n = 0, shown = 0, lines = {} }
            groups[#groups + 1] = by_code[key]
        end
        by_code[key].n = by_code[key].n + 1
    end
    local out, total = {}, #diags
    for i, d in ipairs(diags) do
        local key = tostring(d.severity) .. ":" .. tostring(d.code or d.message)
        local g = by_code[key]
        if g.n > DIAG_GROUP_MIN and g.shown >= DIAG_GROUP_SHOW then
            g.lines[#g.lines + 1] = d.lnum + 1
            if not g.summary then
                g.summary = {
                    severity = vim.diagnostic.severity[d.severity],
                    code = d.code,
                    source = d.source,
                    more = g.n - DIAG_GROUP_SHOW,
                    lines = g.lines,
                    message = ("%d more %s like the ones above, at the listed lines")
                        :format(g.n - DIAG_GROUP_SHOW, tostring(d.code or "")),
                }
                out[#out + 1] = g.summary
            end
        else
            g.shown = g.shown + 1
            local entry = {
                line = d.lnum + 1,
                col = d.col + 1,
                severity = vim.diagnostic.severity[d.severity],
                message = d.message,
                source = d.source,
                code = d.code,
            }
            if okc and i <= 8 and d.severity <= vim.diagnostic.severity.WARN then
                entry.quick_fixes = quick_fix_titles(bufnr, action_client, d)
            end
            out[#out + 1] = entry
        end
    end
    -- An empty answer is only as good as the server behind it. The edit tools
    -- hedge here; this one used to return {"count": 0, "diagnostics": []} for
    -- a shell file that `bash -n` rejects outright, which is the same lie
    -- through a different door.
    local silent, silent_scope
    if total == 0 then silent, silent_scope = edit.silent_server(bufnr, nil, 0) end
    return {
        count = total,
        diagnostics = out,
        note = total > 0
            and "quick_fixes lists code actions the language server can apply for you: "
            .. "call code_actions with this file and line (the col above is optional), "
            .. "then apply one through a change_plan apply_code_action operation, "
            .. "instead of editing by hand"
            or silent and ("nothing is reported for this file, but %s has published no "
                .. "diagnostics %s, so this is not evidence that the file "
                .. "is clean; verify_run runs the project's own build or check")
                :format(silent, silent_scope)
            or nil,
    }
end

local function call_hierarchy(direction)
    local method = direction == "in"
        and "callHierarchy/incomingCalls" or "callHierarchy/outgoingCalls"
    return function(args)
        local bufnr = load_buf(args.file)
        local client = get_client(bufnr, "textDocument/prepareCallHierarchy")
        local items = request(client, bufnr, "textDocument/prepareCallHierarchy",
            position_params(bufnr, client, args))
        if not items or #items == 0 then
            return { calls = {}, note = "no call hierarchy item at this position" }
        end
        local calls = request(client, bufnr, method, { item = items[1] })
        local out = {}
        for _, c in ipairs(calls or {}) do
            local it = c.from or c.to
            out[#out + 1] = {
                name = it.name,
                kind = symbol_kind(it.kind),
                file = vim.uri_to_fname(it.uri),
                line = it.selectionRange.start.line + 1,
                call_sites = vim.tbl_map(function(r)
                    return r.start.line + 1
                end, c.fromRanges or {}),
            }
        end
        return { for_symbol = items[1].name, calls = out }
    end
end

-- One text search over the whole tree answers for every name at once; this
-- bounds it, so a huge tree costs one timeout rather than one per symbol.
local TEXT_SEARCH_TIMEOUT_MS = 30 * 1000

-- Every whole-word occurrence of each of `names` in the project, as a map
-- from name to file/line pairs, from a single rg (or grep) run. The fallback
-- for a file whose language has no server that answers references (QML is
-- one), which is the sweep a careful caller does by hand. nil means the
-- search could not be run or did not finish.
local function textual_hits(root, names)
    if #names == 0 then
        return {}
    end
    local cmd
    if vim.fn.executable("rg") == 1 then
        -- -o prints the matched word itself, which is what attributes each
        -- hit to its name when many are searched at once. core.rg_walk adds
        -- the shared walk: hidden directories are searched, ignored files
        -- and .git are not.
        cmd = core.rg_walk({ "rg", "--no-heading", "--line-number",
            "--only-matching", "--word-regexp", "--fixed-strings" })
    elseif vim.fn.executable("grep") == 1 then
        cmd = core.grep_walk({ "grep", "-rnwIoF" })
    else
        return nil
    end
    for _, name in ipairs(names) do
        cmd[#cmd + 1] = "-e"
        cmd[#cmd + 1] = name
    end
    cmd[#cmd + 1] = "--"
    cmd[#cmd + 1] = root
    local result = core.await(function(resume)
        -- The exit callback runs in a fast event context, where buffers
        -- cannot be loaded; schedule the resume onto the main loop.
        local ok, e = pcall(vim.system, cmd, { text = true, timeout = TEXT_SEARCH_TIMEOUT_MS },
            vim.schedule_wrap(resume))
        if not ok then
            resume({ code = -1, stderr = tostring(e) })
        end
    end)
    -- Both searchers exit 1 for "nothing matched", which is an answer; a
    -- search killed at the timeout (124, SIGTERM) is not, and neither is a
    -- searcher that failed.
    if result.code == 124 and result.signal == 15 then
        return nil, "timed out"
    end
    if result.code > 1 then
        return nil
    end
    local hits, seen = {}, {}
    for _, name in ipairs(names) do
        hits[name] = {}
    end
    for line in (result.stdout or ""):gmatch("[^\n]+") do
        local path, lnum, name = line:match("^(.-):(%d+):(.*)$")
        if path and hits[name] then
            -- One hit per line and name, as a line-oriented search reports.
            local key = name .. "\0" .. path .. "\0" .. lnum
            if not seen[key] then
                seen[key] = true
                local list = hits[name]
                list[#list + 1] = { file = path, line = tonumber(lnum) }
            end
        end
    end
    return hits
end

local dispatch_table = {
    definition = location_tool("textDocument/definition"),
    type_definition = location_tool("textDocument/typeDefinition"),
    implementation = location_tool("textDocument/implementation"),
    references = function(args)
        local tool = location_tool("textDocument/references",
            { context = { includeDeclaration = true } })
        local result = tool(args)
        if (result.count or 0) <= 1 then
            local okb, bufnr = pcall(load_buf, args.file)
            if okb and fresh_buf(bufnr) then
                sleep(FRESH_RETRY_MS)
                result = tool(args)
            end
        end
        annotate_locations(result.locations or {})
        -- Group by file so the path is written once per file, not per hit.
        local groups, order = {}, {}
        for _, loc in ipairs(result.locations or {}) do
            local file = loc.file
            if not groups[file] then
                groups[file] = { file = file, hits = {} }
                order[#order + 1] = file
            end
            loc.file = nil
            table.insert(groups[file].hits, loc)
        end
        local files = {}
        for _, file in ipairs(order) do
            files[#files + 1] = groups[file]
        end
        result.locations = nil
        result.files = files
        -- A server that cannot resolve the project's imports answers about
        -- one package and looks complete: on a monorepo with no node_modules
        -- installed, this returned 23 hits in 3 files while the symbol was
        -- used in 6, and rename_symbol went on to break the other 3. The
        -- caveat belongs here, where the answer is read, not only in the
        -- reply to open_workspace.
        local root = args.root or vim.fn.getcwd()
        local missing = core.deps_missing(root)
        if missing then
            result.may_be_incomplete = "the language server cannot resolve this project's "
                .. "imports (" .. missing .. ") so references from other packages are missing "
                .. "from this answer; grep for the name to see them"
        end
        -- "Only the declaration" is the answer a server gives for genuinely
        -- dead code and the answer it gives while its workspace index is still
        -- building, and the two are indistinguishable from the reply. A shell
        -- function with eleven call sites in six files came back as count: 1,
        -- and the identical call later in the same session came back as 11.
        -- That is a delete-it-and-ship-it answer, so the one case where it
        -- matters is checked against a plain text search before answering.
        if (result.count or 0) <= 1 and #(result.files or {}) <= 1 then
            local name = args.symbol
            if type(name) ~= "string" or name == "" then
                local hit = result.files and result.files[1] and result.files[1].hits[1]
                name = hit and hit.text and hit.text:match("[%w_]+") or nil
            end
            local found = name and textual_hits(root, { name })
            local elsewhere, seen = {}, {}
            local here = result.files and result.files[1] and result.files[1].file
            for _, h in ipairs(found and found[name] or {}) do
                if h.file ~= here and not seen[h.file] then
                    seen[h.file] = true
                    elsewhere[#elsewhere + 1] = rel_path(h.file)
                end
            end
            if #elsewhere > 0 then
                table.sort(elsewhere)
                result.text_search_disagrees = elsewhere
                result.text_search_note = ("the server reports no reference outside the "
                    .. "declaration, but a plain text search finds %q in %d other file(s). A "
                    .. "server whose workspace index is still building answers exactly like "
                    .. "one that has found nothing, so do not read this as dead code: check "
                    .. "those files, or ask again in a moment."):format(name, #elsewhere)
            end
        end
        return result
    end,
    hover = hover,
    document_symbols = document_symbols,
    workspace_symbols = workspace_symbols,
    diagnostics = diagnostics,
    huyang_diagnostic_evidence = function(args)
        return require("huyang.edit").diagnostic_evidence(args)
    end,
    incoming_calls = call_hierarchy("in"),
    outgoing_calls = call_hierarchy("out"),
    code_actions = code_actions,
    workspace_tree = workspace_tree,
    workspace_support = workspace_support,
    install_language = install_language,
    find_symbol = find_symbol,
    -- Internal Huyang transaction calls stage exact bytes without saving or emitting
    -- intermediate edit verdicts. The Go coordinator owns the lease and durable state.
    huyang_prepare = function(args)
        return require("huyang.transaction").prepare(args)
    end,
    huyang_rollback = function(args)
        return require("huyang.transaction").rollback(args)
    end,
    huyang_commit = function(args)
        return require("huyang.transaction").commit(args)
    end,
    huyang_workspace_resync = function()
        for _, bufnr in ipairs(vim.api.nvim_list_bufs()) do
            local path = vim.api.nvim_buf_get_name(bufnr)
            if path ~= "" and vim.api.nvim_buf_is_loaded(bufnr) and vim.bo[bufnr].modified then
                local lines = vim.api.nvim_buf_get_lines(bufnr, 0, -1, false)
                local content = table.concat(lines, "\n")
                if vim.bo[bufnr].eol then content = content .. "\n" end
                local handle = io.open(path, "rb")
                local disk = handle and handle:read("*a") or nil
                if handle then handle:close() end
                if disk == content then
                    vim.bo[bufnr].modified = false
                    vim.api.nvim_buf_call(bufnr, function() vim.cmd("silent checktime") end)
                end
            end
        end
        local changed = resync_open_buffers()
        return { resynced = #changed }
    end,
}

-- Registered under both names: the bridge sends workspace_resync, the unit
-- tests and older callers the huyang_ prefixed form.
dispatch_table.workspace_resync = dispatch_table.huyang_workspace_resync

-- Turn absolute "file" fields (and changed_files lists) into paths relative
-- to the working directory before the result leaves the editor.
local function relativize(value, key)
    if type(value) == "string" then
        if (key == "file" or key == "changed_files") and value:sub(1, 1) == "/" then
            return rel_path(value)
        end
        return value
    end
    if type(value) ~= "table" then
        return value
    end
    for k, v in pairs(value) do
        value[k] = relativize(v, type(k) == "string" and k or key)
    end
    return value
end

function M.dispatch(tool, args)
    -- Which client this call is for, before anything runs: the undo ledger,
    -- the two baselines and the set of diagnostics already disclosed are
    -- kept per (root, client), and this editor serves every client that
    -- opened its root. The id rides in the arguments, put there by the
    -- bridge (bridge/client.go); a call that carries none is the editor's
    -- own.
    require("huyang.client").enter(args and args.client)
    local fn = dispatch_table[tool]
    if not fn then
        -- The debugger tools live in their own module; they share the
        -- transport, the position addressing and the relativized replies.
        local okd, dap_tools = pcall(require, "huyang.dap")
        if okd and dap_tools.handles(tool) then
            return relativize(dap_tools.dispatch(tool, args))
        end
        err("unknown tool: %s", tostring(tool))
    end
    local result = fn(args or {})
    -- The transaction and resync calls are the service's own, not replies
    -- the agent sees: they take no carry (it would vanish) and keep
    -- absolute paths.
    local internal = tool == "workspace_resync" or tool:match("^huyang_") ~= nil
    -- Verdicts owed from earlier edits (deferred by wait=false, or
    -- diagnostics that arrived after their report) ride on this reply.
    if not internal and type(result) == "table"
        and (vim.tbl_isempty(result) or not vim.islist(result)) then
        for k, v in pairs(edit.take_carry()) do
            if result[k] == nil then result[k] = v end
        end
    end
    if not internal then
        result = relativize(result)
    end
    return result
end

-- Internals shared with huyang.dap, exported rather than moved so the
-- debugger module can reuse the coroutine helpers, buffer loading and the
-- symbol index without this file growing further.
M._internal = {
    await = core.await,
    sleep = core.sleep,
    err = core.err,
    load_buf = core.load_buf,
    resolve_symbol = index.resolve_symbol,
    rel_path = core.rel_path,
    disk_fingerprint = core.disk_fingerprint,
    symbol_index = index.symbol_index,
    innermost_entry = index.innermost_entry,
    decl_line = core.decl_line,
    -- Unit-checked: the location cap has to name the files it dropped, and
    -- driving 201 real references through a language server to see that is
    -- not a check anyone would run.
    format_locations = format_locations,
}
return M
