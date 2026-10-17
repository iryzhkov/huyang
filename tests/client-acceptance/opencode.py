"""Drive the real OpenCode 2.0.30 ACP process; no direct plugin invocation."""
import asyncio
import json
from pathlib import Path

from common import canonical, digest, record, require, spawn, stop, write_json
from workflow import Workflow

WITNESS = """import * as sdk from "@opencode/plugin";
import { appendFileSync } from "node:fs";
const log = (entry) => appendFileSync(process.env.ACCEPTANCE_WITNESS, JSON.stringify(entry) + "\\n");
export default {
  id: "acceptance.read-only-witness",
  async setup(ctx) {
    log({kind:"setup",version:ctx.app.version,sdkLoaded:typeof sdk === "object"});
    const r = await ctx.session.hook("context", (event) => {
      log({kind:"context",sessionID:event.sessionID,providerID:event.model?.providerID,
        modelID:event.model?.id,tools:Object.keys(event.tools).sort()});
    });
    return () => r.dispose();
  }
};
"""

def configure(case, plugin, recorder, port, plugin_sha256):
    configdir = case / "oc-config"
    plugins = configdir / "plugins"
    plugins.mkdir(parents=True)
    # Copy the exact candidate module; discovery (no config plugins array) is part of the gate.
    plugin_bytes = Path(plugin).read_bytes()
    require(digest(plugin_bytes) == plugin_sha256, "plugin changed after its runner preflight pin")
    (plugins / "huyang-profiles.js").write_bytes(plugin_bytes)
    (plugins / "zz-acceptance-witness.js").write_text(WITNESS)
    (configdir / "package.json").write_text('{"private":true,"type":"module","dependencies":{"@opencode/plugin":"2.0.30"}}\n')
    providers = {}
    for provider in ("acceptance", "ollama"):
        providers[provider] = {
            "package": "@opencode/ai/providers/openai-compatible",
            "settings": {"baseURL": "http://127.0.0.1:" + str(port) + "/" + provider + "/v1", "apiKey": "sk-huyang-acceptance-not-a-secret"},
            "models": {model: {"name": provider + " " + model,
                               "limit": {"context": 128000, "output": 4096},
                               "capabilities": {"tools": True, "input": ["text"], "output": ["text"]}}
                       for model in ("mock", "mock2", "ollama")}}
    # Pinned native discovery respects these base URLs; empty mock catalogs
    # leave configured inference models untouched, without probing ambient ports.
    for provider in ("lmstudio", "vllm"):
        providers[provider] = {"settings": {
            "baseURL": "http://127.0.0.1:" + str(port) + "/" + provider + "/v1"}}
    write_json(configdir / "opencode.json", {
        "update": "disable", "share": "disabled", "model": "acceptance/mock",
        "providers": providers,
        "mcp": {"servers": {"huyang": {
            "type": "local", "command": [str(recorder), "mcp", "--socket", str(case / "h.sock"),
                                         "--reply-style", "plain"],
            "codemode": False, "timeout": {"startup": 30000, "catalog": 30000, "execution": 30000}}}},
        "permissions": [{"action": "huyang_*", "resource": "*", "effect": "allow"}],
        "policies": [{"action": "provider.use", "resource": "*", "effect": "deny"},
                     {"action": "provider.use", "resource": "acceptance", "effect": "allow"},
                     {"action": "provider.use", "resource": "ollama", "effect": "allow"},
                     {"action": "tool.use", "resource": "*", "effect": "deny"},
                     {"action": "tool.use", "resource": "huyang_*:*", "effect": "allow"}]})

