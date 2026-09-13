local function leaf(n) return n * 2 end
local function run(n)
  if n < 0 then return nil end
  local task = coroutine.create(function() return leaf(n) end)
  local ok, value = coroutine.resume(task)
  if not ok then error(value) end
  return value
end
local function dynamic(n) return unknown_global(n) end
return { run = run, dynamic = dynamic }
