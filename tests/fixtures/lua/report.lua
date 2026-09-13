local ledger = require("ledger")

local M = {}

--- Reads the total, so an inline and a rename have something to move.
function M.report()
    return ledger.total() + 1
end

return M
