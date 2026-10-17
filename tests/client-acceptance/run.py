#!/usr/bin/env python3
"""Run only inside the parent's disposable job after explicit bootstrap and candidate build."""
import argparse
import asyncio
import ctypes
import json
import os
from pathlib import Path
import re
import shutil
import sys

from bootstrap import namespace_required
from common import HERE, KEY, canonical, digest, environment, file_hash, record, require, spawn, stop, write_json
from model_server import Controller
import opencode
from workflow import Workflow

async def capture(command, env, cwd, path, timeout=20):
    with Path(path).open("wb") as output:
        process = await spawn(command, env, cwd, output, asyncio.subprocess.STDOUT)
        try:
            await asyncio.wait_for(process.wait(), timeout)
            require(process.returncode == 0, "command failed: see " + str(path))
        finally:
            await stop(process)
    return Path(path).read_text(errors="replace")

async def socket_ready(path, process):
    """Linux inotify: bounded event wait, no filesystem polling or arbitrary sleeps."""
    libc = ctypes.CDLL(None, use_errno=True)
    fd = libc.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
    require(fd >= 0, "inotify unavailable")
    watch = libc.inotify_add_watch(fd, os.fsencode(path.parent), 0x100 | 0x80)
    require(watch >= 0, "cannot watch private socket directory")
    loop = asyncio.get_running_loop()
    ready = loop.create_future()
    def changed():
        try:
            os.read(fd, 65536)
        except BlockingIOError:
            pass
        if path.is_socket() and not ready.done():
            ready.set_result(True)
    loop.add_reader(fd, changed)
    waiter = asyncio.create_task(process.wait())
    try:
        changed()
        done, _ = await asyncio.wait([ready, waiter], timeout=30, return_when=asyncio.FIRST_COMPLETED)
        require(ready in done, "private service exited or socket readiness deadline exceeded")
    finally:
        waiter.cancel()
        loop.remove_reader(fd)
        os.close(fd)

def fixture(case, nonce):
    root = case / "fixture" / nonce
    root.mkdir(parents=True)
    (root / "go.mod").write_text("module acceptance.invalid/" + nonce + "\n\ngo 1.25.0\n")
    (root / "main.go").write_text("package fixture\n\n// alpha one\n// alpha two\nvar Marker = 1\n")
    return root

def recorder(case, candidate):
    bindir = case / "bin"
    bindir.mkdir()
    path = bindir / "huyang"
    source = ("#!" + sys.executable + "\nimport os, sys\n"
              + "args = sys.argv[1:]\n"
              + "assert args and args[0] == 'mcp', 'acceptance recorder accepts only mcp'\n"
              + "profile = 'full'\n"
              + "for i, arg in enumerate(args):\n"
              + "    if arg == '--profile': profile = args[i + 1]\n"
              + "    elif arg.startswith('--profile='): profile = arg.split('=', 1)[1]\n"
              + "os.execv(sys.executable, [sys.executable, " + repr(str(HERE / "wire.py"))
              + ", '--log', " + repr(str(case / "mcp-wire.jsonl"))
              + ", '--profile', profile, 'mcp', '--', " + repr(str(candidate)) + ", *args])\n")
    path.write_text(source)
    path.chmod(0o755)
    return path

def headless_init(case, gopls):
    cmd = [sys.executable, str(HERE / "wire.py"), "--log", str(case / "lsp-wire.jsonl"),
           "lsp", "--", str(gopls)]
    # JSON strings are valid Lua strings for these absolute Linux paths (reject exotic escapes).
    require(all("\\" not in part and "\n" not in part for part in cmd), "unsupported escaped job path")
    quoted = ", ".join(json.dumps(part) for part in cmd)
    text = ("vim.opt.swapfile = false\nvim.opt.shadafile = 'NONE'\n"
            + "local caps = vim.lsp.protocol.make_client_capabilities()\n"
            + "caps.textDocument.diagnostic = nil\n"
            + "caps.textDocument.publishDiagnostics.versionSupport = true\n"
            + "vim.lsp.config('gopls', {\n  cmd = {" + quoted + "},\n"
            + "  filetypes = {'go', 'gomod'}, root_markers = {'go.mod'}, capabilities = caps,\n"
            + "  settings = {gopls = {diagnosticsDelay = '0s'}},\n"
            + "})\nvim.lsp.enable('gopls')\n")
    path = case / "init.lua"
    path.write_text(text)
    return path

