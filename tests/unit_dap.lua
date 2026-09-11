local probe = require("huyang.dap")._test

local adapter = probe.js_debug_adapter("/tmp/dapDebugServer.js")
assert(adapter.type == "server")
assert(adapter.host == "::1")
assert(adapter.port == "${port}")
assert(adapter.executable.args[#adapter.executable.args] == "${port}")

assert(probe.adapter_start_budget(3000) == 3000)
assert(probe.adapter_start_budget(30000) == 15000)
assert(probe.jdtls_attach_timeout_ms == 15000)
assert(probe.jdtls_command_timeout_ms == 10000)

local notice = probe.sanitize_debug_notice("Couldn't connect to 127.0.0.1:${port}: ECONNREFUSED")
assert(not notice:find("${port}", 1, true))
assert(notice:find("<allocated-port>", 1, true))

print("unit_dap: OK")
