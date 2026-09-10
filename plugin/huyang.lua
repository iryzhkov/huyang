-- Global RPC entry points for Huyang's socket compatibility provider.
-- The modern embedded provider calls the same kernel module directly.

if vim.g.loaded_huyang_kernel then
    return
end
vim.g.loaded_huyang_kernel = true

_G.HuyangRpcStart = function(b64)
    return require("huyang.rpc").start(b64)
end

_G.HuyangRpcPoll = function(id)
    return require("huyang.rpc").poll(id)
end