def traced(args, case, label, command):
    return [str(args.strace), "-ff", "-qq", "-s", "4096", "-e",
            "trace=connect,sendto,sendmsg,execve", "-o", str(case / ("audit-" + label)), *map(str, command)]

def network_audit(case, port):
    files = sorted(case.glob("audit-*"))
    require(files, "missing destination/exec audit")
    calls = []
    for path in files:
        if not path.is_file():
            continue
        for line in path.read_text(errors="replace").splitlines():
            if "AF_INET" in line:
                calls.append({"path": path.name, "line": line})
                require("AF_INET6" not in line and 'inet_addr("127.0.0.1")' in line
                        and "htons(" + str(port) + ")" in line,
                        "unexpected network destination: " + line)
    require(calls, "no real loopback model connect captured")
    return calls

async def cli(client, args, case, env, owner, binary, wrapper):
    root = fixture(case, client + "-full")
    wf = Workflow(owner, client + "-full", root)
    owner.workflows[wf.nonce] = wf
    socket = str(case / "h.sock")
    mcp = [str(wrapper), "mcp", "--socket", socket, "--profile", "full", "--reply-style", "plain"]
    base = "http://127.0.0.1:" + str(owner.port)
    if client == "claude":
        env.update(ANTHROPIC_BASE_URL=base, ANTHROPIC_API_KEY=KEY)
        write_json(case / "claude-mcp.json", {"mcpServers": {"huyang": {
            "type": "stdio", "command": mcp[0], "args": mcp[1:]}}})
        command = [str(binary), "--bare", "-p", "--model", "claude-sonnet-4-6",
                   "--output-format", "stream-json", "--verbose", "--no-session-persistence",
                   "--setting-sources", "", "--strict-mcp-config", "--mcp-config", str(case / "claude-mcp.json"),
                   "--tools", "", "--allowedTools", "mcp__huyang__*", "--permission-mode", "dontAsk",
                   "--permission-prompts", "none", wf.prompt()]
    else:
        env["HUYANG_MOCK_KEY"] = KEY
        config = (
            'model = "gpt-6.1-sol"\nmodel_provider = "acceptance"\napproval_policy = "never"\n'
            'sandbox_mode = "danger-full-access"\nweb_search = "disabled"\nproject_root_markers = []\n'
            'cli_auth_credentials_store = "file"\n'
            '[features]\nenable_request_compression = false\napps = false\nmulti_agent = false\nhooks = false\n'
            '[model_providers.acceptance]\nname = "Private deterministic mock"\n'
            + "base_url = " + json.dumps(base + "/v1") + "\n"
            + 'env_key = "HUYANG_MOCK_KEY"\nwire_api = "responses"\nrequires_openai_auth = false\n'
            'supports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\n'
            '[mcp_servers.huyang]\ncommand = ' + json.dumps(mcp[0]) + "\nargs = " + json.dumps(mcp[1:])
            + '\nrequired = true\nstartup_timeout_sec = 30\ntool_timeout_sec = 30\n'
            'default_tools_approval_mode = "auto"\n')
        (case / "codex" / "config.toml").write_text(config)
        command = [str(binary), "exec", "--strict-config", "--ignore-rules", "--skip-git-repo-check",
                   "--ephemeral", "--json", "--color", "never", "-C", str(case / "fixture"), wf.prompt()]
    with (case / "client.stdout.jsonl").open("wb") as stdout, (case / "client.stderr").open("wb") as stderr:
        process = await spawn(traced(args, case, "client", command), env, case / "fixture", stdout, stderr)
        try:
            await asyncio.wait_for(process.wait(), args.case_deadline)
            require(process.returncode == 0, client + " exit " + str(process.returncode))
        finally:
            await stop(process)
    require(wf.done, client + " ended before full workflow")
    output = (case / "client.stdout.jsonl").read_text()
    def strings(value):
        if isinstance(value, str):
            yield value
        elif isinstance(value, dict):
            for child in value.values():
                yield from strings(child)
        elif isinstance(value, list):
            for child in value:
                yield from strings(child)
    transcript = [json.loads(line) for line in output.splitlines() if line.strip()]
    require(any(wf.final in s for item in transcript for s in strings(item)),
            "actual client transcript lacks observed final marker/guards")

