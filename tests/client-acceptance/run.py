#!/usr/bin/env python3
"""Run only inside the parent's disposable job after explicit bootstrap and candidate build."""
import argparse
import ast
import asyncio
import ctypes
import json
import os
from pathlib import Path
import re
import shutil
import sys

sys.dont_write_bytecode = True
from bootstrap import namespace_required
from common import (HERE, KEY, PLUGIN_BLOB, PLUGIN_COMMIT, PLUGIN_SHA256, clean_home_preflight,
                    guard_scratch_job, canonical, digest, environment, file_hash, record, require, spawn, stop, write_json)
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

def recorder(case, candidate, control_path):
    bindir = case / "bin"
    bindir.mkdir()
    path = bindir / "huyang"
    source = ("#!" + sys.executable + "\nimport os, sys\n"
              + "os.environ['ACCEPTANCE_CONTROL'] = " + repr(str(control_path)) + "\n"
              + "os.environ['ACCEPTANCE_FIXTURE'] = " + repr(str(case / "fixture")) + "\n"
              + "args = sys.argv[1:]\n"
              + "assert args and args[0] == 'mcp', 'acceptance recorder accepts only mcp'\n"
              + "profile = 'full'\n"
              + "for i, arg in enumerate(args):\n"
              + "    if arg == '--profile': profile = args[i + 1]\n"
              + "    elif arg.startswith('--profile='): profile = arg.split('=', 1)[1]\n"
              + "os.execv(sys.executable, [sys.executable, '-B', " + repr(str(HERE / "wire.py"))
              + ", '--log', " + repr(str(case / "mcp-wire.jsonl"))
              + ", '--profile', profile, 'mcp', '--', " + repr(str(candidate)) + ", *args])\n")
    path.write_text(source)
    path.chmod(0o755)
    return path

def headless_init(case, gopls):
    cmd = [sys.executable, "-B", str(HERE / "wire.py"), "--log", str(case / "lsp-wire.jsonl"),
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
            + "  before_init = function(params) params.capabilities.textDocument.diagnostic = nil end,\n"
            + "  settings = {gopls = {diagnosticsDelay = '0s'}},\n"
            + "})\nvim.lsp.enable('gopls')\n")
    path = case / "init.lua"
    path.write_text(text)
    return path

def traced(args, case, label, command):
    return [str(args.strace), "-ff", "-ttt", "-T", "-y", "-s", "4096", "-e",
            "trace=network,process,close,close_range,dup,dup2,dup3,fcntl,unshare,setns",
            "-o", str(case / ("audit-" + label)), *map(str, command)]

