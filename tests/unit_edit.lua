-- Unit checks for the two guards that decide whether a formatter's pass is
-- kept after an edit. They run without a language server: the formatters
-- that fail these checks (a server that rewrites string contents, a server
-- that ignores the indent options it was handed) are not ones the smoke
-- test can start, so the check is driven by rewriting the buffer directly,
-- exactly as a bad format pass would.
--
-- Run with: nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_edit.lua

local edit = require("huyang.edit")
local core = require("huyang.core")

local failures = 0

local function check(name, ok, detail)
    if ok then
        io.stdout:write("ok   " .. name .. "\n")
    else
        failures = failures + 1
        io.stdout:write("FAIL " .. name .. (detail and ("\n     " .. vim.inspect(detail)) or "") .. "\n")
    end
    io.stdout:flush()
end

-- The optional observer cannot change delivery, errors, or source tables.
do
    local observer = require("huyang.diagnostic_observer")
    local result = {kind="full",items={{message="producer body"}}}
    observer.install(nil)
    local a,b,c = observer.receive({},result,function(value)
        check("disabled observer preserves exact result object",value==result)
        return "same",nil,17
    end)
    check("disabled observer preserves all return slots",a=="same" and b==nil and c==17)
    local thrown = {}
    local ok,detail = pcall(observer.receive,{},result,function() error(thrown) end)
    check("disabled observer preserves thrown error object",not ok and detail==thrown)

    local records = {}
    observer.install(function(event)
        records[#records+1]=vim.deepcopy(event)
        if event.result then event.result.items[1].message="observer rewrite" end
        if event.batch then event.batch.findings[1].message="observer rewrite" end
        if event.identity and event.identity.origin and event.identity.origin.raw then
            event.identity.origin.raw.request_id="observer rewrite"
        end
    end)
    local batch = {kind="lsp_pull",findings={{message="queued body"}},complete=false}
    observer.with_raw({request_id="actual"},function()
        observer.receive({generation=3},result,function(value)
            observer.construct(batch)
            observer.enqueue(batch,41)
            local normalized=vim.deepcopy(batch)
            observer.emission("late_emission",batch,normalized,41,{request_id="collection"})
            observer.transport(45,{ok=true,result={batches={normalized}}})
            return value
        end)
    end)
    check("observer snapshots cannot rewrite producer or queue",
        result.items[1].message=="producer body" and batch.findings[1].message=="queued body"
            and batch.result_id==nil and batch.origin_id==nil and batch.queue_sequence==nil)
    check("sidecar preserves accepted construction enqueue emission identity",
        #records==5 and records[1].stage=="accepted_receive"
            and records[2].identity.batch_id==records[3].identity.batch_id
            and records[3].identity.batch_id==records[4].identity.batch_id
            and records[4].identity.origin.raw.request_id=="actual"
            and records[4].queue_sequence==41 and records[5].provider_rpc_id==45
            and records[5].identity.batch_id==records[4].identity.batch_id)
    observer.receive({},result,function() end)
    check("missing native callback context stays unattributed",
        records[#records].stage=="accepted_receive" and records[#records].origin.raw==nil)

    observer.install(function() error("intentional observer failure") end)
    local calls=0
    local returned=observer.receive({},result,function(value) calls=calls+1; return value end)
    check("observer failure leaves production receive intact",
        calls==1 and returned==result and #observer.status().errors>0)
    ok,detail=pcall(observer.with_raw,{},function() error(thrown) end)
    check("enabled observer preserves thrown production error object",not ok and detail==thrown)

    -- Fail only the second snapshot of the final transport. No later observer
    -- event or collection status can reveal this error; transport_end must.
    local transported = {}
    observer.install(function(event)
        if event.stage=="transport_emission" and event.transport_index==2 then
            error("intentional final transport tail failure")
        end
        transported[#transported+1]=event
    end)
    local normalized = {}
    for i=1,2 do
        local source={kind="lsp_pull",findings={{message="body " .. i}},complete=false}
        observer.construct(source)
        normalized[i]=vim.deepcopy(source)
        observer.emission("immediate_emission",source,normalized[i],nil,{request_id="final"})
    end
    check("transport tail fixture has no earlier observer errors",#observer.status().errors==0)
    local completion={ok=true,result={batches=normalized}}
    local marker,args_seen
    local notify=function(...)
        args_seen={n=select("#",...),...}
        return "same",nil,17
    end
    local function notify_with_marker(channel,method,...)
        if method=="huyang/result" then
            local args={...}
            if type(args[2])=="table" then
                observer.transport(args[1],args[2])
                marker={kind="transport_end",provider_rpc_id=args[1],observer_status=observer.status()}
            end
        end
        return notify(channel,method,...)
    end
    a,b,c=notify_with_marker(8,"huyang/result",91,completion,nil)
    check("final transport marker exposes an otherwise silent observer error",
        marker and marker.provider_rpc_id==91 and #marker.observer_status.errors==1
            and transported[#transported].stage=="transport_emission"
            and transported[#transported].transport_index==1)
    check("tail failure preserves notify arguments return slots and source bodies",
        args_seen.n==5 and args_seen[1]==8 and args_seen[2]=="huyang/result"
            and args_seen[3]==91 and args_seen[4]==completion and args_seen[5]==nil
            and a=="same" and b==nil and c==17
            and normalized[2].findings[1].message=="body 2")
    notify=function() error(thrown) end
    ok,detail=pcall(notify_with_marker,8,"huyang/result",92,completion)
    check("transport marker preserves thrown notify error object",not ok and detail==thrown)
    observer.install(nil)
end

-- Missing-import grouping preserves canonical evidence and applies to late
-- observations without changing the raw publication's completion guards.
do
    local python = require("huyang.python_environment")
    local original_client = vim.lsp.get_client_by_id
    vim.lsp.get_client_by_id = function() return nil end
    python.configure({})
    local function batch(producer)
        return {
            kind = "lsp_push", producer = producer or "pyright", provider_id = "pyright#42",
            document = "/fixture/main.py", document_revision = "docrev_fixture",
            document_version = 5, expected_version = 5, transaction_id = "edit_python",
            complete = true, progress_pending = true,
            findings = {
                { code = "reportMissingImports", severity = 1, message = "Import a is missing" },
                { code = "reportMissingImports", severity = 1, message = "Import b is missing" },
                { code = "reportAssignmentType", severity = 1, message = "real type error" },
            },
        }
    end
    for _, source in ipairs({ "uv", "poetry" }) do
        python.configure({ interpreter = "", source = source, reason = "environment_manager_not_project_environment" })
        local rejected = python.normalize_batch(batch())
        check(source .. " base-interpreter rejection keeps import evidence provisional",
            rejected.complete == false and rejected.reason == "python_environment_unresolved"
                and #rejected.findings == 2 and rejected.findings[1].message == "real type error"
                and rejected.findings[2].code == "python_environment_unresolved"
                and rejected.document_version == 5 and rejected.expected_version == 5
                and rejected.progress_pending)
    end
    python.configure({})
    local grouped = python.normalize_batch(batch())
    check("unknown interpreter groups missing imports into one warning",
        #grouped.findings == 2 and grouped.findings[1].message == "real type error"
            and grouped.findings[2].code == "python_environment_unresolved" and grouped.findings[2].severity == 2)
    check("grouped imports never establish complete diagnostics",
        grouped.complete == false and grouped.reason == "python_environment_unresolved"
            and grouped.progress_pending == true)
    check("grouping preserves revision and attribution guards",
        grouped.document_revision == "docrev_fixture" and grouped.document_version == 5
            and grouped.expected_version == 5 and grouped.transaction_id == "edit_python")
    local repeated = python.normalize_batch(batch())
    check("the explanatory note has stable identity despite different imports",
        vim.deep_equal(grouped.findings[2], repeated.findings[2]))
    check("normalization is idempotent",
        #python.normalize_batch(grouped).findings == 2)
    check("basedpyright gets the same policy", #python.normalize_batch(batch("basedpyright")).findings == 2)
    check("other producers keep all findings", #python.normalize_batch(batch("other")).findings == 3)
    local other = batch()
    other.findings = { { code = "reportMissingModuleSource", severity = 2, message = "not a missing import" } }
    check("other import codes remain intact", python.normalize_batch(other).complete)
    vim.lsp.get_client_by_id = function()
        return { config = { settings = { python = { pythonPath = "/user/python" } } } }
    end
    check("an attached user interpreter keeps all import diagnostics",
        #python.normalize_batch(batch()).findings == 3)
    vim.lsp.get_client_by_id = function() return nil end
    python.configure({ configured = true })
    check("explicit project environment configuration keeps imports", #python.normalize_batch(batch()).findings == 3)
    python.configure({})
    local path = "/fixture/python-late.py"
    edit._note_pending_late(path, "edit_python_late", "wsrev_python", 5)
    local raw = batch().findings
    edit._capture_late("pyright", path, 5, raw, 42)
    local late = edit.late_evidence().batches[1]
    check("late imports are grouped without claiming completion",
        late and #late.findings == 2 and late.complete == false
            and late.reason == "python_environment_unresolved"
            and late.document_version == 5 and late.expected_version == 5)
    check("late normalization does not mutate raw evidence", #raw == 3 and raw[1].code == "reportMissingImports")
    vim.lsp.get_client_by_id = original_client
end

local function buffer_with(lines, filetype)
    local bufnr = vim.api.nvim_create_buf(false, true)
    vim.api.nvim_buf_set_lines(bufnr, 0, -1, false, lines)
    vim.bo[bufnr].filetype = filetype
    return bufnr
end

-- indent_profile: the step is measured between the widths that occur, so a
-- region that starts deep inside a nested block still reports the unit.
local flat = edit.indent_profile({ "a", "  b", "    c" })
check("indent_profile finds a two-space unit", flat.step == 2 and flat.levels == 2, flat)

local nested = edit.indent_profile({ "      a", "        b", "      c" })
check("indent_profile ignores the depth a region starts at",
    nested.step == 2 and nested.levels == 2, nested)

local tabbed = edit.indent_profile({ "a", "\tb", "\t\tc" })
check("indent_profile counts tabs", tabbed.tabs == 2 and tabbed.spaces == 0, tabbed)

-- format_damage: respacing code is what a formatter is for.
local before = {
    "local M = {}",
    "",
    "function M.add(x)",
    "  return x   +  1",
    "end",
    "",
    "return M",
}
local bufnr = buffer_with(before, "lua")
vim.api.nvim_buf_set_lines(bufnr, 3, 4, false, { "  return x + 1" })
check("format_damage allows respacing code",
    edit.format_damage(bufnr, before, 3, 5) == nil,
    vim.api.nvim_buf_get_lines(bufnr, 0, -1, false))

-- A string literal is content. The leading spaces inside one belong to
-- whoever wrote it (an embedded shell script, a here-doc, a test fixture).
local with_string = {
    "local M = {}",
    "",
    "M.script = [[",
    "  restore=$(cat state)",
    "  echo $restore",
    "]]",
    "",
    "return M",
}
bufnr = buffer_with(with_string, "lua")
vim.api.nvim_buf_set_lines(bufnr, 3, 5, false, { " restore=$(cat state)", " echo $restore" })
local harm = edit.format_damage(bufnr, with_string, 3, 6)
check("format_damage catches a rewritten string literal",
    type(harm) == "string" and harm:find("string literal"), harm)

-- Re-indenting the region to the server's own default rewrites every line
-- of the symbol in a file that uses a different width.
local two_space = {
    "local M = {}",
    "",
    "function M.add(x)",
    "  if x then",
    "    return x + 1",
    "  end",
    "end",
    "",
    "return M",
}
bufnr = buffer_with(two_space, "lua")
vim.api.nvim_buf_set_lines(bufnr, 3, 6, false, {
    "    if x then",
    "        return x + 1",
    "    end",
})
harm = edit.format_damage(bufnr, two_space, 3, 7)
check("format_damage catches a re-indent to another width",
    type(harm) == "string" and harm:find("re%-indented"), harm)

-- The same region formatted at the file's own width is fine.
bufnr = buffer_with(two_space, "lua")
vim.api.nvim_buf_set_lines(bufnr, 4, 5, false, { "    return x+1" })
check("format_damage leaves a same-width format alone",
    edit.format_damage(bufnr, two_space, 3, 7) == nil,
    vim.api.nvim_buf_get_lines(bufnr, 0, -1, false))

-- Late attachment: a publish for the exact buffer version an unavailable
-- verdict spoke for is recorded against that transaction; a publish for a
-- newer version is not, and clears the pending verdict; collecting the
-- batches empties them.
do
    local path = "/tmp/huyang-late-test/main.py"
    edit._note_pending_late(path, "edit_t1", "docrev_late_postimage", 5)
    check("late publish for a newer version is not attributed",
        edit._capture_late("pyright", path, 6, {}) == false)
    check("an older publish cannot consume the pending verdict",
        edit._capture_late("pyright", path, 4, {}) == false)
    check("an unversioned publish cannot consume the pending verdict",
        edit._capture_late("pyright", path, nil, {}) == false)
    edit._note_pending_late(path, "edit_t1", "docrev_late_postimage", 5)
    local finding = { range = { start_line = 1, start_character = 1, end_line = 1, end_character = 2 }, severity = 1, message = "late" }
    check("late publish for the matching version is captured",
        edit._capture_late("pyright", path, 5, { finding }, 42) == true)
    local collected = edit.late_evidence()
    local batch = collected.batches[1]
    check("late evidence names the transaction and the reason",
        #collected.batches == 1 and batch.transaction_id == "edit_t1" and batch.reason == "late_attach"
            and batch.document_revision == "docrev_late_postimage" and batch.expected_version == 5 and batch.document_version == 5
            and batch.candidates[1].postimage_version_match == true and batch.findings[1].message == "late"
            and batch.provider_id == "pyright#42",
        batch)
    check("collecting late evidence clears it", #edit.late_evidence().batches == 0)
    check("a later clean publication for the same edit remains observable",
        edit._capture_late("pyright", path, 5, {}, 42) == true)
    local transient = edit.late_evidence().batches[1]
    check("late emptiness is incomplete before quiet", transient and transient.complete == false)
    vim.wait(170)
    local clean = edit.late_evidence().batches[1]
    check("late clean publication preserves producer identity after quiet",
        clean and clean.provider_id == "pyright#42" and #clean.findings == 0 and clean.complete == true)
    edit._capture_late("pyright", path, 5, {}, 42)
    edit._capture_late("pyright", path, 5, { finding }, 42)
    local superseded = edit.late_evidence().batches
    check("a transient late empty set is coalesced before collection",
        #superseded == 1 and #superseded[1].findings == 1 and superseded[1].complete)
    check("an unpended path is ignored", edit._capture_late("pyright", path .. ".other", 5, {}) == false)
    check("a late unversioned positive finding is delivered provisionally",
        edit._capture_late("pyright",path,nil,{finding},42)==true)
    local unversioned=edit.late_evidence().batches[1]
    check("unstamped late findings cannot resolve or claim transaction attribution",
        unversioned.complete==false and unversioned.transaction_id==nil and unversioned.candidates==nil)
end

-- Late progress is the same veto as on the immediate path. Collection must
-- retain an incomplete observation until progress ends, without a new push.
do
    local saved_client = vim.lsp.get_client_by_id
    local fake = { id = 9100, name = "late_progress" }
    vim.lsp.get_client_by_id = function(id) if id == fake.id then return fake end return saved_client(id) end
    local path = vim.fn.getcwd() .. "/late-progress.py"
    edit._note_pending_late(path, "progress_tx", "r", 6, 0)
    vim.api.nvim_exec_autocmds("LspProgress", { data = { client_id = fake.id,
        params = { token = "unit", value = { kind = "begin" } } } })
    edit._capture_late(fake.name, path, 6, {}, fake.id)
    vim.wait(170)
    local during = edit.late_evidence().batches[1]
    check("late progress veto survives quiet", during and not during.complete and during.progress_pending)
    vim.api.nvim_exec_autocmds("LspProgress", { data = { client_id = fake.id,
        params = { token = "unit", value = { kind = "end" } } } })
    local after = edit.late_evidence().batches[1]
    check("late observation completes after progress ends", after and after.complete and not after.progress_pending)
    vim.lsp.get_client_by_id = saved_client
end

-- Checkout reloads keep one LSP lifetime and increasing changedticks. Mixed
-- line endings must preserve both the bytes and physical line coordinates.
do
    local dir = vim.fn.tempname()
    vim.fn.mkdir(dir, "p")
    local function git(args)
        local cmd = { "git", "-C", dir }
        vim.list_extend(cmd, args)
        local out = vim.fn.system(cmd)
        check("checkout fixture git " .. args[1], vim.v.shell_error == 0, out)
    end
    local path = dir .. "/sample.txt"
    local function write(bytes)
        local f = assert(io.open(path, "wb")); f:write(bytes); f:close()
    end
    write("first\nsecond\n")
    git({ "init", "-q" }); git({ "add", "." })
    git({ "-c", "user.name=Unit", "-c", "user.email=unit@localhost", "commit", "-qm", "LF" })
    local first_commit = vim.trim(vim.fn.system({ "git", "-C", dir, "rev-parse", "HEAD" }))
    local buf = core.load_buf(path)
    local tick = vim.api.nvim_buf_get_changedtick(buf)
    write("first\r\nsecond\nthird\r\n")
    git({ "add", "." }); git({ "-c", "user.name=Unit", "-c", "user.email=unit@localhost", "commit", "-qm", "mixed" })
    local mixed_commit = vim.trim(vim.fn.system({ "git", "-C", dir, "rev-parse", "HEAD" }))
    core.load_buf(path)
    check("mixed checkout has three physical buffer lines", vim.api.nvim_buf_line_count(buf) == 3)
    check("mixed endings retain CR only on CRLF lines",
        vim.deep_equal(vim.api.nvim_buf_get_lines(buf,0,-1,false), { "first\r", "second", "third\r" }))
    local function bytes()
        local sep = vim.bo[buf].fileformat == "dos" and "\r\n" or "\n"
        return table.concat(vim.api.nvim_buf_get_lines(buf,0,-1,false),sep) .. (vim.bo[buf].endofline and sep or "")
    end
    check("mixed ending reload preserves exact bytes", bytes() == "first\r\nsecond\nthird\r\n")
    local mixed_tick = vim.api.nvim_buf_get_changedtick(buf)
    git({ "checkout", "-q", first_commit, "--", "sample.txt" })
    core.load_buf(path)
    check("git checkout reloads in place with growing versions", core.load_buf(path) == buf
        and vim.api.nvim_buf_get_changedtick(buf) > mixed_tick and mixed_tick > tick and bytes() == "first\nsecond\n")
    git({ "checkout", "-q", mixed_commit, "--", "sample.txt" })
    core.load_buf(path)
    check("repeated checkout reloads mixed endings", bytes() == "first\r\nsecond\nthird\r\n")
    vim.api.nvim_buf_delete(buf,{force=true})
    vim.fn.delete(dir,"rf")
end

-- Publication payloads, their versions, and the nonblocking baseline travel
-- together even when the editor cache or an older publication disagrees.
do
    local saved_clients = vim.lsp.get_clients
    local saved_client = vim.lsp.get_client_by_id
    local path = vim.fn.getcwd() .. "/huyang-publication-unit.py"
    local buf = buffer_with({ "value = 1" }, "python")
    vim.api.nvim_buf_set_name(buf, path)
    vim.bo[buf].modified = false
    vim.lsp.util.buf_versions[buf] = 4
    local fake = { id = 9001, name = "proof_test", config = { filetypes = { "python" } },
        handlers = { ["textDocument/publishDiagnostics"] = function() end },
        supports_method = function() return false end,
        request_sync = function() error("baseline must not request LSP analysis") end }
    vim.lsp.get_clients = function(filter)
        if filter and filter.bufnr == buf then return { fake } end
        return {}
    end
    edit._wrap_publish_handler(fake)
    local publish = fake.handlers["textDocument/publishDiagnostics"]
    local diagnostic = { range = { start = {line=0,character=0}, ["end"]={line=0,character=1} },
        severity=1,message="bound payload" }
    publish(nil,{uri=vim.uri_from_bufnr(buf),version=4,diagnostics={diagnostic}}, {})
    publish(nil,{uri=vim.uri_from_bufnr(buf),version=3,diagnostics={}}, {})
    local began = vim.uv.hrtime()
    local baseline = edit.diagnostic_evidence({baseline=true,wait_ms=0})
    local elapsed_ms = (vim.uv.hrtime()-began)/1e6
    check("baseline keeps the finding bound to the newest publication",
        #baseline.batches==1 and baseline.batches[1].document_version==4
        and baseline.batches[1].expected_version==4
        and baseline.batches[1].findings[1].message=="bound payload",baseline)
    check("baseline adds no attach or analysis wait",elapsed_ms<25,elapsed_ms)
    vim.lsp.get_client_by_id = function(id)
        if id == fake.id then return fake end
        return saved_client(id)
    end
    vim.api.nvim_exec_autocmds("LspProgress", { data = { client_id = fake.id,
        params = { token = "baseline", value = { kind = "begin" } } } })
    vim.wait(10) -- the token must predate the baseline call
    publish(nil,{uri=vim.uri_from_bufnr(buf),version=4,diagnostics={diagnostic}}, {})
    check("partial baseline during already-open progress is not accepted",
        #edit.diagnostic_evidence({baseline=true,wait_ms=0}).batches==0)
    vim.api.nvim_exec_autocmds("LspProgress", { data = { client_id = fake.id,
        params = { token = "baseline", value = { kind = "end" } } } })
    check("baseline is available again once existing progress ends",
        #edit.diagnostic_evidence({baseline=true,wait_ms=0}).batches==1)
    vim.lsp.get_client_by_id = saved_client
    publish(nil,{uri=vim.uri_from_bufnr(buf),diagnostics={}}, {})
    check("an unstamped empty publication cannot become a clean baseline",
        #edit.diagnostic_evidence({baseline=true,wait_ms=0}).batches==0)
    vim.lsp.get_clients = saved_clients
    vim.api.nvim_buf_delete(buf,{force=true})
end

-- A pull that outlives the edit budget must still reach late evidence. A
-- response after another buffer change must not be charged to the old edit.
do
    local path = vim.fn.tempname() .. ".py"
    vim.fn.writefile({ "value = 1" }, path)
    local buf = core.load_buf(path)
    local saved_clients = vim.lsp.get_clients
    local saved_client = vim.lsp.get_client_by_id
    local saved_attach = vim.lsp.buf_attach_client
    local pull_coroutine
    local pull_items, responses = {}, 0
    local next_request, callbacks, registered = 0, {}, nil
    vim.lsp.util.buf_versions[buf] = 4
    local fake = {id=9002,name="pull_test",config={filetypes={"python"}},
        handlers={["textDocument/publishDiagnostics"]=function() end},
        supports_method=function(_,method) return method=="textDocument/diagnostic" end,
        request=function(_,_,_,callback)
            next_request = next_request + 1
            local id = next_request
            callbacks[id] = callback
            vim.defer_fn(function()
                if not callbacks[id] then return end
                callbacks[id] = nil
                responses = responses + 1
                callback(nil,{kind="full",items=vim.deepcopy(pull_items)})
            end,40)
            return true,id
        end,
        cancel_request=function(_,id) callbacks[id] = nil end}
    registered=fake
    vim.lsp.get_client_by_id = function(id)
        if id == fake.id then return registered end
        return saved_client(id)
    end
    -- Only Huyang's diagnostic request sees this protocol stub. Native LSP
    -- on_lines and VimLeavePre callbacks need the real client registry.
    vim.lsp.get_clients=function(filter)
        if pull_coroutine and coroutine.running()==pull_coroutine then
            if not filter or filter.bufnr==buf then return {fake} end
            return {}
        end
        return saved_clients(filter)
    end
    -- The progress check below also stubs get_client_by_id. Never let the
    -- diagnostic request register this partial client with native tracking.
    vim.lsp.buf_attach_client=function(bufnr,client_id)
        if client_id==fake.id then return true end
        return saved_attach(bufnr,client_id)
    end
    local function timed_pull()
        local result
        local co=coroutine.create(function()
            result=edit.diagnostic_evidence({files={path},wait_ms=5,
                transaction_id="edit_pull",revision="wsrev_pull"})
        end)
        pull_coroutine=co
        local ok,detail=coroutine.resume(co)
        check("late pull starts without blocking",ok,detail)
        vim.wait(500,function() return coroutine.status(co)=="dead" end,1)
        pull_coroutine=nil
        return result
    end
    local result=timed_pull()
    check("a timed-out pull stays incomplete",
        result and #result.batches==1 and result.batches[1].complete==false,result)
    vim.wait(70)
    local late=edit.late_evidence().batches
    check("a late empty pull retains request attribution without producer proof",
        #late==1 and late[1].kind=="lsp_pull" and late[1].reason=="late_pull"
        and late[1].document_version==nil and late[1].expected_version==4
        and late[1].transaction_id=="edit_pull" and late[1].complete==false,late)
    timed_pull()
    vim.api.nvim_buf_set_lines(buf,0,-1,false,{"value = 2"})
    vim.wait(70)
    check("a pull response for superseded bytes is not late current evidence",
        #edit.late_evidence().batches==0)
    -- Queue the response while its proof is still current, then supersede it
    -- before collection. Callback-time checks alone miss every case below.
    local changes = {
        { "buffer change", function()
            vim.api.nvim_buf_set_lines(buf,0,-1,false,{"value = 200"})
        end },
        { "external sync", function()
            vim.fn.writefile({"value = 300"},path)
            core.sync_buf(buf,path)
        end },
        { "disk write", function() vim.fn.writefile({"value = 400"},path) end },
        { "plan invalidation", function() edit.mark_diagnostic_change(buf) end },
        { "version change", function() vim.lsp.util.buf_versions[buf] = 5 end },
        { "client replacement", function() registered = {} end },
        { "client detach", function() fake.attached_buffers[buf] = nil end },
        { "buffer rename", function() vim.api.nvim_buf_set_name(buf,path .. ".renamed") end },
    }
    for _, positive in ipairs({false,true}) do
        for _, change in ipairs(changes) do
            registered=fake
            fake.attached_buffers={[buf]=true}
            vim.api.nvim_buf_set_name(buf,path)
            vim.api.nvim_buf_set_lines(buf,0,-1,false,{"value = 1"})
            vim.bo[buf].modified = false
            vim.fn.writefile({"value = 1"},path)
            core.mark_synced(buf,path)
            edit.mark_diagnostic_change(buf)
            vim.lsp.util.buf_versions[buf] = 4
            pull_items = positive and {{
                range={start={line=0,character=0},["end"]={line=0,character=1}},
                severity=1,message="queued positive",
            }} or {}
            local before = responses
            timed_pull()
            vim.wait(100,function() return responses > before end,1)
            check("pull is queued before " .. change[1],responses==before+1)
            change[2]()
            check("queued " .. (positive and "positive" or "empty") .. " pull rejected after " .. change[1],
                #edit.late_evidence().batches==0)
        end
    end
    -- Progress completion is not a producer document-version stamp, even
    -- when a full response has arrived and its request bytes remain current.
    vim.api.nvim_buf_set_name(buf,path)
    fake.attached_buffers={[buf]=true}
    registered=fake
    vim.lsp.get_client_by_id = function(id)
        if id == fake.id then return fake end
        return saved_client(id)
    end
    edit.mark_diagnostic_change(buf)
    vim.lsp.util.buf_versions[buf] = 4
    pull_items = {}
    timed_pull()
    vim.wait(70)
    vim.api.nvim_exec_autocmds("LspProgress", { data = { client_id = fake.id,
        params = { token = "pull", value = { kind = "begin" } } } })
    local pending = edit.late_evidence().batches[1]
    check("collection-time progress vetoes a queued pull",
        pending and not pending.complete and pending.progress_pending,pending)
    vim.api.nvim_exec_autocmds("LspProgress", { data = { client_id = fake.id,
        params = { token = "pull", value = { kind = "end" } } } })
    local complete = edit.late_evidence().batches[1]
    check("progress end cannot promote the previously delivered empty pull",
        complete==nil,complete)
    check("an unchanged provisional pull is not delivered again",#edit.late_evidence().batches==0)
    edit.mark_diagnostic_change(buf)
    vim.lsp.get_client_by_id=saved_client
    vim.lsp.get_clients=saved_clients
    vim.lsp.buf_attach_client=saved_attach
    vim.api.nvim_buf_delete(buf,{force=true})
    vim.fn.delete(path)
end

-- A pull lane coalesces wakeups, bounds requests/state, and retires its own
-- requests on expiry or cancellation without changing evidence confidence.
do
    local watcher = require("huyang.diagnostic_pull")
    local saved_client = vim.lsp.get_client_by_id
    local clients = {}
    vim.lsp.get_client_by_id=function(id) return clients[id] or saved_client(id) end
    local calls, cancelled, received, callback = 0, 0, 0, nil
    local fake = { id=9011, name="bounded_pull_test",
        request=function(_,_,_,reply)
            calls=calls+1
            callback=reply
            return true,calls
        end,
        cancel_request=function() cancelled=cancelled+1 end }
    clients[fake.id]=fake
    local args = {client=fake,bufnr=1,document="bounded-pull",params={},
        current=function() return true end,
        receive=function() received=received+1 end}
    watcher.start(args)
    for _=1,10 do watcher.wake(fake.id) end
    vim.wait(5)
    check("wakeups do not duplicate an owned in-flight pull",calls==1,calls)
    callback(nil,{kind="full",items={}})
    vim.wait(20,function() return calls==2 end,1)
    check("one deferred fetch follows producer events during a request",calls==2,calls)
    callback(nil,{kind="full",items={}})
    for _=1,20 do
        watcher.wake(fake.id)
        vim.wait(2)
        if calls>received then callback(nil,{kind="full",items={}}) end
    end
    check("a pull lane has a finite request budget",calls==16 and received==16,{calls,received})
    watcher.cancel_document(args.document)

    calls,cancelled=0,0
    local documents={}
    for i=1,65 do
        local document="bounded-pull-" .. i
        documents[#documents+1]=document
        watcher.start({client=fake,bufnr=1,document=document,params={},
            current=function() return true end,receive=function() end})
    end
    check("the watcher cap cancels the oldest retained owned request",calls==65 and cancelled==1,
        {calls,cancelled})
    for _,document in ipairs(documents) do watcher.cancel_document(document) end
    check("cancellation retires every retained owned request",cancelled==65,cancelled)
    local saved_timer, expiry, timer_closed = vim.uv.new_timer, nil, false
    vim.uv.new_timer=function()
        return {
            start=function(_,_,_,expire) expiry=expire end,
            stop=function() end,
            close=function() timer_closed=true end,
        }
    end
    watcher.start(args)
    vim.uv.new_timer=saved_timer
    local before_expiry, before_received=cancelled,received
    expiry()
    vim.wait(20,function() return timer_closed end,1)
    callback(nil,{kind="full",items={}})
    check("deadline cancels the owned request and ignores its later response",
        timer_closed and cancelled==before_expiry+1 and received==before_received,
        {cancelled,received})
    vim.lsp.get_client_by_id=saved_client
end

-- Every owned response may provoke more progress, including after its callback.
-- Bound that feedback without blocking explicit collection/refresh wakeups.
do
    local watcher = require("huyang.diagnostic_pull")
    local saved_client = vim.lsp.get_client_by_id
    local calls, received, callbacks, automatic = 0, {}, {}, true
    local fake = {id=9014,name="progress_feedback_test"}
    vim.lsp.get_client_by_id=function(id)
        if id==fake.id then return fake end
        return saved_client(id)
    end
    local function progress()
        for _,kind in ipairs({"begin","report","end"}) do
            vim.api.nvim_exec_autocmds("LspProgress",{data={client_id=fake.id,
                params={token="owned-request-work",value={kind=kind}}}})
        end
    end
    local function respond(id,items)
        local callback=callbacks[id]
        callbacks[id]=nil
        check("feedback request remains live before response",callback~=nil,id)
        if callback then callback(nil,{kind="full",items=items or {}}) end
        progress()
    end
    fake.request=function(_,_,_,callback)
        calls=calls+1
        local id=calls
        callbacks[id]=callback
        if automatic then vim.schedule(function() respond(id) end) end
        return true,id
    end
    local args={client=fake,bufnr=1,document="progress-feedback",params={},
        current=function() return true end,
        receive=function(result) received[#received+1]=result end}
    watcher.start(args)
    vim.wait(100,function() return #received>=2 end,1)
    vim.wait(10)
    check("owned response progress stops at two pulls instead of exhausting sixteen",
        calls==2 and #received==2,{calls,#received})

    automatic=false
    watcher.wake(fake.id)
    vim.wait(100,function() return calls==3 end,1)
    progress()
    progress()
    watcher.wake(fake.id) -- Re-arm while request 3 and old progress are pending.
    progress()
    local positive={{message="new body after explicit wake"}}
    respond(3,positive)
    vim.wait(100,function() return calls==4 end,1)
    respond(4,positive)
    vim.wait(10)
    check("explicit in-flight wake survives consumed progress allowance and delivers changed body",
        calls==4 and #received==4 and received[4].items[1].message==positive[1].message,
        {calls,received})
    watcher.wake(fake.id)
    vim.wait(100,function() return calls==5 end,1)
    respond(5,positive)
    vim.wait(100,function() return calls==6 end,1)
    respond(6,positive)
    vim.wait(10)
    check("later explicit wake re-arms one progress follow-up without a recursive chain",
        calls==6 and #received==6,{calls,#received})
    watcher.cancel_document(args.document)
    vim.lsp.get_client_by_id=saved_client
end

-- Full pull bodies can change their opaque ID before analysis begins.
-- Producer events fetch new facts, but only a stamped push can prove absence.
for _, final_positive in ipairs({false,true}) do
    local path = vim.fn.tempname() .. ".py"
    vim.fn.writefile({ "value = 1" }, path)
    local buf = core.load_buf(path)
    local saved_clients, saved_client = vim.lsp.get_clients, vim.lsp.get_client_by_id
    local saved_attach = vim.lsp.buf_attach_client
    local pull_coroutine, requests, refreshes = nil, 0, 0
    local diagnostic = { range={start={line=0,character=0},["end"]={line=0,character=1}},
        severity=1,message="delayed assignment error" }
    local response = { kind="full", resultId="before", items={diagnostic} }
    local fake = { id=9010, name="causal_pull_test", config={filetypes={"python"}},
        handlers={
            ["textDocument/publishDiagnostics"]=function() end,
            ["workspace/diagnostic/refresh"]=function() refreshes=refreshes+1; return "native refresh" end,
        },
        supports_method=function(_,method) return method=="textDocument/diagnostic" end,
        request=function(_,_,_,callback)
            requests=requests+1
            callback(nil,vim.deepcopy(response))
            return true,requests
        end }
    vim.lsp.get_client_by_id=function(id)
        if id==fake.id then return fake end
        return saved_client(id)
    end
    vim.lsp.get_clients=function(filter)
        if pull_coroutine and coroutine.running()==pull_coroutine then
            if not filter or filter.bufnr==buf then return {fake} end
            return {}
        end
        return saved_clients(filter)
    end
    vim.lsp.buf_attach_client=function(target,id)
        if id==fake.id then return true end
        return saved_attach(target,id)
    end
    local function pull()
        local result
        local co=coroutine.create(function()
            result=edit.diagnostic_evidence({files={path},wait_ms=50,
                transaction_id="edit_causal_pull",revision="wsrev_causal_pull"})
        end)
        pull_coroutine=co
        local ok,detail=coroutine.resume(co)
        check("causal pull request starts",ok,detail)
        vim.wait(200,function() return coroutine.status(co)=="dead" end,1)
        pull_coroutine=nil
        return result and result.batches[1]
    end
    vim.lsp.util.buf_versions[buf]=8
    local before=pull()
    check("positive full pull does not invent producer version proof",
        before and not before.complete and before.document_version==nil and #before.findings==1,before)
    edit.mark_diagnostic_change(buf)
    vim.lsp.util.buf_versions[buf]=9
    response={kind="full",resultId="advanced-before-progress",items={}}
    local empty=pull()
    check("advanced opaque ID before progress does not certify clean",
        empty and empty.result_id=="advanced-before-progress" and not empty.complete
            and empty.document_version==nil and empty.expected_version==9,empty)
    local function progress(kind)
        vim.api.nvim_exec_autocmds("LspProgress",{data={client_id=fake.id,
            params={token="analysis",value={kind=kind}}}})
    end
    progress("begin")
    response={kind="full",resultId="analyzed",items={diagnostic}}
    local expected_requests=requests+1
    progress("end")
    vim.wait(100,function() return requests==expected_requests end,1)
    local late=edit.late_evidence().batches
    check("producer progress fetches and delivers the delayed finding provisionally",
        #late==1 and not late[1].complete and late[1].document_version==nil
            and late[1].expected_version==9 and late[1].transaction_id=="edit_causal_pull"
            and late[1].findings[1].message=="delayed assignment error",late)
    vim.wait(10)
    check("repeated pull payload is delivered once",#edit.late_evidence().batches==0)
    response={kind="full",resultId="different-findings",items={{
        range=diagnostic.range,severity=1,message="another delayed error",
    }}}
    local native=fake.handlers["workspace/diagnostic/refresh"](nil,nil,{client_id=fake.id})
    expected_requests=requests+1
    vim.wait(100,function() return requests==expected_requests end,1)
    local refreshed=edit.late_evidence().batches
    check("native refresh handler is preserved and fetches another provisional body",
        native=="native refresh" and refreshes==1 and #refreshed==1
            and not refreshed[1].complete and refreshed[1].document_version==nil
            and refreshed[1].findings[1].message=="another delayed error",refreshed)
    -- Queue the push first to make delivery independent of arrival/hash order.
    local pushed={range=diagnostic.range,severity=1,message="unstamped push finding"}
    fake.handlers["textDocument/publishDiagnostics"](nil,
        {uri=vim.uri_from_bufnr(buf),diagnostics={pushed}}, {})
    response={kind="full",resultId="simultaneous-provisional",items={{
        range=diagnostic.range,severity=1,message="simultaneous pull finding",
    }}}
    expected_requests=requests+1
    fake.handlers["workspace/diagnostic/refresh"](nil,nil,{client_id=fake.id})
    vim.wait(100,function() return requests==expected_requests end,1)
    local pending_streams=edit.late_evidence().batches
    check("simultaneous provisional push and pull retain both lanes in one RPC",
        #pending_streams==2 and pending_streams[1].kind=="lsp_push" and not pending_streams[1].complete
            and pending_streams[1].document_version==nil and pending_streams[1].transaction_id==nil
            and pending_streams[1].findings[1].message==pushed.message
            and pending_streams[2].kind=="lsp_pull" and not pending_streams[2].complete
            and pending_streams[2].findings[1].message=="simultaneous pull finding",pending_streams)
    check("both provisional slots are delivered exactly once",#edit.late_evidence().batches==0)

    -- Now queue the pull first and let the stamped push become complete before
    -- either is collected. Neither stream may swallow the other.
    response={kind="full",resultId="simultaneous-authority",items={{
        range=diagnostic.range,severity=1,message="pull before authoritative push",
    }}}
    expected_requests=requests+1
    fake.handlers["workspace/diagnostic/refresh"](nil,nil,{client_id=fake.id})
    vim.wait(100,function() return requests==expected_requests end,1)
    local final_items=final_positive and {diagnostic} or {}
    fake.handlers["textDocument/publishDiagnostics"](nil,
        {uri=vim.uri_from_bufnr(buf),version=9,diagnostics=final_items}, {})
    vim.wait(170)
    local before_proof=edit.late_evidence().batches
    check("complete push cannot swallow a simultaneous provisional pull",
        #before_proof==2 and before_proof[1].kind=="lsp_pull" and not before_proof[1].complete
            and before_proof[1].document_version==nil
            and before_proof[1].findings[1].message=="pull before authoritative push",before_proof)
    local proof=before_proof[2]
    check("current push retains its own authoritative body alongside the pull",
        proof and proof.kind=="lsp_push" and proof.complete
            and proof.document_version==9 and proof.expected_version==9
            and proof.transaction_id=="edit_causal_pull"
            and #proof.findings==#final_items
            and (not final_positive or proof.findings[1].message==diagnostic.message),proof)
    check("unchanged stream payloads are not delivered twice",
        #edit.late_evidence().batches==0)
    local stopped=requests
    vim.wait(10)
    edit.late_evidence()
    vim.wait(10)
    check("a versioned push leaves the independent bounded pull lane alive",requests>stopped,requests)
    edit.mark_diagnostic_change(buf)
    vim.lsp.get_clients, vim.lsp.get_client_by_id = saved_clients, saved_client
    vim.lsp.buf_attach_client = saved_attach
    vim.api.nvim_buf_delete(buf,{force=true})
    vim.fn.delete(path)
end

-- Real Neovim clients use boolean attachment entries in 0.11 and language IDs
-- in 0.12. Exercise the owned request boundary with both representations, then
-- reject detached buffers and stale client objects before dispatch and delivery.
do
    local pull = require("huyang.diagnostic_pull")
    local saved_client = vim.lsp.get_client_by_id
    local buf = vim.api.nvim_create_buf(false, true)
    local document = "attachment-compatibility"
    local client = {id=9013,attached_buffers={}}
    local registered = client
    vim.lsp.get_client_by_id = function(id)
        if id == client.id then return registered end
        return saved_client(id)
    end
    local requests, received, callback = 0, 0, nil
    client.request = function(_,_,_,receive)
        requests = requests + 1
        callback = receive
        return true, requests
    end
    local function start()
        callback = nil
        pull.start({client=client,bufnr=buf,document=document,params={},
            current=function() return true end,
            receive=function(result)
                if result and result.items[1].message == "late positive" then received = received + 1 end
            end})
    end
    local positive = {kind="full",items={{message="late positive"}}}
    for _, attachment in ipairs({true,"python"}) do
        client.attached_buffers[buf] = attachment
        local before_requests, before_received = requests, received
        start()
        check("owned pull dispatches for attachment " .. tostring(attachment),
            requests == before_requests + 1 and callback ~= nil)
        if callback then callback(nil,positive) end
        check("owned pull receives positive for attachment " .. tostring(attachment),
            received == before_received + 1)
        pull.cancel_document(document,client.id)
    end
    for _, invalid in ipairs({"nil attachment","false attachment","replaced client","missing client"}) do
        local function invalidate()
            if invalid == "nil attachment" then client.attached_buffers[buf] = nil
            elseif invalid == "false attachment" then client.attached_buffers[buf] = false
            elseif invalid == "replaced client" then registered = {id=client.id}
            else registered = nil end
        end
        registered, client.attached_buffers[buf] = client, "python"
        invalidate()
        local before_requests = requests
        start()
        check(invalid .. " blocks owned pull dispatch",requests == before_requests and callback == nil)
        pull.cancel_document(document,client.id)
        registered, client.attached_buffers[buf] = client, "python"
        start()
        local pending, before_received = callback, received
        invalidate()
        if pending then pending(nil,positive) end
        check(invalid .. " rejects an in-flight positive",pending ~= nil and received == before_received)
        pull.cancel_document(document,client.id)
    end
    vim.lsp.get_client_by_id = saved_client
    vim.api.nvim_buf_delete(buf,{force=true})
end

-- Native refresh cancels buffer diagnostic requests and may drop their RPC
-- callbacks entirely. Exercise that asynchronous lifecycle, including a stale
-- callback after replacement, rather than answering inside client:request.
do
    local path = vim.fn.tempname() .. ".py"
    vim.fn.writefile({"value = 1"},path)
    local buf = core.load_buf(path)
    local saved_clients, saved_client = vim.lsp.get_clients, vim.lsp.get_client_by_id
    local saved_attach = vim.lsp.buf_attach_client
    local observer = require("huyang.diagnostic_observer")
    local seam_events = {}
    observer.install(function(event) seam_events[#seam_events+1]=event end)
    local function accepted_count()
        local count=0
        for _,event in ipairs(seam_events) do
            if event.stage=="accepted_receive" then count=count+1 end
        end
        return count
    end
    local pull_coroutine, requests, refreshes = nil, 0, 0
    local pending, callbacks, emit_cancel = {}, {}, true
    local diagnostic = {range={start={line=0,character=0},["end"]={line=0,character=1}},
        severity=1,code="E0308",source="rust-analyzer",message="expected i32, found &'static str"}
    local push_diagnostic = {range=diagnostic.range,severity=1,code="E0308",source="rustc",
        message="mismatched types expected i32, found &str"}
    local fake = {id=9012,name="refresh_cancel_test",config={filetypes={"python"}},
        attached_buffers={[buf]=true},requests={},
        handlers={["textDocument/publishDiagnostics"]=function() end},
        supports_method=function(_,method) return method=="textDocument/diagnostic" end}
    fake.request=function(_,method,_,callback,bufnr)
        requests=requests+1
        pending[requests],callbacks[requests]=callback,callback
        fake.requests[requests]={type="pending",method=method,bufnr=bufnr}
        return true,requests
    end
    fake.cancel_request=function(_,id)
        pending[id]=nil -- RequestCancelled is swallowed by the native RPC layer.
        if fake.requests[id] then fake.requests[id].type="cancel" end
        if emit_cancel then
            vim.api.nvim_exec_autocmds("LspRequest",{buffer=buf,data={
                client_id=fake.id,request_id=id,request=fake.requests[id],
            }})
        end
    end
    fake.handlers["workspace/diagnostic/refresh"]=function()
        refreshes=refreshes+1
        for id,request in pairs(fake.requests) do
            if request.type=="pending" and request.bufnr==buf then fake:cancel_request(id) end
        end
        return "native asynchronous refresh"
    end
    vim.lsp.get_client_by_id=function(id)
        if id==fake.id then return fake end
        return saved_client(id)
    end
    vim.lsp.get_clients=function(filter)
        if pull_coroutine and coroutine.running()==pull_coroutine then
            if not filter or filter.bufnr==buf then return {fake} end
            return {}
        end
        return saved_clients(filter)
    end
    vim.lsp.buf_attach_client=function(target,id)
        if id==fake.id then return true end
        return saved_attach(target,id)
    end
    local function launch(wait_ms)
        local state={started=vim.uv.hrtime()}
        state.co=coroutine.create(function()
            state.result=edit.diagnostic_evidence({files={path},wait_ms=wait_ms or 500,
                transaction_id="edit_async_refresh",revision="wsrev_async_refresh"})
        end)
        pull_coroutine=state.co
        local ok,detail=coroutine.resume(state.co)
        check("asynchronous pull coroutine starts",ok,detail)
        return state
    end
    local function finish(state)
        local done=vim.wait(200,function() return coroutine.status(state.co)=="dead" end,1)
        check("asynchronous evidence caller resumes promptly",done)
        pull_coroutine=nil
        return state.result and state.result.batches or {},(vim.uv.hrtime()-state.started)/1e6
    end
    local function reply(id,items)
        local callback=pending[id]
        pending[id],fake.requests[id]=nil,nil
        check("owned asynchronous request is still live before reply",callback~=nil,id)
        if callback then callback(nil,{kind="full",items=vim.deepcopy(items)}) end
    end
    local function progress(kind)
        vim.api.nvim_exec_autocmds("LspProgress",{data={client_id=fake.id,
            params={token="async-analysis",value={kind=kind}}}})
    end
    local function publish(version,items)
        fake.handlers["textDocument/publishDiagnostics"](nil,
            {uri=vim.uri_from_bufnr(buf),version=version,diagnostics=items},{})
    end

    vim.lsp.util.buf_versions[buf]=20
    local first=launch()
    reply(requests,{})
    local initial=finish(first)
    check("initial asynchronous empty pull stays provisional",
        #initial==1 and not initial[1].complete and initial[1].document_version==nil,initial)
    progress("begin")
    progress("end")
    vim.wait(50,function() return requests==2 end,1)
    local cancelled_id=requests
    local native=fake.handlers["workspace/diagnostic/refresh"](nil,nil,{client_id=fake.id})
    vim.wait(50,function() return requests==cancelled_id+1 end,1)
    local replacement=requests
    check("progress then native refresh replaces a cancelled callback-less request",
        native=="native asynchronous refresh" and refreshes==1 and replacement==cancelled_id+1
            and pending[cancelled_id]==nil and pending[replacement]~=nil,
        {requests=requests,refreshes=refreshes})
    local accepted_before_cancel=accepted_count()
    callbacks[cancelled_id](nil,{kind="full",items={diagnostic}})
    check("discarded cancelled callback gets no accepted-origin grant",accepted_count()==accepted_before_cancel)
    require("huyang.diagnostic_pull").wake(fake.id)
    vim.wait(5)
    check("stale cancelled callback cannot clear the replacement in flight",
        requests==replacement and pending[replacement]~=nil,requests)
    reply(replacement,{diagnostic})
    -- An empty unstamped push must not overwrite the undelivered positive
    -- pull or suppress the same body when the next collection fetches it.
    publish(nil,{})
    local late=edit.late_evidence().batches
    check("unstamped empty push preserves the undelivered late pull finding",
        #late==1 and late[1].kind=="lsp_pull" and not late[1].complete
            and late[1].document_version==nil and late[1].expected_version==20
            and late[1].transaction_id=="edit_async_refresh"
            and late[1].findings[1].message==diagnostic.message,late)
    vim.wait(50,function() return requests==replacement+1 end,1)
    reply(requests,{diagnostic})
    check("preserved late positive is delivered exactly once",
        #edit.late_evidence().batches==0)

    -- The refresh wrapper also recovers if LspRequest delivery is delayed or
    -- unavailable: native client.requests already marks the request cancelled.
    edit.mark_diagnostic_change(buf)
    vim.lsp.util.buf_versions[buf]=21
    emit_cancel=false
    local cancelled_wait=launch()
    local before_cancel=requests
    fake.handlers["workspace/diagnostic/refresh"](nil,nil,{client_id=fake.id})
    local cancelled_batches,cancelled_ms=finish(cancelled_wait)
    vim.wait(50,function() return requests==before_cancel+1 end,1)
    check("native cancellation wakes the immediate caller before its full budget",
        cancelled_ms<200 and #cancelled_batches==1 and not cancelled_batches[1].complete
            and requests==before_cancel+1,{cancelled_ms,cancelled_batches,requests})
    edit.mark_diagnostic_change(buf)
    vim.lsp.util.buf_versions[buf]=22
    local closed_wait=launch()
    edit.mark_diagnostic_change(buf)
    local closed_batches,closed_ms=finish(closed_wait)
    check("closing a lane wakes its immediate caller without waiting for expiry",
        closed_ms<200 and #closed_batches==1 and not closed_batches[1].complete,
        {closed_ms,closed_batches})

    -- A changedtick can move without an LSP version change. A repeated
    -- collection for the same transaction must bind a new pending postimage.
    vim.lsp.util.buf_versions[buf]=23
    local old=launch(5)
    finish(old)
    local old_id=requests
    reply(old_id,{diagnostic})
    vim.api.nvim_buf_set_lines(buf,0,-1,false,{"value = 2"})
    vim.fn.writefile({"value = 2"},path)
    vim.bo[buf].modified=false
    core.mark_synced(buf,path)
    local renewed=launch()
    local renewed_id=requests
    reply(renewed_id,{diagnostic})
    local renewed_batches=finish(renewed)
    check("same-version changedtick collection binds a fresh pending postimage",
        renewed_id==old_id+1 and #renewed_batches==1
            and #renewed_batches[1].findings==1 and not renewed_batches[1].complete,
        renewed_batches)
    check("same-version supersession rejects the old queued postimage",
        #edit.late_evidence().batches==0)

    -- A push wakes the immediate caller without cancelling its independent
    -- pull. Repeat across edits to prevent learned push mode suppressing pulls.
    edit.mark_diagnostic_change(buf)
    vim.lsp.util.buf_versions[buf]=24
    local learned=launch()
    publish(24,{push_diagnostic})
    local learned_batches,learned_ms=finish(learned)
    check("current push arriving during pull wakes and supplies immediate authority",
        learned_ms<200 and #learned_batches==2 and learned_batches[2].kind=="lsp_push"
            and learned_batches[2].complete and learned_batches[2].document_version==24
            and #learned_batches[2].findings==1,{learned_ms,learned_batches})
    check("immediately delivered authoritative push is not queued again",
        #edit.late_evidence().batches==0)
    local alive_id=requests
    check("push arrival keeps the original pull request in flight",pending[alive_id]~=nil,alive_id)
    reply(alive_id,{diagnostic})
    require("huyang.diagnostic_pull").wake(fake.id)
    vim.wait(50,function() return requests==alive_id+1 end,1)
    reply(requests,{})
    local after_push=edit.late_evidence().batches
    check("provisional empty pull preserves undelivered independent positive after push",
        #after_push==2 and after_push[1].kind=="lsp_pull" and not after_push[1].complete
            and #after_push[1].findings==1 and after_push[1].findings[1].source=="rust-analyzer"
            and after_push[2].kind=="lsp_pull" and not after_push[2].complete
            and #after_push[2].findings==0,after_push)
    for version=25,26 do
        vim.api.nvim_buf_set_lines(buf,0,-1,false,{"value = " .. version})
        vim.fn.writefile({"value = " .. version},path)
        vim.bo[buf].modified=false
        core.mark_synced(buf,path)
        edit.mark_diagnostic_change(buf)
        vim.lsp.util.buf_versions[buf]=version
        local before_requests=requests
        local current=launch()
        publish(version,{push_diagnostic})
        local current_batches,current_ms=finish(current)
        check("current push preserves independent pull on edited revision " .. version,
            current_ms<200 and requests==before_requests+1 and #current_batches==2
                and current_batches[1].kind=="lsp_pull" and not current_batches[1].complete
                and current_batches[2].kind=="lsp_push" and current_batches[2].complete
                and current_batches[2].document_version==version
                and current_batches[2].expected_version==version
                and #current_batches[2].findings==1 and pending[requests]~=nil,
            {current_ms,current_batches,requests})
    end
    -- A changed opaque identity with the same empty body is useful
    -- correlation evidence, with no producer-version or omission authority.
    edit.mark_diagnostic_change(buf)
    vim.lsp.util.buf_versions[buf]=27
    local identity=launch()
    local initial_callback=pending[requests]
    pending[requests],fake.requests[requests]=nil,nil
    if initial_callback then initial_callback(nil,{kind="full",resultId="9",items={}}) end
    local identity_batches=finish(identity)
    check("initial empty pull retains result identity without completeness",
        #identity_batches==1 and identity_batches[1].result_id=="9"
            and not identity_batches[1].complete,identity_batches)
    local function identity_reply(result,lsp_err)
        local before_requests=requests
        require("huyang.diagnostic_pull").wake(fake.id)
        vim.wait(50,function() return requests==before_requests+1 end,1)
        local callback=pending[requests]
        pending[requests],fake.requests[requests]=nil,nil
        check("identity refresh has an owned live request",callback~=nil,requests)
        if callback then
            observer.with_raw({request_id="unit:" .. requests,client_id=fake.id},callback,lsp_err,result)
        end
    end
    identity_reply({kind="full",resultId="12",items={}})
    local changed_identity=edit.late_evidence().batches
    check("same empty body with result nine to twelve delivers exact provisional attribution",
        #changed_identity==1 and changed_identity[1].result_id=="12"
            and changed_identity[1].kind=="lsp_pull" and changed_identity[1].reason=="late_pull"
            and changed_identity[1].document==path
            and changed_identity[1].document_revision=="wsrev_async_refresh"
            and changed_identity[1].transaction_id=="edit_async_refresh"
            and changed_identity[1].expected_version==27
            and changed_identity[1].document_version==nil and not changed_identity[1].complete
            and changed_identity[1].selected and changed_identity[1].dimension=="edited_documents"
            and #changed_identity[1].findings==0,changed_identity)
    local represented
    for _,event in ipairs(seam_events) do
        if event.stage=="late_emission" and event.batch.result_id=="12" then represented=event end
    end
    local before_duplicate=#seam_events
    identity_reply({kind="full",resultId="12",items={}})
    local duplicate_enqueues=0
    for i=before_duplicate+1,#seam_events do
        if seam_events[i].stage=="enqueued" then duplicate_enqueues=duplicate_enqueues+1 end
    end
    check("actual dedup retains original enqueue and emission origin",
        represented and represented.identity.origin.raw.request_id~=nil
            and represented.identity.queue_sequence==represented.queue_sequence
            and duplicate_enqueues==0)
    check("repeated empty result twelve is delivered exactly once",
        #edit.late_evidence().batches==0)
    identity_reply({kind="unchanged",resultId="13"})
    check("non-full identity change does not fabricate a late full response",
        #edit.late_evidence().batches==0)
    identity_reply({kind="full",resultId="13",items={}}, {code=-32800,message="cancelled"})
    check("error body identity change does not fabricate a late full response",
        #edit.late_evidence().batches==0)
    identity_reply({kind="full",resultId="12",items={}})
    check("non-full and error responses cannot change the last full response signature",
        #edit.late_evidence().batches==0)

    edit.mark_diagnostic_change(buf)
    local emitted_with_queue=0
    for _,event in ipairs(seam_events) do
        if event.stage=="late_emission" and event.batch.kind=="lsp_pull" then
            local found=false
            for _,queued in ipairs(seam_events) do
                if queued.stage=="enqueued" and queued.identity
                    and queued.identity.batch_id==event.identity.batch_id
                    and queued.queue_sequence==event.queue_sequence then found=true end
            end
            check("actual late emission has its observed enqueue",found,event)
            emitted_with_queue=emitted_with_queue+1
        end
    end
    check("actual asynchronous seam exercised late queue delivery",emitted_with_queue>0)
    observer.install(nil)
    vim.lsp.get_clients,vim.lsp.get_client_by_id=saved_clients,saved_client
    vim.lsp.buf_attach_client=saved_attach
    vim.api.nvim_buf_delete(buf,{force=true})
    vim.fn.delete(path)
end

-- map_region: where an edited region ends up once polishing has moved it,
-- and which old lines below it the ledger has to fold in so an undo puts
-- the whole change back.
local function lines_of(n, mark)
    local out = {}
    for i = 1, n do out[i] = (mark or "line ") .. i end
    return out
end

local base = lines_of(10)

local shifted = vim.list_slice(base, 1, 10)
table.insert(shifted, 2, "new")
local first_line, last_line, extra = edit.map_region(base, shifted, 5, 6)
check("map_region shifts a region by what was added above it",
    first_line == 6 and last_line == 7 and #extra == 0,
    { first_line, last_line, extra })

local grown = vim.list_slice(base, 1, 10)
table.insert(grown, 6, "new")
first_line, last_line, extra = edit.map_region(base, grown, 5, 6)
check("map_region grows a region by what was added inside it",
    first_line == 5 and last_line == 7 and #extra == 0,
    { first_line, last_line, extra })

local below = vim.list_slice(base, 1, 10)
below[8] = "reformatted"
first_line, last_line, extra = edit.map_region(base, below, 5, 6)
check("map_region reaches down to what polishing changed below",
    first_line == 5 and last_line == 8 and #extra == 2
    and extra[1] == "line 7" and extra[2] == "line 8",
    { first_line, last_line, extra })

-- The case that made undo destructive: a range format answered with edits
-- for the whole document. Every line differs, so the region cannot be
-- anchored to a mark - but it must still start where the edit was, or the
-- ledger records the file as the edit and undo replaces it wholesale.
local rewritten = lines_of(10, "  line ")
first_line, last_line, extra = edit.map_region(base, rewritten, 5, 6)
check("map_region keeps its start when the whole file was rewritten",
    first_line == 5 and last_line == 10 and #extra == 4,
    { first_line, last_line, extra })

first_line, last_line, extra = edit.map_region(base, base, 5, 6)
check("map_region leaves an untouched buffer alone",
    first_line == 5 and last_line == 6 and #extra == 0,
    { first_line, last_line, extra })

-- The QML JavaScript guard. A .js file that opens with QML's own directives
-- is not JavaScript any TypeScript server can parse, so the server that
-- attached to it is detached and what it published is discarded; this is the
-- detection that decides it. No server is needed to check the decision, and
-- none of the ones the smoke test can start would attach to a .js file
-- anyway.
local core = require("huyang.core")

local function named_buffer(name, lines)
    local bufnr = vim.api.nvim_create_buf(false, true)
    vim.api.nvim_buf_set_name(bufnr, name)
    vim.api.nvim_buf_set_lines(bufnr, 0, -1, false, lines)
    return bufnr
end

check("a .pragma header marks a file as QML JavaScript",
    core.dialect_note(named_buffer("/tmp/agent99-unit/Sanitize.js",
        { "// helpers", "", ".pragma library", "function f() {}" })) ~= nil)

check("an .import header counts too",
    core.dialect_note(named_buffer("/tmp/agent99-unit/Helper.js",
        { '.import "Sanitize.js" as Sanitize', "function g() {}" })) ~= nil)

-- The directives come before any code. A dot at the start of a line further
-- down is a method call split across lines, not a QML header.
check("plain JavaScript is left alone",
    core.dialect_note(named_buffer("/tmp/agent99-unit/plain.js",
        { "const x = 1;", ".pragma library" })) == nil)

check("a .qml file is not a JavaScript library",
    core.dialect_note(named_buffer("/tmp/agent99-unit/Panel.qml",
        { ".pragma library" })) == nil)

-- reindented_kept_line: the guard that keeps an import pass from rewriting a
-- file's indentation. It has to fire on the pass that re-indents the lines it
-- kept, and stay quiet on the pass that only adds or removes imports - a file
-- where one text sits at two depths (`}` in every Go, TypeScript and C file
-- there is) must not read as re-indented.
local go_before = {
    "package main",
    "",
    "import (",
    "\t\"fmt\"",
    ")",
    "",
    "func a() {",
    "\tif true {",
    "\t\tfmt.Println(\"x\")",
    "\t}",
    "}",
}
local go_after = vim.deepcopy(go_before)
table.insert(go_after, 5, "\t\"os\"")
check("an import added leaves the kept lines alone",
    edit.reindented_kept_line(go_before, go_after) == nil,
    edit.reindented_kept_line(go_before, go_after))

local ts_before = {
    "import { a } from \"./a\";",
    "import {",
    "  b,",
    "} from \"./b\";",
    "",
    "export function f() {",
    "  return a;",
    "}",
}
local ts_after = vim.deepcopy(ts_before)
ts_after[3] = "    b,"
check("a re-indented import block is caught",
    edit.reindented_kept_line(ts_before, ts_after) == "b,",
    edit.reindented_kept_line(ts_before, ts_after))

-- The guard reads lines rather than syntax, so the fixture is lines: a text
-- the pass removed one of cannot be paired occurrence by occurrence, and
-- pairing it anyway would call an untouched line below it re-indented.
local dropped_before = { "keep", "\tdrop", "}", "\t}", "\t\tdrop" }
local dropped_after = { "keep", "}", "\t}", "\t\tdrop" }
check("a text the pass removed a line of is left unpaired",
    edit.reindented_kept_line(dropped_before, dropped_after) == nil,
    edit.reindented_kept_line(dropped_before, dropped_after))

-- A move loads the files that referenced a moved symbol so the server
-- checks them, and only a diagnostic naming one of those symbols is the move's
-- doing. The two ways to get this wrong both shipped: charging the whole file
-- to the move put three pre-existing warnings from an untouched Go test file
-- into one move's verdict, and the fix for that dropped everything else in
-- those files on the floor - an unresolved-import error in a file the move had
-- genuinely broken vanished from the reply while the same error in an
-- unrelated open file was reported.
check("a message naming the moved symbol is the move's doing",
    edit.mentions_name('"has_triple_quotes" is unknown import symbol',
        { "has_triple_quotes" }) == true, "not matched")
check("a longer word merely containing the name is not",
    edit.mentions_name("address is not defined", { "add" }) == false, "matched")
check("a name that is only a suffix of a word is not",
    edit.mentions_name("reshout is undefined", { "shout" }) == false, "matched")
check("an unrelated error in the same file is not the move's doing",
    edit.mentions_name('Import "black.strings" could not be resolved',
        { "has_triple_quotes" }) == false, "matched")
-- The symbol index spells a Go method `(*Archiver).Do`, which begins with
-- punctuation: a leading word-frontier there can never match, so it must not
-- be applied on that side.
check("a name carrying pattern magic still matches",
    edit.mentions_name("undefined: (*Archiver).Do", { "(*Archiver).Do" }) == true, "not matched")
check("a name in backticks matches",
    edit.mentions_name("undefined global `shout`", { "shout" }) == true, "not matched")
check("no names means nothing is the move's doing",
    edit.mentions_name("anything at all", {}) == false, "matched")
check("a nil message is not a match",
    edit.mentions_name(nil, { "shout" }) == false, "matched")

-- The honest hedge, and what it is gated on. A server's silence about a file
-- is only evidence once that server has published something about THAT file:
-- gating it per server instead made one diagnostic in one file answer for
-- every other file in the project, which is how a file nothing had analysed
-- came back flatly clean. Driven through the same DiagnosticChanged path a
-- real publish takes, with a namespace named the way vim.lsp names its own.
local function hedge_buffer(path, filetype)
    local b = vim.api.nvim_create_buf(false, true)
    vim.api.nvim_buf_set_name(b, path)
    vim.bo[b].filetype = filetype
    return b
end

local function publish(bufnr, server, message)
    local ns = vim.api.nvim_create_namespace("nvim.lsp." .. server .. ".1")
    vim.api.nvim_exec_autocmds("DiagnosticChanged", {
        buffer = bufnr,
        data = {
            diagnostics = {
                {
                    bufnr = bufnr, lnum = 0, col = 0, namespace = ns,
                    severity = vim.diagnostic.severity.ERROR, message = message,
                },
            },
        },
    })
end

local analysed = hedge_buffer("/tmp/agent99-unit-hedge/analysed.zig", "zig")
local untouched = hedge_buffer("/tmp/agent99-unit-hedge/untouched.zig", "zig")

local who, how = edit.silent_server(untouched, { "fakels" }, 0)
check("a server that has published nothing hedges, and says so of the session",
    who == "fakels" and how == "at all in this session", { who, how })

publish(analysed, "fakels", "something is wrong here")

who, how = edit.silent_server(analysed, { "fakels" }, 0)
check("the file the server did publish for is not hedged", who == nil, { who, how })

who, how = edit.silent_server(untouched, { "fakels" }, 0)
check("a diagnostic in one file does not certify another file",
    who == "fakels", { who, how })
check("and the hedge narrows to what is actually unknown about this file",
    how == "for this file in this session", { who, how })

-- Two servers on one file: the hedge is gone only when one of them has
-- spoken about this file. The other having spoken about some other file is
-- exactly the evidence that is not evidence.
local both = hedge_buffer("/tmp/agent99-unit-hedge/both.zig", "zig")
who = edit.silent_server(both, { "fakels", "otherls" }, 0)
check("both servers silent about this file still hedges, naming both",
    who == "fakels and otherls", who)
publish(both, "otherls", "and here")
check("one of them publishing for this file ends the hedge",
    edit.silent_server(both, { "fakels", "otherls" }, 0) == nil, who)

-- Liveness. A server that published and is no longer running leaves its
-- diagnostics in the editor with nothing refreshing them, so "0 elsewhere" is
-- no longer an answer about the project. No LSP client is running under the
-- unit tests, so every server that has published here counts as stopped.
local gone = edit.stopped_servers()
check("a server that published and is not running is reported as stopped",
    vim.tbl_contains(gone, "fakels") and vim.tbl_contains(gone, "otherls"), gone)

-- Diagnostic evidence has one attach budget per request, not per file.
-- Files with no enabled/startable server report why immediately.
local latency_dir = vim.fn.tempname()
vim.fn.mkdir(latency_dir, "p")
local no_config = {}
for i = 1, 3 do
    local path = latency_dir .. "/plain" .. i .. ".huyang-no-lsp"
    vim.fn.writefile({ "plain text" }, path)
    no_config[#no_config + 1] = path
end

local started = vim.uv.hrtime()
local evidence = edit.diagnostic_evidence({ files = no_config, wait_ms = 1000 })
local elapsed_ms = (vim.uv.hrtime() - started) / 1e6
local unavailable = #evidence.batches == #no_config
for _, batch in ipairs(evidence.batches) do
    unavailable = unavailable and batch.kind == "unavailable"
        and batch.reason == "lsp_not_configured"
end
check("diagnostic evidence reports an explicit unavailable reason with no LSP",
    unavailable, evidence)
check("diagnostic evidence does not wait when no LSP is configured",
    elapsed_ms < 750, elapsed_ms)

-- An enabled config whose command cannot launch is unavailable immediately too.
vim.filetype.add({ extension = { huyangnostart = "huyang_no_start_test" } })
local no_start_path = latency_dir .. "/nostart.huyangnostart"
vim.fn.writefile({ "plain text" }, no_start_path)
vim.fn.bufload(vim.fn.bufadd(no_start_path))
vim.lsp.config("huyang_no_start_ls", {
    cmd = { latency_dir .. "/missing-language-server" },
    filetypes = { "huyang_no_start_test" },
})
local enabled_configs = vim.lsp._enabled_configs
local no_start_was_enabled = enabled_configs.huyang_no_start_ls
enabled_configs.huyang_no_start_ls = vim.lsp.config.huyang_no_start_ls
started = vim.uv.hrtime()
evidence = edit.diagnostic_evidence({ files = { no_start_path }, wait_ms = 1000 })
elapsed_ms = (vim.uv.hrtime() - started) / 1e6
enabled_configs.huyang_no_start_ls = no_start_was_enabled
check("diagnostic evidence reports an unstartable LSP explicitly",
    #evidence.batches == 1 and evidence.batches[1].kind == "unavailable"
        and evidence.batches[1].reason == "lsp_not_startable", evidence)
check("diagnostic evidence does not wait for an unstartable LSP",
    elapsed_ms < 250, elapsed_ms)

-- A viable enabled configuration may still be attaching. Even then, all files
-- share one deadline: three files cannot turn a 180 ms budget into 540 ms.
vim.filetype.add({ extension = { huyanglatency = "huyang_latency_test" } })
vim.lsp.config("huyang_latency_ls", {
    cmd = { "/bin/true" },
    filetypes = { "huyang_latency_test" },
})
local enabled = vim.lsp._enabled_configs
local was_enabled = enabled.huyang_latency_ls
enabled.huyang_latency_ls = vim.lsp.config.huyang_latency_ls
local waiting = {}
for i = 1, 3 do
    local path = latency_dir .. "/waiting" .. i .. ".huyanglatency"
    vim.fn.writefile({ "plain text" }, path)
    waiting[#waiting + 1] = path
end
started = vim.uv.hrtime()
local diagnostic_co = coroutine.create(function()
    evidence = edit.diagnostic_evidence({ files = waiting, wait_ms = 180 })
end)
local resumed, resume_error = coroutine.resume(diagnostic_co)
local completed = resumed and vim.wait(1000, function()
    return coroutine.status(diagnostic_co) == "dead"
end)
elapsed_ms = (vim.uv.hrtime() - started) / 1e6
enabled.huyang_latency_ls = was_enabled
check("diagnostic evidence coroutine completes", completed, resume_error)
check("diagnostic evidence spends one shared attach deadline",
    elapsed_ms >= 120 and elapsed_ms < 450, elapsed_ms)
local timed_out_once = #evidence.batches == #waiting
for _, batch in ipairs(evidence.batches) do
    timed_out_once = timed_out_once and batch.kind == "unavailable"
        and batch.reason == "lsp_attach_deadline_exceeded"
end
check("the shared deadline still returns explicit unavailable batches",
    timed_out_once, evidence)
-- Navigation uses the same actionable availability check instead of spending
-- the full attach timeout when no language server can possibly attach.
vim.filetype.add({ extension = { huyangnav = "huyang_navigation_no_lsp" } })
local nav_path = latency_dir .. "/navigation.huyangnav"
vim.fn.writefile({ "plain text" }, nav_path)
local nav_buf = vim.fn.bufadd(nav_path)
vim.fn.bufload(nav_buf)
started = vim.uv.hrtime()
local nav_ok, nav_error = pcall(core.get_client, nav_buf, "textDocument/hover", 1000)
elapsed_ms = (vim.uv.hrtime() - started) / 1e6
check("navigation reports actionable missing LSP configuration",
    not nav_ok and tostring(nav_error):find("lsp_not_configured", 1, true)
        and tostring(nav_error):find("language_server_setup", 1, true), nav_error)
check("navigation does not wait when no LSP is configured",
    elapsed_ms < 250, elapsed_ms)

vim.fn.delete(latency_dir, "rf")

-- Go-side canonical edits update disk behind the persistent provider. The
-- internal resync operation must reload clean buffers without restarting it.
do
    local root = vim.fn.tempname() .. "-huyang-resync"
    vim.fn.mkdir(root, "p")
    local path = root .. "/service.lua"
    vim.fn.writefile({ "return 1" }, path)
    local bufnr = core.load_buf(path)
    vim.fn.writefile({ "return 2" }, path)
    local result = require("huyang.lsp").dispatch("huyang_workspace_resync", {})
    local lines = vim.api.nvim_buf_get_lines(bufnr, 0, -1, false)
    check("workspace resync reloads an externally edited clean buffer",
        result.resynced >= 1 and lines[1] == "return 2" and vim.bo[bufnr].modified == false,
        { result = result, lines = lines })
    -- The bridge sends the operation as workspace_resync; both names must
    -- reach the same handler and answer with the same shape.
    vim.fn.writefile({ "return 3" }, path)
    local alias = require("huyang.lsp").dispatch("workspace_resync", {})
    lines = vim.api.nvim_buf_get_lines(bufnr, 0, -1, false)
    check("workspace_resync is registered under the name the bridge sends",
        type(alias) == "table" and alias.resynced >= 1 and lines[1] == "return 3",
        { result = alias, lines = lines })
    vim.api.nvim_buf_delete(bufnr, { force = true })
    vim.fn.delete(root, "rf")
end

-- The code-action cache keeps each client's newest listings only: a token
-- that falls out answers like any stale token, and the newest still apply.
do
    local first = edit.cache_actions({ actions = { "first" } })
    local last
    for _ = 1, edit.MAX_CACHED_ACTIONS do
        last = edit.cache_actions({ actions = { "later" } })
    end
    local kept = 0
    for token = tonumber(first), tonumber(last) do
        if edit.cached_actions(token) then kept = kept + 1 end
    end
    check("code-action cache drops its oldest token past the bound",
        edit.cached_actions(first) == nil and edit.cached_actions(last) ~= nil
            and kept == edit.MAX_CACHED_ACTIONS, { kept = kept })
    local ok, why = pcall(edit.apply_code_action, { token = first, index = 1 })
    check("an evicted code-action token is refused as unknown or expired",
        not ok and tostring(why):find("unknown or expired", 1, true), why)
end

-- The undo ledger forgets whole old steps past its bounds and always keeps
-- the newest step, however large.
do
    local edits = require("huyang.edits")
    local steps, lines = edits.MAX_STEPS, edits.MAX_LINES
    edits.take()
    edits.MAX_STEPS, edits.MAX_LINES = 3, 100
    local function fake(n)
        local out = {}
        for i = 1, n do out[i] = "line " .. i end
        return out
    end
    for i = 1, 5 do
        edits.as_one_step(function()
            edits.record({ file = "a" .. i, first = 1, old_lines = fake(1), new_lines = fake(1), new_count = 1 })
            edits.record({ file = "b" .. i, first = 1, old_lines = fake(1), new_lines = fake(1), new_count = 1 })
        end)
    end
    local kept = edits.take()
    check("undo ledger keeps only the newest whole steps past the step bound",
        edits.forgotten() == 2 and #kept == 6 and kept[1].file == "a3" and kept[6].file == "b5",
        { forgotten = edits.forgotten(), files = vim.tbl_map(function(e) return e.file end, kept) })
    edits.record({ file = "small", first = 1, old_lines = fake(10), new_lines = fake(10), new_count = 10 })
    edits.record({ file = "huge", first = 1, old_lines = fake(200), new_lines = fake(200), new_count = 200 })
    kept = edits.take()
    check("undo ledger keeps the newest step even when it alone passes the line bound",
        #kept == 1 and kept[1].file == "huge", vim.tbl_map(function(e) return e.file end, kept))
    edits.MAX_STEPS, edits.MAX_LINES = steps, lines
end

if failures > 0 then
    io.stdout:write(("unit_edit: %d failed\n"):format(failures))
    vim.cmd("cquit 1")
end
io.stdout:write("unit_edit: OK\n")
io.stdout:flush()
vim.cmd("quit")
