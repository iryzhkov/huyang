#!/usr/bin/env python3
"""Explicit job-only bootstrap. Never invoked by repository unit tests."""
import argparse
import asyncio
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import re
import subprocess
import sys

sys.dont_write_bytecode = True
from common import GO_CLI, HERE, clean_home_preflight, guard_scratch_job, environment, file_hash, require, write_json
import nss_control
import telemetry_control

PINS = {
    "claude": ("2.1.301", "node_modules/@anthropic-ai/claude-code-linux-x64/claude"),
    "codex": ("0.163.0", "node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex"),
    "opencode": ("2.0.30", "node_modules/@opencode/cli-linux-x64/bin/opencode"),
}
GOPLS = "v0.21.1"

def namespace_required():
    require(os.geteuid() == 0, "requires Chaldea's disposable UID0 job namespace")
    mapping = Path("/proc/self/uid_map").read_text().split()
    require(mapping != ["0", "0", "4294967295"], "refusing the initial host user namespace")
    require(platform.system() == "Linux" and platform.machine() == "x86_64",
            "pins require Linux x86_64 glibc; no fallback package")
    require(platform.libc_ver()[0] == "glibc", "pins require glibc")

def command(argv, env, cwd, log, timeout=600):
    with Path(log).open("wb") as output:
        process = subprocess.Popen(list(map(str, argv)), cwd=cwd, env=env,
                                   stdin=subprocess.DEVNULL, stdout=output,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        try:
            process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
            raise
        result = process
    require(result.returncode == 0, "bootstrap command failed; see " + str(log))
    return Path(log).read_text(errors="replace")

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--job", required=True, type=Path)
    p.add_argument("--job-isolated", required=True, action="store_true")
    p.add_argument("--tool-path", default=os.defpath)
    p.add_argument("--gopls-artifact", type=Path)
    p.add_argument("--gopls-sha256")
    p.add_argument("--nvim-nss-control", action="store_true", help="opt in to pinned native-Nvim NSS acquisition control")
    args = p.parse_args()
    job = guard_scratch_job(args.job, namespace_required)
    require(not job.exists(), "bootstrap --job must be new (retain prior artifacts on failure)")
    if args.nvim_nss_control:
        nss_control.sources(job)  # Validate exact job mounts/accounts/maps before any writes.
    job.mkdir(parents=True, mode=0o700)
    tools, logs = job / "tools", job / "bootstrap"
    tools.mkdir()
    logs.mkdir()
    write_json(job / "bootstrap-report.json", {"status": "running"})
    try:
        home_preflight = clean_home_preflight(job / "home-bootstrap-preflight.json", namespace_required)
        env = environment(job / "bootstrap-env", args.tool_path)
        go_cli = telemetry_control.pin()
        telemetry = asyncio.run(telemetry_control.prepare(job / "bootstrap-env", env, go_cli, namespace_required))
        nss = nss_control.provision(job, env, command) if args.nvim_nss_control else None
        for name in ("package.json", "package-lock.json"):
            shutil.copyfile(HERE / name, tools / name)
        lock = json.loads((tools / "package-lock.json").read_text())
        for key, package in lock["packages"].items():
            if not key:
                continue
            require(package.get("integrity", "").startswith("sha512-"), "unlocked package SRI: " + key)
            require(package.get("resolved", "").startswith("https://registry.npmjs.org/"),
                    "nonpublic package resolution: " + key)
        before = file_hash(tools / "package-lock.json")
        node = command(["node", "--version"], env, job, logs / "node-version.txt", 15)
        npm = command(["npm", "--version"], env, job, logs / "npm-version.txt", 15)
        command(["npm", "ci", "--prefix", tools, "--ignore-scripts", "--legacy-peer-deps",
                 "--no-audit", "--no-fund", "--registry", "https://registry.npmjs.org/"],
                env, tools, logs / "npm-ci.txt")
        require(file_hash(tools / "package-lock.json") == before, "npm changed locked resolution")
        binaries = {}
        for name, (version, relative) in PINS.items():
            path = tools / relative
            require(path.is_file(), "native optional package absent: " + str(path))
            output = command([path, "--version"], env, job, logs / (name + "-version.txt"), 15)
            expected = {"claude": {"2.1.301 (Claude Code)"},
                        "codex": {"codex-cli 0.163.0"},
                        "opencode": {"opencode v2.0.30", "2.0.30"}}[name]
            require(output.strip() in expected, "native version gate failed: " + name + " " + output)
            binaries[name] = {"path": str(path), "sha256": file_hash(path), "version": output.strip()}
        bindir = job / "bin"
        bindir.mkdir()
        go_version = command([GO_CLI, "version"], env, job, logs / "go-version.txt", 15)
        if args.gopls_artifact:
            require(args.gopls_sha256 and file_hash(args.gopls_artifact) == args.gopls_sha256,
                    "staged gopls requires matching explicit --gopls-sha256")
            shutil.copyfile(args.gopls_artifact, bindir / "gopls")
            (bindir / "gopls").chmod(0o755)
        else:
            env.update(GOBIN=str(bindir), GOPROXY="https://proxy.golang.org", GOSUMDB="sum.golang.org")
            command([GO_CLI, "install", "golang.org/x/tools/gopls@" + GOPLS], env, job, logs / "gopls-install.txt")
        version = command([bindir / "gopls", "version"], env, job, logs / "gopls-version.txt", 15)
        require("golang.org/x/tools/gopls " + GOPLS in version, "real gopls pin mismatch: " + version)
        build = command([GO_CLI, "version", "-m", bindir / "gopls"], env, job, logs / "gopls-build.txt", 15)
        # SDK imports from the actual discovered plugin resolve only to this job's locked dependencies.
        (job / "node_modules").symlink_to(tools / "node_modules", target_is_directory=True)
        telemetry_control.recheck(job / "bootstrap-env", env, telemetry, go_cli, namespace_required)
        write_json(job / "bootstrap-report.json", {
            "go_cli": go_cli, "telemetry_preparation": telemetry,
            "status": "bootstrapped-not-accepted", "uid": os.getuid(), "node": node.strip(),
            "npm": npm.strip(), "go": go_version.strip(), "package_lock_sha256": before,
            "binaries": binaries, "gopls": {"path": str(bindir / "gopls"),
                "version": version.strip(), "sha256": file_hash(bindir / "gopls"), "build": build},
            "home_preflight": home_preflight,
            "nss_control": nss, "synthetic_credentials_only": True, "scripts": "ignored"})
    except BaseException as error:
        write_json(job / "bootstrap-report.json", {"status": "failed", "error": repr(error),
            "home_preflight_report": str(job / "home-bootstrap-preflight.json")})
        raise

if __name__ == "__main__":
    main()