class ACP:
    def __init__(self, case):
        self.case = case
        self.counter = 0
        self.futures = {}
        self.notifications = []
        self.failures = []
        self.lock = asyncio.Lock()
        self.selection_expectation = None
        self.selections = []

    async def start(self, binary, env):
        self.err = (self.case / "client.stderr").open("wb")
        self.process = await spawn([binary, "--print-logs", "acp"], env, self.case / "fixture",
                                   asyncio.subprocess.PIPE, self.err, asyncio.subprocess.PIPE)
        assert self.process.stdout is not None and self.process.stdin is not None
        self.task = asyncio.create_task(self.read())
        return self

    async def read(self):
        assert self.process.stdout is not None
        try:
            while line := await self.process.stdout.readline():
                record(self.case / "client.jsonl", direction="from-client", raw=line.decode())
                message = json.loads(line)
                if "method" in message and "id" in message:
                    self.failures.append("unexpected ACP request " + message["method"])
                    if message["method"] == "session/request_permission":
                        reply = {"jsonrpc": "2.0", "id": message["id"],
                                 "result": {"outcome": {"outcome": "cancelled"}}}
                    else:
                        reply = {"jsonrpc": "2.0", "id": message["id"],
                                 "error": {"code": -32601, "message": "Acceptance denies unexpected capability"}}
                    await self.send(reply)
                elif "id" in message:
                    require(message["id"] in self.futures, "unknown ACP response id")
                    self.futures.pop(message["id"]).set_result(message)
                else:
                    self.notifications.append(message)
                    self.check_selection_notice(message)
        except Exception as error:
            self.failures.append(repr(error))
        finally:
            for future in self.futures.values():
                if not future.done():
                    future.set_exception(AssertionError("ACP EOF: " + str(self.failures)))

    async def send(self, message):
        async with self.lock:
            raw = (canonical(message) + "\n").encode()
            record(self.case / "client.jsonl", direction="to-client", message=message)
            assert self.process.stdin is not None
            self.process.stdin.write(raw)
            await self.process.stdin.drain()

    async def rpc(self, method, params, allow_error=False):
        self.counter += 1
        ident = self.counter
        future = asyncio.get_running_loop().create_future()
        self.futures[ident] = future
        await self.send({"jsonrpc": "2.0", "id": ident, "method": method, "params": params})
        response = await asyncio.wait_for(future, 120)
        if not allow_error:
            require("error" not in response, "ACP error: " + canonical(response))
            return response["result"]
        return response

    @staticmethod
    def selected_model(options):
        require(isinstance(options, list), "ACP selection lacks configOptions array")
        models = [option for option in options if option.get("id") == "model"]
        require(len(models) == 1, "ACP configOptions must identify exactly one model selector")
        return models[0].get("currentValue")

    def check_selection_notice(self, message):
        if message.get("method") != "session/update":
            return
        params = message.get("params", {})
        update = params.get("update", {})
        if update.get("sessionUpdate") != "config_option_update" or self.selection_expectation is None:
            return
        expected = self.selection_expectation
        require(params.get("sessionId") == expected["sessionId"],
                "ACP selection notification leaked into another session")
        require(self.selected_model(update.get("configOptions")) == expected["model"],
                "ACP notification did not select exact provider/model")

    async def select(self, session, model):
        start = len(self.notifications)
        # The JSON-RPC request ID binds this result to this selected session.
        self.selection_expectation = {"sessionId": session, "model": model}
        result = await self.rpc("session/set_config_option",
                                {"sessionId": session, "configId": "model", "value": model})
        require(result.get("sessionId", session) == session, "ACP selection result names another session")
        notices = [n for n in self.notifications[start:] if n.get("method") == "session/update"
                   and n.get("params", {}).get("update", {}).get("sessionUpdate") == "config_option_update"]
        for notice in notices:
            self.check_selection_notice(notice)
        if "configOptions" in result:
            require(self.selected_model(result["configOptions"]) == model,
                    "ACP response did not select exact provider/model")
            proof = "response.configOptions"
        else:
            require(notices, "ACP selection has neither response configOptions nor a matching notice")
            proof = "matching notification before response"
        selection = {"sessionId": session, "model": model, "result": result, "proof": proof,
                     "rpc_response_id": self.counter, "notifications": self.notifications[start:]}
        self.selections.append(selection)
        record(self.case / "selections.jsonl", **selection)
        # No notification wait: the response itself is authoritative selection proof.
        return result

    async def close(self):
        await stop(self.process)
        await self.task
        self.err.close()

