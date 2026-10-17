-- Python environment policy shared by immediate and late diagnostic evidence.
-- Go's pipeline selector supplies discovery; Lua never runs a second detector.
local M = {}
local environment = { reason = "project_interpreter_not_found" }
local automatic = {}

local function configured_python(name)
    local config = vim.lsp.config[name] or {}
    return ((config.settings or {}).python or {}).pythonPath
end

function M.configure(resolved)
    environment = vim.deepcopy(resolved or { reason = "project_interpreter_not_found" })
    local interpreter
    for _, name in ipairs({ "pyright", "basedpyright" }) do
        local current = configured_python(name)
        -- A user setting wins. Only replace a value we previously supplied.
        if current == nil or current == "" or current == automatic[name] then
            local python = environment.interpreter
            if python and python ~= "" then
                local ok = pcall(function()
                    vim.lsp.config(name, { settings = { python = { pythonPath = python } } })
                end)
                if ok then automatic[name] = python else environment.reason = "interpreter_configuration_failed" end
            end
        end
        interpreter = interpreter or configured_python(name)
    end
    return interpreter
end

function M.interpreter(name)
    return configured_python(name or "pyright")
end

function M.reason(name)
    local python = configured_python(name or "pyright")
    if (type(python) == "string" and python ~= "") or environment.configured then return nil end
    return environment.reason
end

local function known_interpreter(batch)
    if environment.configured then return true end
    local client_id = tonumber((batch.provider_id or ""):match("#(%d+)$"))
    local client = client_id and vim.lsp.get_client_by_id(client_id)
    -- Read the attached client's actual settings, not a later global update
    -- that the running server has never received.
    if client then
        local settings = ((client.config or {}).settings or {}).python
        if type(settings) ~= "table" then return false end
        ---@cast settings table<string, any>
        local python = settings.pythonPath
        return type(python) == "string" and python ~= ""
    end
    return false
end

-- Keep the canonical batch shape. A stable warning survives the edit severity
-- filter and deduplicates by finding identity; it is never an error or a clean
-- verdict. Only Pyright's missing-import code is collapsed.
function M.normalize_batch(batch)
    if (batch.producer ~= "pyright" and batch.producer ~= "basedpyright")
        or known_interpreter(batch) then return batch end
    local findings, missing = {}, false
    for _, finding in ipairs(batch.findings or {}) do
        if finding.code == "reportMissingImports" then
            missing = true
        else
            findings[#findings + 1] = finding
        end
    end
    if not missing then return batch end
    findings[#findings + 1] = {
        range = { start_line = 1, start_character = 1, end_line = 1, end_character = 1 },
        severity = 2, code = "python_environment_unresolved", source = "huyang",
        message = "Python environment not resolved: unresolved imports are grouped here. "
            .. "Configure python.pythonPath or the project's environment, then restart the language server. "
            .. "Import diagnostics remain provisional; other findings are preserved.",
    }
    batch.findings = findings
    batch.complete = false
    batch.reason = "python_environment_unresolved"
    return batch
end

return M