def network_audit(case, port, job, ipc_proof=None):
    from ipc_audit import destination_text, read_trace_records
    parsed = read_trace_records(case)
    report = {"ip_destinations": [], "unix_destinations": [], "execve_recorded": [],
              "model_connects": [], "acp_ipc_records": [],
              "execve_policy": "recorded only; no executable allowlist asserted",
              "unix_allowed_root": str(job.resolve()), "violations": list(parsed["violations"])}
    allowed = set()
    if ipc_proof is not None:
        if ipc_proof["violations"]:
            report["violations"].extend("ACP IPC proof: " + v for v in ipc_proof["violations"])
        else:
            allowed = {(r["path"], r["line"]) for r in ipc_proof["allowed_connect_records"]}
            if any(not re.fullmatch(r"audit-client\.\d+", path) for path, _ in allowed):
                report["violations"].append("ACP IPC exemption contains non-client trace records")
                allowed.clear()
    used = set()
    for syscall in parsed["records"]:
        if "call" not in syscall:
            continue  # Parser-validated signal/exit metadata has no destination.
        originals = syscall["original_records"]
        entry = {"tid": syscall["tid"], "call": syscall["call"], "result": syscall["result"],
                 "start_ns": syscall["start_ns"], "end_ns": syscall["end_ns"],
                 "text": syscall["text"], "original_records": originals}
        if syscall["call"] in ("execve", "execveat"):
            report["execve_recorded"].append(entry)
        try:
            require(syscall["complete"] is True, "incomplete syscall destination evidence")
            address = destination_text(syscall)
            if address is None:
                continue
            entry["destination"] = address
            if re.search(r"\bsa_family=AF_INET6?\b", address):
                report["ip_destinations"].append(entry)
                host = re.search(r'\bsin_addr=inet_addr\("([^"]+)"\)', address)
                dest_port = re.search(r"\bsin_port=htons\((\d+)\)", address)
                port_number = int(dest_port[1]) if dest_port is not None else None
                loopback = ("AF_INET6" not in address and host is not None
                            and host[1] == "127.0.0.1" and port_number is not None)
                if loopback and port_number == port:
                    if syscall["call"] == "connect":
                        report["model_connects"].append(entry)
                    continue
                keys = {(r["path"], r["line"]) for r in originals}
                if loopback and syscall["call"] == "connect" and keys and keys <= allowed:
                    used.update(keys)
                    entry["classification"] = "proven-native-acp-private-api"
                    report["acp_ipc_records"].append(entry)
                    continue
                raise AssertionError("unexpected IP destination")
            if re.search(r"\bsa_family=AF_UNIX\b", address):
                report["unix_destinations"].append(entry)
                match = re.search(r'\bsun_path=("(?:[^"\\]|\\.)*")', address)
                require(match is not None, "unknown or abstract AF_UNIX address")
                assert match is not None
                destination = ast.literal_eval(match[1])
                require(isinstance(destination, str) and Path(destination).is_absolute(),
                        "AF_UNIX destination is not an absolute job path")
                require(Path(destination).resolve().is_relative_to(job.resolve()),
                        "AF_UNIX destination escaped the job allowlist")
                entry["resolved_destination"] = str(Path(destination).resolve())
                continue
            raise AssertionError("unknown non-NULL socket destination")
        except (AssertionError, ValueError, SyntaxError, OSError, RuntimeError) as error:
            report["violations"].append(str(error) + ": " + canonical(entry))
    if used != allowed:
        report["violations"].append("ACP IPC proof records do not exactly match audited connect records")
    if not report["model_connects"]:
        report["violations"].append("no real model-port connect captured")
    return report

