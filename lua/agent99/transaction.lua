local M = {}

local active

local function err(fmt, ...)
    error(string.format(fmt, ...), 0)
end

local function absolute(path)
    return vim.fn.fnamemodify(path, ":p")
end

local function buffer_bytes(buf)
    local lines = vim.api.nvim_buf_get_lines(buf, 0, -1, false)
    local separator = vim.bo[buf].fileformat == "dos" and "\r\n" or "\n"
    local content = table.concat(lines, separator)
    if vim.bo[buf].endofline then
        content = content .. separator
    end
    if vim.bo[buf].bomb then
        content = "\239\187\191" .. content
    end
    return content
end

local function set_buffer_bytes(buf, content)
    local bomb = content:sub(1, 3) == "\239\187\191"
    if bomb then content = content:sub(4) end
    local dos = content:find("\r\n", 1, true) ~= nil
    local separator = dos and "\r\n" or "\n"
    local endofline = content:sub(-#separator) == separator
    if endofline then content = content:sub(1, -#separator - 1) end
    local lines = {}
    local at = 1
    while true do
        local next_at = content:find(separator, at, true)
        if not next_at then
            lines[#lines + 1] = content:sub(at)
            break
        end
        lines[#lines + 1] = content:sub(at, next_at - 1)
        at = next_at + #separator
    end
    if #lines == 0 then lines = { "" } end
    vim.bo[buf].modifiable = true
    vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
    vim.bo[buf].fileformat = dos and "dos" or "unix"
    vim.bo[buf].endofline = endofline
    vim.bo[buf].fixendofline = endofline
    vim.bo[buf].bomb = bomb
end

local function capture(path, before_exists)
    path = absolute(path)
    local existing = vim.fn.bufnr(path, false)
    local was_loaded = existing > 0 and vim.api.nvim_buf_is_loaded(existing)
    local buf
    if before_exists then
        buf = require("agent99.core").load_buf(path)
    else
        if vim.uv.fs_lstat(path) then err("provider preimage changed: %s now exists", path) end
        if existing > 0 then
            buf = existing
            if not vim.api.nvim_buf_is_loaded(buf) then vim.fn.bufload(buf) end
        else
            buf = vim.api.nvim_create_buf(true, false)
            vim.api.nvim_buf_set_name(buf, path)
        end
    end
    return {
        path = path,
        buf = buf,
        existed = existing > 0,
        loaded = was_loaded,
        listed = vim.bo[buf].buflisted,
        modified = vim.bo[buf].modified,
        modifiable = vim.bo[buf].modifiable,
        readonly = vim.bo[buf].readonly,
        content = buffer_bytes(buf),
        deleted = vim.b[buf].huyang_deleted,
    }
end

local function restore(preimages)
    local failures = {}
    for i = #preimages, 1, -1 do
        local pre = preimages[i]
        local ok, reason = pcall(function()
            if not vim.api.nvim_buf_is_valid(pre.buf) then
                err("buffer disappeared: %s", pre.path)
            end
            set_buffer_bytes(pre.buf, pre.content)
            vim.b[pre.buf].huyang_deleted = pre.deleted
            vim.bo[pre.buf].readonly = pre.readonly
            vim.bo[pre.buf].modifiable = pre.modifiable
            vim.bo[pre.buf].buflisted = pre.listed
            vim.bo[pre.buf].modified = pre.modified
            if not pre.loaded then
                vim.api.nvim_buf_delete(pre.buf, { force = true })
            end
        end)
        if not ok then failures[#failures + 1] = tostring(reason) end
    end
    return failures
end

function M.prepare(args)
    if active then
        if active.plan_id == args.plan_id and active.plan_revision == args.plan_revision then
            return active.result
        end
        err("workspace_busy: transaction %s holds the provider lease", active.plan_id)
    end
    local preimages = {}
    local ok, result = pcall(function()
        for _, file in ipairs(args.files or {}) do
            local pre = capture(file.path, file.before_exists == true)
            local expected = vim.base64.decode(file.before_b64 or "")
            if pre.content ~= expected then
                err("provider_preimage_changed: %s", pre.path)
            end
            preimages[#preimages + 1] = pre
        end
        for index, file in ipairs(args.files or {}) do
            local pre = preimages[index]
            local after = vim.base64.decode(file.after_b64 or "")
            set_buffer_bytes(pre.buf, after)
            vim.b[pre.buf].huyang_deleted = file.after_exists ~= true
            if tonumber(args.exit_provider_after) == index then
                vim.cmd("qa!")
            end
            vim.bo[pre.buf].modified = true
            if tonumber(args.fail_after) == index then
                err("injected provider apply failure after %d", index)
            end
        end
        return {
            ok = true,
            transaction_id = args.plan_id,
            files = vim.tbl_map(function(file)
                return { path = absolute(file.path), exists = file.after_exists == true }
            end, args.files or {}),
            diagnostics = "suppressed",
            canonical_changed = false,
        }
    end)
    if not ok then
        local failures = restore(preimages)
        if #failures > 0 then
            err("%s; restore failures: %s", tostring(result), table.concat(failures, "; "))
        end
        error(result, 0)
    end
    active = {
        plan_id = args.plan_id,
        plan_revision = args.plan_revision,
        preimages = preimages,
        result = result,
    }
    return result
end

function M.rollback(args)
    if not active then return { ok = true, restored = 0 } end
    if active.plan_id ~= args.plan_id then
        err("workspace_busy: transaction %s holds the provider lease", active.plan_id)
    end
    local count = #active.preimages
    local failures = restore(active.preimages)
    if #failures > 0 then err("restore failures: %s", table.concat(failures, "; ")) end
    active = nil
    return { ok = true, restored = count }
end

function M.commit(args)
    if not active then return { ok = true, resynced = 0 } end
    if active.plan_id ~= args.plan_id then
        err("workspace_busy: transaction %s holds the provider lease", active.plan_id)
    end
    local count = 0
    for _, pre in ipairs(active.preimages) do
        if vim.api.nvim_buf_is_valid(pre.buf) then
            if vim.b[pre.buf].huyang_deleted then
                vim.api.nvim_buf_delete(pre.buf, { force = true })
            else
                vim.b[pre.buf].huyang_deleted = nil
                vim.bo[pre.buf].modified = false
            end
            count = count + 1
        end
    end
    active = nil
    return { ok = true, resynced = count }
end

function M.status(args)
    if not active then return { active = false } end
    if args and args.plan_id and args.plan_id ~= active.plan_id then
        err("workspace_busy: transaction %s holds the provider lease", active.plan_id)
    end
    return { active = true, plan_id = active.plan_id, plan_revision = active.plan_revision }
end

return M
