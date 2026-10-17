"""Supported, case-local telemetry preparation for the sealed Go CLI.

Only the pinned `go telemetry off` writes the profile. This is preparation
evidence, never native-client or tracer qualification.
"""
import asyncio
from datetime import datetime, timezone
import os
from pathlib import Path
import stat

from common import (GO_CLI, GO_SHA256, digest, file_hash, guard_scratch_job,
                    require, require_no_telemetry_overrides, spawn, stop, write_json)
from nss_control import identity

PIN = {"path": str(GO_CLI), "sha256": GO_SHA256}
TIMEOUT = 20
OUTPUT_LIMIT = 64 * 1024


def pin(expected=PIN):
    require(expected == PIN, "missing or incompatible sealed Go CLI")
    require(GO_CLI.resolve() == GO_CLI and stat.S_ISREG(GO_CLI.lstat().st_mode),
            "Go CLI must be the canonical pinned regular executable")
    require(os.access(GO_CLI, os.X_OK) and file_hash(GO_CLI) == GO_SHA256,
            "Go CLI SHA-256 pin mismatch")
    return dict(PIN)


def metadata(info):
    # atime changes on reads and is deliberately excluded.
    return {key: getattr(info, "st_" + key) for key in
            ("dev", "ino", "mode", "uid", "gid", "nlink", "size", "mtime_ns", "ctime_ns")}


def directory(path, private=False):
    info = path.lstat()
    require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.geteuid(),
            "profile ancestor must be a real UID-owned directory: " + str(path))
    if private:
        require(stat.S_IMODE(info.st_mode) == 0o700, "profile directory must be 0700: " + str(path))
    return metadata(info)


def profile_path(case, env):
    require_no_telemetry_overrides(os.environ)
    require_no_telemetry_overrides(env)
    home = Path("/home/job")
    require(env.get("HOME") == os.environ.get("HOME") == str(home), "scratch HOME changed")
    require(case.is_absolute() and case != home and case.is_relative_to(home)
            and case.resolve() == case, "noncanonical case below scratch HOME required")
    config = case / "config"
    require(env.get("XDG_CONFIG_HOME") == str(config), "wrong case XDG_CONFIG_HOME")
    ancestors = {}
    # Walk every existing ancestor, including HOME, without following links.
    path = home
    for component in (None, *config.relative_to(home).parts):
        if component is not None:
            path = path / component
        info = directory(path, private=path in (case, config))
        ancestors[str(path)] = {k: info[k] for k in ("dev", "ino", "mode", "uid", "gid")}
    return config / "go/telemetry/mode", ancestors


def validate_mode(raw, started, finished):
    require(started.tzinfo == timezone.utc and finished.tzinfo == timezone.utc
            and started <= finished, "invalid UTC command window")
    require(raw in {("off " + t.date().isoformat()).encode("ascii") for t in (started, finished)},
            "mode must be canonical dated off bytes from the command UTC window")


def profile(case, env, started, finished):
    mode, ancestors = profile_path(case, env)
    go = directory(mode.parent.parent, private=True)
    ancestors[str(mode.parent.parent)] = {k: go[k] for k in ("dev", "ino", "mode", "uid", "gid")}
    telemetry = directory(mode.parent, private=True)
    require(sorted(p.name for p in mode.parent.iterdir()) == ["mode"],
            "unexpected telemetry subtree entry")
    fd = os.open(mode, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid()
                and stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1
                and info.st_size == 14, "unsafe telemetry mode file")
        raw = stream.read(15)
        require(metadata(os.fstat(stream.fileno())) == metadata(info), "mode changed during read")
    require(metadata(mode.lstat()) == metadata(info), "mode path changed during read")
    validate_mode(raw, started, finished)
    return {"path": str(mode), "bytes_hex": raw.hex(), "text": raw.decode("ascii"),
            "sha256": digest(raw), "metadata": metadata(info),
            "telemetry_directory": telemetry, "ancestors": ancestors}


def validate_exit(returncode, stdout_size, stderr_size, timed_out=False):
    require(not timed_out and returncode == 0 and stdout_size == stderr_size == 0,
            "go telemetry off requires natural zero exit and empty stdout/stderr")


