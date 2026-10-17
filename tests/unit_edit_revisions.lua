-- Provider-wire regression for receipt document revisions; no language server.
-- Run: nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_edit_revisions.lua
local edit = require("huyang.edit")
local core = require("huyang.core")
local failures = 0
local function check(name, ok, detail)
    if ok then
        io.stdout:write("ok   " .. name .. "\n")
    else
        failures = failures + 1
        io.stdout:write("FAIL " .. name .. "\n" .. vim.inspect(detail) .. "\n")
    end
end

local root = vim.fn.tempname() .. "-document-revisions"
vim.fn.mkdir(root, "p")
local paths, buffers = {}, {}
for _, name in ipairs({ "a.py", "b.py", "unknown.py" }) do
    local path = root .. "/" .. name
    vim.fn.writefile({ "value = 1" }, path)
    local buf = core.load_buf(path)
    vim.lsp.util.buf_versions[buf] = 3
    paths[#paths + 1], buffers[#buffers + 1] = path, buf
end
local saved_clients, saved_client, saved_attach =
    vim.lsp.get_clients, vim.lsp.get_client_by_id, vim.lsp.buf_attach_client
local fake = {
    id = 19001, name = "revision_test", config = { filetypes = { "python" } },
    handlers = { ["textDocument/publishDiagnostics"] = function() end },
    supports_method = function() return false end,
}
vim.lsp.get_clients = function() return { fake } end
vim.lsp.get_client_by_id = function(id)
    if id == fake.id then return fake end
    return saved_client(id)
end
vim.lsp.buf_attach_client = function() return true end

local revisions = { [paths[1]] = "docrev_a_final", [paths[2]] = "docrev_b_final" }
local immediate = edit.diagnostic_evidence({
    files = paths, document_revisions = revisions, revision = "wsrev_2",
    transaction_id = "edit_multi", wait_ms = 0,
})
check("each synchronous batch uses its own receipt revision", #immediate.batches == 3, immediate)
for _, batch in ipairs(immediate.batches) do
    check("immediate revision for " .. batch.document,
        batch.document_revision == revisions[batch.document], batch)
end
-- Mutating the request map later cannot rebind the pending postimages.
revisions[paths[1]] = "docrev_a_later"
local finding = { severity = 1, message = "late", range = {
    start_line = 1, start_character = 1, end_line = 1, end_character = 2,
} }
for _, path in ipairs(paths) do
    check("matching late push captured for " .. path,
        edit._capture_late(fake.name, path, 3, { finding }, fake.id))
end
local late = edit.late_evidence().batches
local expected = { [paths[1]] = "docrev_a_final", [paths[2]] = "docrev_b_final" }
check("multi-file late evidence retains all documents", #late == 3, late)
for _, batch in ipairs(late) do
    check("late receipt attribution for " .. batch.document,
        batch.document_revision == expected[batch.document]
            and batch.transaction_id == "edit_multi"
            and batch.expected_version == 3 and batch.document_version == 3, batch)
end

-- A scalar revision remains compatible for older provider callers.
local legacy = edit.diagnostic_evidence({
    files = { paths[1] }, revision = "docrev_legacy", transaction_id = "edit_legacy", wait_ms = 0,
})
check("legacy scalar document revision remains supported",
    legacy.batches[1].document_revision == "docrev_legacy", legacy)
edit._capture_late(fake.name, paths[1], 3, { finding }, fake.id)
local legacy_late = edit.late_evidence().batches
check("legacy late attribution remains supported",
    #legacy_late == 1 and legacy_late[1].document_revision == "docrev_legacy"
        and legacy_late[1].transaction_id == "edit_legacy", legacy_late)

-- A timed-out unversioned pull retains the same receipt revision while
-- remaining provisional. This uses only the diagnostic provider wire API.
fake.supports_method = function(_, method) return method == "textDocument/diagnostic" end
fake.attached_buffers = { [buffers[1]] = true }
local request_id, callbacks = 0, {}
fake.request = function(_, _, _, callback)
    request_id = request_id + 1
    local id = request_id
    callbacks[id] = callback
    vim.defer_fn(function()
        if not callbacks[id] then return end
        callbacks[id] = nil
        callback(nil, { kind = "full", items = { {
            range = { start = { line = 0, character = 0 }, ["end"] = { line = 0, character = 1 } },
            severity = 1, message = "late pull",
        } } })
    end, 40)
    return true, id
end
fake.cancel_request = function(_, id) callbacks[id] = nil end
local pull_result
local pull = coroutine.create(function()
    pull_result = edit.diagnostic_evidence({
        files = { paths[1] }, revision = "wsrev_pull", transaction_id = "edit_pull",
        document_revisions = { [paths[1]] = "docrev_pull_postimage" }, wait_ms = 5,
    })
end)
local ok, detail = coroutine.resume(pull)
check("pull starts", ok, detail)
vim.wait(500, function() return coroutine.status(pull) == "dead" end, 1)
check("immediate pull uses the receipt revision and remains provisional",
    pull_result and #pull_result.batches == 1
        and pull_result.batches[1].document_revision == "docrev_pull_postimage"
        and pull_result.batches[1].complete == false, pull_result)
vim.wait(70)
local late_pull = edit.late_evidence().batches
check("late pull uses the receipt revision without claiming producer version proof",
    #late_pull == 1 and late_pull[1].kind == "lsp_pull"
        and late_pull[1].document_revision == "docrev_pull_postimage"
        and late_pull[1].transaction_id == "edit_pull"
        and late_pull[1].document_version == nil and late_pull[1].complete == false, late_pull)

vim.lsp.get_clients, vim.lsp.get_client_by_id, vim.lsp.buf_attach_client =
    saved_clients, saved_client, saved_attach
for _, buf in ipairs(buffers) do vim.api.nvim_buf_delete(buf, { force = true }) end
vim.fn.delete(root, "rf")
if failures > 0 then vim.cmd("cquit 1") end
io.stdout:write("unit_edit_revisions: OK\n")
vim.cmd("quit")