async def exercise(binary, env, owner, make_fixture, native_binary, native_sha256):
    from ipc_audit import capture_acp_listener
    acp = await ACP(owner.case).start(binary, env)
    sessions = []
    witness = []
    initial_capture = None
    final_capture = None
    try:
        await acp.rpc("initialize", {"protocolVersion": 1, "clientCapabilities": {},
                                    "clientInfo": {"name": "huyang-acceptance", "version": "1"}})
        try:
            initial_capture = capture_acp_listener(acp.process.pid, native_binary, native_sha256, owner.port)
        except Exception as error:
            initial_capture = {"violations": [repr(error)]}
        write_json(owner.case / "acp-ipc-initial.json", initial_capture)
        require(not initial_capture["violations"], "ACP listener ownership capture failed: "
                + canonical(initial_capture["violations"]))
        for _ in range(2):
            created = await acp.rpc("session/new", {"cwd": str(owner.case / "fixture"), "mcpServers": []})
            require("sessionId" in created, "ACP session/new lacks sessionId")
            options = canonical(created.get("configOptions", []))
            require("acceptance/mock" in options and "ollama/mock" in options,
                    "ACP model configOptions do not advertise both exact providers")
            sessions.append(created["sessionId"])
        a, b = sessions
        require(a != b, "ACP returned one session for both creations")
        # Native registration/catalog loading is asynchronous; start inference only
        # after the real loader has returned both tools/list replies. No MCP calls here.
        await asyncio.wait_for(owner.catalogs_ready({"full", "lean"}), 30)

        async def prompt(session, nonce, profile, provider, probe=False):
            root = make_fixture(nonce)
            workflow = Workflow(owner, nonce, root, profile, session, provider, probe)
            owner.workflows[nonce] = workflow
            result = await acp.rpc("session/prompt", {
                "sessionId": session, "prompt": [{"type": "text", "text": workflow.prompt()}]})
            require(result.get("stopReason") == "end_turn", "ACP turn did not end normally: " + canonical(result))
            require(workflow.done, "ACP turn ended without scripted workflow")
            return workflow

        await acp.select(a, "ollama/mock")
        await asyncio.gather(prompt(a, "oc-a-lean", "lean", "ollama"),
                             prompt(b, "oc-b-full", "full", "acceptance"))
        await acp.select(a, "acceptance/mock")
        await acp.select(b, "ollama/mock")
        await asyncio.gather(prompt(a, "oc-a-full", "full", "acceptance"),
                             prompt(b, "oc-b-lean", "lean", "ollama"))
        # A model named "ollama" under the acceptance provider must remain full.
        await acp.select(a, "acceptance/ollama")
        await acp.select(b, "ollama/mock2")
        await asyncio.gather(prompt(a, "oc-a-provider-exact", "full", "acceptance", True),
                             prompt(b, "oc-b-model-only", "lean", "ollama", True))
        await acp.select(b, "acceptance/mock2")
        await prompt(b, "oc-b-restored-full", "full", "acceptance", True)
        before = owner.count
        invalid = await acp.rpc("session/set_config_option",
                                {"sessionId": a, "configId": "model", "value": "unavailable/mock"}, True)
        require("error" in invalid and owner.count == before, "invalid model silently fell back or inferred")
        require(not acp.failures, str(acp.failures))
        witness_path = owner.case / "plugin-witness.jsonl"
        require(witness_path.is_file(), "actual V2 discovery/setup witness missing")
        witness = [json.loads(line) for line in witness_path.read_text().splitlines()]
        require(any(e.get("kind") == "setup" and e.get("version") == "2.0.30" and e.get("sdkLoaded")
                    for e in witness), "pinned V2 plugin SDK not loaded by native loader")
        contexts = [e for e in witness if e.get("kind") == "context"]
        for session in sessions:
            require({e.get("providerID") for e in contexts if e.get("sessionID") == session}
                    >= {"acceptance", "ollama"}, "actual context witness missing both providers for a session")
        # Both selected catalogs came from distinct real MCP registrations.
        require(set(owner.catalogs) == {"full", "lean"}, "plugin did not discover both actual profiles")
    finally:
        try:
            final_capture = capture_acp_listener(acp.process.pid, native_binary, native_sha256, owner.port)
        except Exception as error:
            final_capture = {"violations": [repr(error)]}
        try:
            await acp.close()
        finally:
            write_json(owner.case / "acp-ipc-final.json", final_capture)
            owner.acp_ipc_captures = (initial_capture, final_capture)
            write_json(owner.case / "acp-report.json", {"sessions": sessions, "notifications": acp.notifications,
                                                      "selections": acp.selections,
                                                      "failures": acp.failures, "witness": witness})
            require(not acp.failures, str(acp.failures))