async def off_command(case, env, evidence, report):
    # Reuse the acceptance process-group spawn/stop helpers; no shell or HOME rewrite.
    process = None
    old_umask = os.umask(0o077)
    report["started_utc"] = datetime.now(timezone.utc).isoformat()
    try:
        with (evidence / "stdout").open("xb") as stdout, (evidence / "stderr").open("xb") as stderr:
            process = await spawn(report["argv"], env, case, stdout, stderr,
                                  stdin=asyncio.subprocess.DEVNULL)
            report["pid"] = process.pid
            try:
                await asyncio.wait_for(process.wait(), TIMEOUT)
            except asyncio.TimeoutError:
                report["timed_out"] = True
                raise
            finally:
                report["returncode_before_cleanup"] = process.returncode
                await stop(process)
    finally:
        os.umask(old_umask)
        report["finished_utc"] = datetime.now(timezone.utc).isoformat()
        report["outputs"] = {}
        for name in ("stdout", "stderr"):
            path = evidence / name
            if path.exists():
                size = path.stat().st_size
                report["outputs"][name] = {"path": str(path), "size": size}
                if size <= OUTPUT_LIMIT:
                    report["outputs"][name]["sha256"] = file_hash(path)
    sizes = [report["outputs"][name]["size"] for name in ("stdout", "stderr")]
    require(all(size <= OUTPUT_LIMIT for size in sizes), "oversized telemetry command output retained")
    validate_exit(report["returncode_before_cleanup"], sizes[0], sizes[1], report.get("timed_out", False))


async def prepare(case, env, sealed_go, namespace_guard):
    raw_case = Path(case)
    case = guard_scratch_job(raw_case, namespace_guard)
    require(case == raw_case, "noncanonical case path refused")
    evidence = case / "telemetry-control"
    evidence.mkdir(mode=0o700)
    report = {"status": "preparing", "argv": [str(GO_CLI), "telemetry", "off"],
              "environment": dict(env), "cwd": str(case), "timeout_seconds": TIMEOUT}
    try:
        mode, ancestors = profile_path(case, env)
        report["initial"] = {"mode_path": str(mode), "ancestors": ancestors,
                             "go_subtree_absent": not os.path.lexists(mode.parent.parent)}
        require(report["initial"]["go_subtree_absent"], "fresh case Go configuration required")
        report["go_cli"] = pin(sealed_go)
        report["identity_before"] = identity()
        await off_command(case, env, evidence, report)
        report["identity_after"] = identity()
        require(report["identity_before"] == report["identity_after"], "telemetry command identity changed")
        require(pin(sealed_go) == report["go_cli"], "Go CLI changed during preparation")
        report["before"] = profile(case, env, datetime.fromisoformat(report["started_utc"]),
                                   datetime.fromisoformat(report["finished_utc"]))
        with (evidence / "mode.before").open("xb") as output:
            os.fchmod(output.fileno(), 0o600)
            output.write(bytes.fromhex(report["before"]["bytes_hex"]))
        report["status"] = "prepared-not-qualified"
        return report
    except BaseException as error:
        report["status"] = "failed"
        report["error"] = repr(error)
        raise
    finally:
        if report["status"] == "failed" and "finished_utc" in report:
            try:
                report["identity_after"] = identity()
                report["observed_profile"] = profile(case, env,
                    datetime.fromisoformat(report["started_utc"]),
                    datetime.fromisoformat(report["finished_utc"]))
            except Exception as error:
                report["observation_error"] = repr(error)
        write_json(evidence / "preparation.json", report)
        (evidence / "preparation.json").chmod(0o600)


def recheck(case, env, prepared, sealed_go, namespace_guard):
    case = Path(case)
    # Refuse changed containment before any finally-path report write.
    require(guard_scratch_job(case, namespace_guard) == case, "case path changed")
    report: dict = {"status": "checking"}
    try:
        report["go_cli"] = pin(sealed_go)
        report["after"] = profile(case, env, datetime.fromisoformat(prepared["started_utc"]),
                                  datetime.fromisoformat(prepared["finished_utc"]))
        require(report["after"] == prepared["before"], "telemetry profile changed during actual case")
        report["status"] = "stable-not-qualified"
    except BaseException as error:
        report["status"] = "failed"
        report["error"] = repr(error)
        raise
    finally:
        write_json(case / "telemetry-control/recheck.json", report)
        (case / "telemetry-control/recheck.json").chmod(0o600)
