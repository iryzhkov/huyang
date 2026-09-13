local M = {}

--- The amount left in the ledger.
function M.total()
    return 7
end

--- Nothing calls this, so a reference-checked deletion may remove it.
function M.unused()
    return 0
end

return M