async def run_case(client, args, manifest, candidate, runtime):
    case = args.job / "c" / client
    case.mkdir(parents=True)
    (case / "fixture").mkdir()
    tool_path = str(args.job / "bin") + ":" + str(args.nvim.parent) + ":" + args.tool_path
    env = environment(case, tool_path)
    owner = await Controller(case, json.loads((HERE / "lean-schemas.json").read_text())).start()
    env.update(ACCEPTANCE_CONTROL=str(owner.control_path),
               ACCEPTANCE_FIXTURE=str(case / "fixture"), ACCEPTANCE_CANDIDATE=str(candidate),
               ACCEPTANCE_WITNESS=str(case / "plugin-witness.jsonl"),
               HUYANG_RUNTIME_PATH=str(runtime), HUYANG_HEADLESS_INIT=str(headless_init(case, args.job / "bin/gopls")),
               HUYANG_FRICTION="0", HUYANG_SOCKET=str(case / "h.sock"))
    wrapper = recorder(case, candidate)
    write_json(case / "case-manifest.json", {
        "client": client, "cwd": str(case / "fixture"), "uid": os.getuid(),
        "environment_keys": sorted(env), "model_port": owner.port, "service_socket": str(case / "h.sock"),
        "binary": manifest["binaries"][client], "candidate_sha256": file_hash(candidate),
        "plugin_sha256": file_hash(args.plugin) if client == "opencode" else None,
        "recorder_sha256": file_hash(wrapper), "runtime_files": manifest["candidate_runtime_files"]})
    stdout = (case / "service.stdout").open("wb")
    stderr = (case / "service.stderr").open("wb")
    service = None
    success = False
    try:
        service = await spawn(traced(args, case, "service", [candidate, "serve",
            "--socket", case / "h.sock", "--state-dir", case / "h-state"]),
            env, case / "fixture", stdout, stderr)
        await socket_ready(case / "h.sock", service)
        binary = Path(manifest["binaries"][client]["path"])
        async with asyncio.timeout(args.case_deadline):
            if client == "opencode":
                opencode.configure(case, args.plugin, wrapper, owner.port)
                # ACP owns its process; this launcher records its real native process tree.
                launcher = case / "bin" / "opencode-traced"
                launcher.write_text("#!" + sys.executable + "\nimport os\nos.execv(" + repr(str(args.strace))
                    + ", " + repr(list(map(str, traced(args, case, "client", [binary]))))
                    + " + __import__('sys').argv[1:])\n")
                launcher.chmod(0o755)
                await opencode.exercise(launcher, env, owner, lambda nonce: fixture(case, nonce))
            else:
                await cli(client, args, case, env, owner, binary, wrapper)
        require(not owner.failures, str(owner.failures))
        require(owner.initialized and owner.initialized == owner.listed,
                "missing actual MCP initialized/tools/list handshake")
        full = [w for w in owner.workflows.values() if w.profile == "full" and not w.probe]
        require(full and all(w.done and w.late and w.set_handle and w.guard for w in full),
                "full-profile evidence incomplete")
        require(all(w.done for w in owner.workflows.values()), "unfinished scenario")
        require(all(r["matched"] for r in owner.receipts), "unscripted or unmatched actual MCP call")
        for uri, held in owner.held.items():
            require([e["sha256"] for e in owner.deliveries.get(uri, [])] == [e["sha256"] for e in held],
                    "gopls publication bytes/order/count changed at release")
        require(file_hash(binary) == manifest["binaries"][client]["sha256"], "client self-modified")
        success = True
    except Exception as error:
        owner.fail(error)
    finally:
        if service:
            await stop(service)
        stdout.close()
        stderr.close()
        try:
            audit = network_audit(case, owner.port)
            write_json(case / "destination-audit.json", audit)
        except Exception as error:
            owner.fail(error)
            success = False
        await owner.close()
        write_json(case / "report.json", {"status": "passed" if success and not owner.failures else "failed",
                                          **owner.report()})
    return success and not owner.failures

