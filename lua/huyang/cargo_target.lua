-- Where cargo builds for a Cargo project that a headless Neovim opens.
--
-- rust-analyzer builds proc macros and build scripts, about 1 GB for a
-- mid-sized workspace. Pointing CARGO_TARGET_DIR away from the project's
-- own target/ keeps an agent's `cargo build` from waiting on rust-analyzer's
-- lock, but the directory used to be a fresh one inside Neovim's tempdir,
-- /tmp/nvim.<user>/XXXXXX/, removed from VimLeavePre. A provider that is
-- killed (systemd-oomd kills the whole unit, a restart, a stop timeout)
-- never runs VimLeavePre, and on a RAM-backed /tmp the leaked builds filled
-- the per-user quota and counted against the service's memory.
--
-- So the build lives on disk, one directory per project root shared by
-- every session on that root (incremental, bounded by the number of
-- projects rather than sessions), directories unused for
-- HUYANG_CARGO_TARGET_MAX_AGE_DAYS are pruned, and the per-session
-- directories earlier versions leaked into /tmp are swept once per process.
-- None of it depends on Neovim exiting cleanly.

local M = {}

M.DEFAULT_MAX_AGE_DAYS = 30

-- The suffix earlier versions gave the per-session directory in the tempdir.
local LEGACY_SUFFIX = "-huyang-cargo-target"
-- Touched each time a session uses a directory: a directory's own mtime
-- does not change when cargo writes below it.
local STAMP = ".huyang-last-used"

-- The directory CARGO_TARGET_DIR was set to by this process, so a second
-- call can tell its own value from one the environment brought.
local ours

-- Set once the sweep and prune have run in this process; a test sets it
-- to keep prepare away from the real tempdir and cache.
M._swept = false

local function cache_home()
    local xdg = vim.env.XDG_CACHE_HOME
    if type(xdg) == "string" and xdg:sub(1, 1) == "/" then return xdg end
    return (vim.env.HOME or vim.uv.os_homedir()) .. "/.cache"
end

function M.base_dir()
    return cache_home() .. "/huyang/cargo-target"
end

-- A stable directory per project root: the root's base name for whoever
-- lists the cache, and a hash of the whole path so two checkouts with the
-- same name do not share a build.
function M.dir_for(root, base)
    root = vim.fs.normalize(root)
    local name = vim.fs.basename(root):gsub("[^%w._-]", "_")
    return (base or M.base_dir()) .. "/" .. name .. "-" .. vim.fn.sha256(root):sub(1, 12)
end

function M.max_age_days()
    local days = tonumber(vim.env.HUYANG_CARGO_TARGET_MAX_AGE_DAYS)
    if days and days > 0 then return days end
    return M.DEFAULT_MAX_AGE_DAYS
end

-- Removal runs in a detached rm so a gigabyte of small files does not hold
-- up the tool call that triggered it, and finishes even if Neovim exits.
local function remove_async(path)
    local ok = pcall(vim.system, { "rm", "-rf", "--", path }, { detach = true })
    if not ok then pcall(vim.fn.delete, path, "rf") end
end