def codex_direct_catalog(case):
    """Explicit direct-tool coverage using the otherwise unchanged pinned entry."""
    source_path = HERE / "codex-model-entry.json"
    raw = source_path.read_bytes()
    require(digest(raw) == "84908db89e40af232436afe55da355d33b73d5c653ef402b2a820ce3012340f8",
            "pinned Codex source model entry changed")
    source = raw.decode("utf-8")
    original = json.loads(source)
    require(original["slug"] == "gpt-6.1-sol" and original["tool_mode"] == "code_mode_only"
            and original["supports_search_tool"] is True, "unexpected pinned Codex model defaults")
    override, modes = re.subn(r'("tool_mode"\s*:\s*)"code_mode_only"', r'\1"direct"', source)
    override, searches = re.subn(r'("supports_search_tool"\s*:\s*)true', r'\1false', override)
    require(modes == searches == 1 and json.loads(override) == {
        **original, "tool_mode": "direct", "supports_search_tool": False},
        "Codex model override changed more than its two authorized fields")
    path = case / "codex" / "model-catalog.json"
    path.write_bytes(b'{"models":[' + override.encode("utf-8") + b']}\n')
    provenance = {
        "coverage": "explicit-direct-tools-only", "model_slug": original["slug"],
        "source_url": "https://raw.githubusercontent.com/openai/codex/rust-v0.163.0/codex-rs/models-manager/models.json",
        "source_sha256": "8f32fc36f69a37b095edb86fd91e569876de89ee6c4de08bcafe1660027aa06e",
        "source_entry": str(source_path), "source_entry_sha256": digest(raw),
        "override_entry_sha256": digest(override.encode("utf-8")),
        "catalog": str(path), "catalog_sha256": file_hash(path),
        "overrides": {"tool_mode": "direct", "supports_search_tool": False}}
    write_json(case / "codex-model-catalog-provenance.json", provenance)
    return provenance

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
            + "model_catalog_json = " + json.dumps(str(case / "codex/model-catalog.json")) + "\n"
            + "log_dir = " + json.dumps(str(case / "codex/log")) + "\n"
            + '[features]\nenable_request_compression = false\napps = false\nmulti_agent = false\nhooks = false\nplugins = false\n'
            '[model_providers.acceptance]\nname = "Private deterministic mock"\n'
            + "base_url = " + json.dumps(base + "/v1") + "\n"
            + 'env_key = "HUYANG_MOCK_KEY"\nwire_api = "responses"\nrequires_openai_auth = false\n'
            'supports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\n'
            '[mcp_servers.huyang]\ncommand = ' + json.dumps(mcp[0]) + "\nargs = " + json.dumps(mcp[1:])
            + '\nrequired = true\nstartup_timeout_sec = 30\ntool_timeout_sec = 30\n'
            'startup_readiness = "catalog"\nomit_tools_from = ["deferred", "code_mode"]\n'
            'tool_input_schema_max_bytes = 131072\n'
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
    home_preflight = clean_home_preflight(case / "home-preflight.json", namespace_required)
    env = environment(case, tool_path)
    if client == "opencode":
        env["OPENCODE_DISABLE_MODELS_FETCH"] = "1"
    if client == "codex":
        env["RUST_LOG"] = "codex_core=debug,codex_mcp=debug"
    model_catalog = codex_direct_catalog(case) if client == "codex" else None
    owner = await Controller(case, json.loads((HERE / "lean-schemas.json").read_text())).start()
    env.update(ACCEPTANCE_CONTROL=str(owner.control_path),
               ACCEPTANCE_FIXTURE=str(case / "fixture"), ACCEPTANCE_CANDIDATE=str(candidate),
               ACCEPTANCE_WITNESS=str(case / "plugin-witness.jsonl"),
               HUYANG_RUNTIME_PATH=str(runtime), HUYANG_HEADLESS_INIT=str(headless_init(case, args.job / "bin/gopls")),
               HUYANG_FRICTION="0", HUYANG_SOCKET=str(case / "h.sock"))
    wrapper = recorder(case, candidate, owner.control_path)
    write_json(case / "case-manifest.json", {
        "client": client, "cwd": str(case / "fixture"), "uid": os.getuid(),
        "environment_keys": sorted(env), "model_port": owner.port, "service_socket": str(case / "h.sock"),
        "binary": manifest["binaries"][client], "candidate_sha256": file_hash(candidate),
        "plugin_sha256": args.plugin_sha256 if client == "opencode" else None,
        "home_preflight": home_preflight, "codex_model_catalog": model_catalog,
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
                opencode.configure(case, args.plugin, wrapper, owner.port, args.plugin_sha256)
                # ACP owns its process; this launcher records its real native process tree.
                launcher = case / "bin" / "opencode-traced"
                launcher.write_text("#!" + sys.executable + "\nimport os\nos.execv(" + repr(str(args.strace))
                    + ", " + repr(list(map(str, traced(args, case, "client", [binary]))))
                    + " + __import__('sys').argv[1:])\n")
                launcher.chmod(0o755)
                await opencode.exercise(launcher, env, owner, lambda nonce: fixture(case, nonce),
                                        binary, manifest["binaries"][client]["sha256"])
                require(file_hash(case / "oc-config/plugins/huyang-profiles.js") == args.plugin_sha256,
                        "discovered plugin changed during the actual client run")
            else:
                await cli(client, args, case, env, owner, binary, wrapper)
        require(not owner.failures, str(owner.failures))
        negotiated = owner.initialized | set(owner.discovered)
        require(negotiated and negotiated == owner.listed,
                "missing actual MCP legacy/stateless negotiation and tools/list")
        full = [w for w in owner.workflows.values() if w.profile == "full" and not w.probe]
        require(full and all(w.done and w.late and w.set_handle and w.guard for w in full),
                "full-profile evidence incomplete")
        require(all(w.done for w in owner.workflows.values()), "unfinished scenario")
        require(all(r["matched"] for r in owner.receipts), "unscripted or unmatched actual MCP call")
        initialized_relays = {e["relay"] for e in owner.lsp_initialize_results}
        require(initialized_relays, "missing actual gopls initialize result")
        for uri, held in owner.held.items():
            require(all(e["relay"] in initialized_relays for e in held),
                    "delayed producer lacks actual initialize-result evidence")
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
        if client == "opencode":
            # The parser reads client AND service traces. Both trees must be
            # stopped/flushed before proof, not only ACP at exercise teardown.
            try:
                from ipc_audit import audit_acp_ipc
                captures = owner.acp_ipc_captures
                require(captures is not None, "missing native ACP ownership captures")
                assert captures is not None
                owner.acp_ipc_proof = audit_acp_ipc(case, owner.port, *captures)
            except Exception as error:
                owner.acp_ipc_proof = {"allowed_connect_records": [], "violations": [repr(error)]}
            write_json(case / "acp-ipc-proof.json", owner.acp_ipc_proof)
        try:
            audit = network_audit(case, owner.port, args.job, owner.acp_ipc_proof)
            write_json(case / "destination-audit.json", audit)
            require(not audit["violations"], canonical(audit["violations"]))
        except Exception as error:
            owner.fail(error)
            success = False
        await owner.close()
        write_json(case / "report.json", {"status": "passed" if success and not owner.failures else "failed",
                                          **owner.report()})
    return success and not owner.failures

async def main(args):
    args.job = guard_scratch_job(args.job, namespace_required)
    for name in ("candidate", "runtime", "nvim", "plugin", "strace"):
        setattr(args, name, getattr(args, name).resolve())
    manifest = json.loads((args.job / "bootstrap-report.json").read_text())
    require(manifest["status"] == "bootstrapped-not-accepted", "bootstrap did not complete")
    manifest["runtime_home_preflight"] = clean_home_preflight(args.job / "home-runtime-preflight.json", namespace_required)
    require(file_hash(args.job / "tools/package-lock.json") == manifest["package_lock_sha256"],
            "bootstrap lock changed")
    for value in manifest["binaries"].values():
        require(file_hash(value["path"]) == value["sha256"], "bootstrapped client executable changed")
    require(file_hash(args.job / "bin/gopls") == manifest["gopls"]["sha256"], "gopls artifact changed")
    require(not (args.job / "c").exists(), "case artifacts already exist; retain failed run and bootstrap a new --job")
    require(args.plugin.name == "huyang-profiles.js", "stage the exact candidate V2 module")
    plugin_preflight = {"path": str(args.plugin), "source_commit": PLUGIN_COMMIT, "git_blob": PLUGIN_BLOB,
                        "expected_sha256": PLUGIN_SHA256, "provided_sha256": args.plugin_sha256,
                        "actual_sha256": file_hash(args.plugin)}
    write_json(args.job / "plugin-preflight.json", plugin_preflight)
    require(args.plugin_sha256 == PLUGIN_SHA256, "plugin pin must identify the approved merged Omarchy blob")
    require(plugin_preflight["actual_sha256"] == args.plugin_sha256, "staged plugin failed its immutable SHA-256 pin")
    manifest["plugin"] = plugin_preflight
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
    p.add_argument("--plugin-sha256", required=True)
    p.add_argument("--nvim", required=True, type=Path)
    p.add_argument("--strace", required=True, type=Path)
    p.add_argument("--tool-path", default=os.defpath)
    p.add_argument("--case-deadline", type=int, default=300)
    a = p.parse_args()
    a.job = guard_scratch_job(a.job, namespace_required)
    try:
        asyncio.run(main(a))
    except BaseException as error:
        if a.job.exists():
            write_json(a.job / "failure.json", {"status": "failed", "error": repr(error)})
        raise