async def main(args):
    namespace_required()
    args.job = args.job.resolve()
    for name in ("candidate", "runtime", "nvim", "plugin", "strace"):
        setattr(args, name, getattr(args, name).resolve())
    manifest = json.loads((args.job / "bootstrap-report.json").read_text())
    require(manifest["status"] == "bootstrapped-not-accepted", "bootstrap did not complete")
    require(file_hash(args.job / "tools/package-lock.json") == manifest["package_lock_sha256"],
            "bootstrap lock changed")
    for value in manifest["binaries"].values():
        require(file_hash(value["path"]) == value["sha256"], "bootstrapped client executable changed")
    require(file_hash(args.job / "bin/gopls") == manifest["gopls"]["sha256"], "gopls artifact changed")
    require(not (args.job / "c").exists(), "case artifacts already exist; retain failed run and bootstrap a new --job")
    require(args.plugin.name == "huyang-profiles.js", "stage the exact candidate V2 module")
    require((args.runtime / "lua/huyang/rpc.lua").is_file(), "matching candidate Lua runtime missing")
    require(re.fullmatch(r"[0-9a-f]{40}", args.source_commit), "exact candidate --source-commit required")
    candidate = args.job / "bin/huyang-candidate"
    shutil.copyfile(args.candidate, candidate)
    candidate.chmod(0o755)
    runtime = args.job / "runtime"
    shutil.copytree(args.runtime / "lua", runtime / "lua")
    manifest["candidate_runtime_files"] = {
        str(p.relative_to(runtime)): file_hash(p) for p in sorted((runtime / "lua").rglob("*")) if p.is_file()}
    manifest["source_commit"] = args.source_commit
    manifest["candidate_sha256"] = file_hash(candidate)
    env = environment(args.job / "preflight", args.tool_path)
    nvim_version = await capture([args.nvim, "--version"], env, args.job, args.job / "nvim-version.txt")
    match = re.search(r"NVIM v(\d+)\.(\d+)", nvim_version)
    require(match and tuple(map(int, match.groups())) >= (0, 11), "Neovim >=0.11 required")
    manifest["nvim"] = {"path": str(args.nvim), "sha256": file_hash(args.nvim), "version": nvim_version}
    nvim_launcher = args.job / "bin/nvim"
    nvim_launcher.write_text("#!" + sys.executable + "\nimport os, sys\nos.execv("
                             + repr(str(args.nvim)) + ", [" + repr(str(args.nvim)) + ", *sys.argv[1:]])\n")
    nvim_launcher.chmod(0o755)
    manifest["nvim_launcher_sha256"] = file_hash(nvim_launcher)
    manifest["candidate_go_build"] = await capture(["go", "version", "-m", candidate], env, args.job,
                                                  args.job / "candidate-build.txt")
    manifest["harness_files"] = {str(p.relative_to(HERE)): file_hash(p) for p in sorted(HERE.rglob("*"))
                                 if p.is_file() and "__pycache__" not in p.parts}
    write_json(args.job / "runtime-manifest.json", manifest)
    outcomes = {}
    for client in ("claude", "codex", "opencode"):
        outcomes[client] = await run_case(client, args, manifest, candidate, runtime)
    write_json(args.job / "report.json", {"status": "passed" if all(outcomes.values()) else "failed",
                                         "cases": outcomes, "source_commit": args.source_commit})
    require(all(outcomes.values()), "actual-client acceptance failed; preserve entire --job directory")

if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--job", required=True, type=Path)
    p.add_argument("--job-isolated", required=True, action="store_true")
    p.add_argument("--candidate", required=True, type=Path)
    p.add_argument("--runtime", required=True, type=Path)
    p.add_argument("--source-commit", required=True)
    p.add_argument("--plugin", required=True, type=Path)
    p.add_argument("--nvim", required=True, type=Path)
    p.add_argument("--strace", required=True, type=Path)
    p.add_argument("--tool-path", default=os.defpath)
    p.add_argument("--case-deadline", type=int, default=300)
    a = p.parse_args()
    try:
        asyncio.run(main(a))
    except BaseException as error:
        if a.job.exists():
            write_json(a.job / "failure.json", {"status": "failed", "error": repr(error)})
        raise
