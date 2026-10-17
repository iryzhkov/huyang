"""Private acceptance helpers; only Python stdlib, no model or MCP SDK."""
import asyncio
import base64
import hashlib
import json
import os
from pathlib import Path
import signal
import time

KEY = "sk-huyang-acceptance-not-a-secret"
PLUGIN_COMMIT = "ebec80bca66d684d6fe47c5c57c7cf212b6558b2"
PLUGIN_BLOB = "88ea20a15450e52fd94194a1f91ff69e7277da30"
PLUGIN_SHA256 = "e46883992727741758115492030f75bb26dbea14dccd3a4b4c3459cc5547f6c4"
HERE = Path(__file__).resolve().parent
GO_CLI = Path("/opt/chaldea-tools/go/1.27.0/bin/go")
GO_SHA256 = "1db869c560a193573a71be466a34e0d4abb7792d78165c6102cdda069276a3a8"
FULL = {"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate",
        "read", "diagnostics", "edit_apply", "change_plan", "revision_diff",
        "evidence_get", "language_server_status", "language_server_setup"}
LEAN = {"read", "search", "edit_apply"}

def require(condition, message):
    if not condition:
        raise AssertionError(message)

def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)

def digest(data):
    return hashlib.sha256(data).hexdigest()

def file_hash(path):
    return digest(Path(path).read_bytes())

def write_json(path, value):
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    Path(path).write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")

def record(path, /, **entry):
    entry = {"time_ns": time.monotonic_ns(), "pid": os.getpid(), **entry}
    # One append write, also safe for independent recorder processes.
    data = (canonical(entry) + "\n").encode()
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    try:
        os.write(fd, data)
    finally:
        os.close(fd)

def snapshot(path):
    s = Path(path).stat()
    return {"sha256": file_hash(path), "size": s.st_size, "mtime_ns": s.st_mtime_ns,
            "inode": s.st_ino}

def guard_scratch_job(job, namespace_guard):
    namespace_guard()
    home = Path("/home/job")
    require(os.environ.get("HOME") == str(home), "HOME must remain /home/job")
    require(home.is_dir() and not home.is_symlink() and home.resolve() == home, "scratch HOME must be a real directory")
    job = Path(job).resolve()
    require(job != home and job.is_relative_to(home), "job write root must resolve below scratch HOME")
    return job

def clean_home_preflight(report_path, namespace_guard):
    """Check discovery-path names only; never read auth/config contents or alter HOME."""
    report_path = guard_scratch_job(report_path, namespace_guard)
    raw_home = os.environ.get("HOME")
    report = {"home": raw_home, "checked_paths": [], "existing_paths": [],
              "patterns": [".claude*"], "contents_read": False}
    try:
        require(raw_home and Path(raw_home).is_absolute(), "HOME must name the namespace's absolute ordinary home")
        home = Path(raw_home)
        require(home.is_dir(), "namespace HOME is not an accessible directory")
        names = {".codex", ".agents", ".opencode", ".claude", ".npmrc",
                 ".config/codex", ".config/opencode", ".config/claude",
                 ".local/share/opencode", ".local/state/opencode", ".cache/opencode"}
        names.update(path.name for path in home.iterdir() if path.name.startswith(".claude"))
        report["checked_paths"] = sorted(names)
        # lexists also rejects dangling links instead of silently treating them as clean.
        report["existing_paths"] = [name for name in sorted(names) if os.path.lexists(home / name)]
        report["resolved_home"] = str(home.resolve())
    except (OSError, AssertionError) as error:
        report["error"] = repr(error)
    report["status"] = "clean" if not report.get("error") and not report["existing_paths"] else "refused"
    write_json(report_path, report)
    require(report["status"] == "clean", "unclean or unverifiable HOME; see " + str(report_path))
    return report

def require_no_telemetry_overrides(env):
    require(not any(k in {"TEST_TELEMETRY_DIR", "GOTELEMETRYDIR"} or k.startswith("GO_TELEMETRY_")
                    for k in env), "telemetry override/child environment refused")

def environment(case, tool_path):
    require_no_telemetry_overrides(os.environ)
    case = Path(case)
    case.mkdir(parents=True, exist_ok=True, mode=0o700)
    env = {"PATH": str(GO_CLI.parent) + ":" + tool_path, "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8",
           "TERM": "dumb", "CI": "1", "NO_COLOR": "1",
           "PYTHONDONTWRITEBYTECODE": "1",
           "DO_NOT_TRACK": "1", "GOTOOLCHAIN": "local", "GOPROXY": "off",
           "GOSUMDB": "off", "GOTELEMETRY": "off", "GIT_TERMINAL_PROMPT": "0",
           "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
           "npm_config_ignore_scripts": "true", "npm_config_audit": "false",
           "npm_config_fund": "false", "npm_config_update_notifier": "false",
           "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
           "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL": "1",
           "DISABLE_AUTOUPDATER": "1", "DISABLE_TELEMETRY": "1",
           "OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_DISABLE_TELEMETRY": "1"}
    # Preserve the namespace's real home, never assign a synthetic HOME.
    if "HOME" in os.environ:
        env["HOME"] = os.environ["HOME"]
    for key, sub in {
        "TMPDIR": "tmp", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache",
        "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state",
        "XDG_RUNTIME_DIR": "run", "CODEX_HOME": "codex",
        "CLAUDE_CONFIG_DIR": "claude", "OPENCODE_CONFIG_DIR": "oc-config",
        "GOPATH": "go", "GOMODCACHE": "go/pkg/mod", "GOCACHE": "go-build",
        "npm_config_cache": "npm-cache",
    }.items():
        path = case / sub
        path.mkdir(parents=True, exist_ok=True, mode=0o700)
        env[key] = str(path)
    npmrc = case / "empty-npmrc"
    npmrc.write_text("")
    env["npm_config_userconfig"] = str(npmrc)
    global_npmrc = case / "empty-global-npmrc"
    global_npmrc.write_text("")
    env["npm_config_globalconfig"] = str(global_npmrc)
    env["ENABLE_TOOL_SEARCH"] = "false"
    return env

async def control(event):
    reader, writer = await asyncio.open_unix_connection(os.environ["ACCEPTANCE_CONTROL"])
    writer.write((canonical(event) + "\n").encode())
    await writer.drain()
    answer = json.loads(await asyncio.wait_for(reader.readline(), 30))
    writer.close()
    await writer.wait_closed()
    require(not answer.get("error"), str(answer))
    return answer

async def stop(process):
    # Also reap a client's remaining MCP descendants after its leader exited.
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        await asyncio.wait_for(process.wait(), 5)
    except asyncio.TimeoutError:
        os.killpg(process.pid, signal.SIGKILL)
        await process.wait()
    finally:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

async def spawn(command, env, cwd, stdout, stderr, stdin=None):
    return await asyncio.create_subprocess_exec(
        *map(str, command), env=env, cwd=cwd, stdin=stdin,
        stdout=stdout, stderr=stderr, start_new_session=True, limit=8*1024*1024)

def raw_record(path, direction, raw, **extra):
    record(path, direction=direction, bytes_b64=base64.b64encode(raw).decode(),
           sha256=digest(raw), **extra)