local function subdirs(dir)
    local out = {}
    local handle = vim.uv.fs_scandir(dir)
    while handle do
        local name, kind = vim.uv.fs_scandir_next(handle)
        if not name then break end
        if kind == "directory" then out[#out + 1] = name end
    end
    return out
end

local function last_used(dir)
    local stat = vim.uv.fs_stat(dir .. "/" .. STAMP) or vim.uv.fs_stat(dir)
    return stat and stat.mtime.sec or nil
end

-- Remove the per-root directories under base not used for max_age_days.
-- keep is never removed. Returns the paths removed.
function M.prune(opts)
    opts = opts or {}
    local base = opts.base or M.base_dir()
    local cutoff = (opts.now or os.time()) - (opts.max_age_days or M.max_age_days()) * 86400
    local remove = opts.remove or remove_async
    local removed = {}
    for _, name in ipairs(subdirs(base)) do
        local dir = base .. "/" .. name
        local used = last_used(dir)
        if dir ~= opts.keep and used and used < cutoff then
            remove(dir)
            removed[#removed + 1] = dir
        end
    end
    return removed
end

-- The Neovim tempdirs directly under base that some process still has a
-- file, directory or working directory open in, by name; nil when /proc
-- cannot be read, since then no tempdir can be proven abandoned.
local function live_tempdirs(base)
    if not vim.uv.fs_scandir("/proc/self/fd") then return nil end
    local prefix = base .. "/"
    local live = {}
    local function note(link)
        if link and link:sub(1, #prefix) == prefix then
            live[link:sub(#prefix + 1):match("^[^/]+")] = true
        end
    end
    local procs = vim.uv.fs_scandir("/proc")
    while procs do
        local pid = vim.uv.fs_scandir_next(procs)
        if not pid then break end
        if pid:match("^%d+$") then
            note(vim.uv.fs_readlink("/proc/" .. pid .. "/cwd"))
            local fds = vim.uv.fs_scandir("/proc/" .. pid .. "/fd")
            while fds do
                local fd = vim.uv.fs_scandir_next(fds)
                if not fd then break end
                note(vim.uv.fs_readlink("/proc/" .. pid .. "/fd/" .. fd))
            end
        end
    end
    return live
end

-- Remove the per-session cargo directories earlier versions left in other
-- Neovims' tempdirs, /tmp/nvim.<user>/<dir>/<n>-huyang-cargo-target, when
-- no live process holds that tempdir. Our own tempdir is never touched.
-- opts.own and opts.base override the tempdir layout and opts.is_live the
-- /proc check, for tests. Returns the paths removed.
function M.sweep(opts)
    opts = opts or {}
    local own = opts.own or vim.fn.fnamemodify(vim.fn.tempname(), ":h")
    local base = opts.base or vim.fn.fnamemodify(own, ":h")
    local remove = opts.remove or remove_async
    -- Only a directory laid out as Neovim's per-user temp root is swept.
    if not vim.fs.basename(base):match("^nvim%.") then return {} end
    local candidates = {}
    for _, name in ipairs(subdirs(base)) do
        local tempdir = base .. "/" .. name
        if tempdir ~= own then
            for _, entry in ipairs(subdirs(tempdir)) do
                if entry:sub(-#LEGACY_SUFFIX) == LEGACY_SUFFIX then
                    candidates[#candidates + 1] = { name = name, path = tempdir .. "/" .. entry }
                end
            end
        end
    end
    if #candidates == 0 then return {} end
    local is_live = opts.is_live
    if not is_live then
        local live = live_tempdirs(base)
        if not live then return {} end
        is_live = function(name) return live[name] == true end
    end
    local removed = {}
    for _, candidate in ipairs(candidates) do
        if not is_live(candidate.name) then
            remove(candidate.path)
            removed[#removed + 1] = candidate.path
        end
    end
    return removed
end

-- Point CARGO_TARGET_DIR at root's shared build directory when root is a
-- Cargo project and the environment has not already chosen one, and, once
-- per process, clear what earlier sessions left behind. Returns the
-- directory it chose, or nil.
function M.prepare(root)
    local dir
    local env = vim.env.CARGO_TARGET_DIR
    if (env == nil or env == "" or env == ours) and vim.uv.fs_stat(root .. "/Cargo.toml") then
        dir = M.dir_for(root)
        vim.fn.mkdir(dir, "p")
        pcall(vim.fn.writefile, {}, dir .. "/" .. STAMP)
        vim.env.CARGO_TARGET_DIR = dir
        ours = dir
    end
    if not M._swept then
        M._swept = true
        pcall(M.sweep)
        pcall(M.prune, { keep = dir })
    end
    return dir
end

return M
