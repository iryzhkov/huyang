#!/usr/bin/env python3
"""Release guard and installed artifact tests; run inside an asserted scratch HOME."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import selectors
import shutil
import struct
import subprocess
import tarfile
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parent.parent
VERSION = "huyang-v1.2.3"


def assert_scratch():
    expected = os.environ.get("HUYANG_RELEASE_TEST_HOME", "")
    home = Path(expected)
    if (not home.is_absolute() or str(home) != os.environ.get("HOME")
            or home.resolve() != home
            or "/.cache/chaldea-scratch/agent-tooling/" not in str(home)):
        raise RuntimeError("HOME must equal HUYANG_RELEASE_TEST_HOME under task scratch")
    temporary = Path(os.environ.get("TMPDIR", ""))
    scratch = home.parent
    if not temporary.is_absolute() or not temporary.resolve().is_relative_to(scratch):
        raise RuntimeError("TMPDIR must be inside the same task scratch directory")


assert_scratch()
SPEC = importlib.util.spec_from_file_location("package_release", ROOT / "scripts/package-release.py")
assert SPEC is not None and SPEC.loader is not None
PACK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PACK)


def run(*args, cwd=None):
    return subprocess.check_output(args, cwd=cwd, text=True, stderr=subprocess.STDOUT).strip()


class ReleaseGuard(unittest.TestCase):
    def setUp(self):
        assert_scratch()
        self.temp = tempfile.TemporaryDirectory(prefix="guard-")
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.name", "Release test")
        self.git("config", "user.email", "release@example.invalid")
        self.git("commit", "-qm", "base", "--allow-empty")
        self.base = self.git("rev-parse", "HEAD")
        self.git("commit", "-qm", "main", "--allow-empty")
        self.main = self.git("rev-parse", "HEAD")
        self.git("update-ref", "refs/remotes/origin/main", self.main)

    def git(self, *args):
        return run("git", *args, cwd=self.repo)

    def guard(self, version=VERSION, commit="HEAD", success=True):
        result = subprocess.run(
            ["sh", str(ROOT / "scripts/check-release"), version, commit],
            cwd=self.repo, capture_output=True, text=True,
        )
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)

    def test_lightweight_and_annotated_tags_on_main_history(self):
        self.git("checkout", "-q", self.base)
        self.git("tag", VERSION)
        self.guard()
        self.git("tag", "-d", VERSION)
        self.git("tag", "-am", "annotated release", VERSION)
        self.guard(commit=VERSION)

    def test_unmerged_commit_is_refused(self):
        self.git("checkout", "-qb", "side", self.base)
        self.git("commit", "-qm", "unmerged", "--allow-empty")
        self.git("tag", VERSION)
        self.guard(success=False)

    def test_missing_main_or_tag_is_refused(self):
        self.guard(success=False)
        self.git("tag", VERSION)
        self.git("update-ref", "-d", "refs/remotes/origin/main")
        self.guard(success=False)

    def test_mismatched_commit_or_checkout_is_refused(self):
        self.git("tag", VERSION, self.base)
        self.guard(commit=self.main, success=False)
        self.guard(commit=self.base, success=False)

    def test_invalid_versions_are_refused_even_with_existing_tags(self):
        for version in ("huyang-v1", "huyang-v1.2.3-rc1", "huyang-v1x2x3", "other-v1.2.3"):
            with self.subTest(version=version):
                self.git("tag", version)
                self.guard(version=version, success=False)


class ReleaseArtifacts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        assert_scratch()
        cls.temp = tempfile.TemporaryDirectory(prefix="release-")
        cls.addClassCleanup(cls.temp.cleanup)
        cls.work = Path(cls.temp.name)
        cls.output = cls.work / "release"
        PACK.package(VERSION, cls.output)

    def test_archives_are_complete_regular_files_with_verified_manifests(self):
        # This independently names the installed payload, rather than asking
        # the packager what it intended to include.
        expected = {"bin/huyang", "LICENSE", "README.md", "contrib/systemd/huyang.service"}
        expected.update(p.relative_to(ROOT).as_posix() for p in (ROOT / "lua").rglob("*") if p.is_file())
        self.assertEqual({p.name for p in self.output.iterdir()},
                         {"huyang-linux-x64.tar.gz", "huyang-linux-arm64.tar.gz"})
        for architecture, machine in (("x64", 62), ("arm64", 183)):
            with self.subTest(architecture=architecture):
                path = self.output / f"huyang-linux-{architecture}.tar.gz"
                with tarfile.open(path, "r:gz") as archive:
                    entries = archive.getmembers()
                    names = [entry.name for entry in entries]
                    self.assertEqual(len(names), len(set(names)), "duplicate members")
                    self.assertEqual(set(names), expected | {"manifest.json"})
                    for entry in entries:
                        self.assertTrue(entry.isfile(), entry.name)
                        parts = PurePosixPath(entry.name)
                        self.assertFalse(parts.is_absolute())
                        self.assertNotIn("..", parts.parts)
                        self.assertEqual(entry.mode, 0o755 if entry.name == "bin/huyang" else 0o644)
                    data = {}
                    for entry in entries:
                        stream = archive.extractfile(entry)
                        assert stream is not None
                        data[entry.name] = stream.read()
                manifest = json.loads(data.pop("manifest.json"))
                self.assertEqual(set(manifest), {"version", "architecture", "files"})
                self.assertEqual(manifest["version"], VERSION)
                self.assertEqual(manifest["architecture"], architecture)
                self.assertEqual(manifest["files"],
                                 {name: hashlib.sha256(body).hexdigest() for name, body in data.items()})
                for name in expected - {"bin/huyang"}:
                    self.assertEqual(data[name], (ROOT / name).read_bytes(), name)
                binary = data["bin/huyang"]
                self.assertEqual(binary[:6], b"\x7fELF\x02\x01")
                self.assertEqual(struct.unpack_from("<H", binary, 18)[0], machine)
                phoff = struct.unpack_from("<Q", binary, 32)[0]
                phsize, phnum = struct.unpack_from("<HH", binary, 54)
                segments = [struct.unpack_from("<I", binary, phoff + i * phsize)[0]
                            for i in range(phnum)]
                self.assertNotIn(3, segments, "binary has a dynamic interpreter")

    def test_repeat_build_is_byte_identical(self):
        repeat = self.work / "repeat"
        PACK.package(VERSION, repeat)
        for archive in self.output.iterdir():
            self.assertEqual(archive.read_bytes(), (repeat / archive.name).read_bytes())

    def test_payload_refuses_links_and_missing_kernel(self):
        fixture = self.work / "payload"
        (fixture / "lua/huyang").mkdir(parents=True)
        for name in PACK.EXTRAS:
            target = fixture / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("fixture")
        with self.assertRaisesRegex(ValueError, "kernel entry"):
            PACK.payload(fixture)
        (fixture / "lua/huyang/rpc.lua").symlink_to(fixture / "LICENSE")
        with self.assertRaisesRegex(ValueError, "symlink"):
            PACK.payload(fixture)

    def test_installed_service_discovers_kernel_outside_checkout(self):
        self.assertIsNotNone(shutil.which("nvim"), "Neovim API 13+ is required for the release smoke test")
        installed = self.work / "installed"
        installed.mkdir()
        with tarfile.open(self.output / "huyang-linux-x64.tar.gz") as archive:
            archive.extractall(installed, filter="data")
        fixture = self.work / "project"
        fixture.mkdir()
        (fixture / "sample.lua").write_text("local value = 1\nreturn value\n")
        init = self.work / "init.lua"
        init.write_text("-- no user plugins or language tools needed for this smoke test\n")
        environment = {k: v for k, v in os.environ.items()
                       if not k.startswith(("HUYANG_", "AGENT99_"))}
        environment.update(HUYANG_FRICTION="0", HUYANG_HEADLESS_INIT=str(init))
        binary = str(installed / "bin/huyang")
        socket = self.work / "s"
        with (self.work / "service.log").open("w+") as log:
            daemon = subprocess.Popen(
                [binary, "serve", "--socket", str(socket), "--state-dir", str(self.work / "state")],
                cwd=fixture, env=environment, stdout=log, stderr=log,
            )
            try:
                deadline = time.monotonic() + 15
                while not socket.exists() and daemon.poll() is None and time.monotonic() < deadline:
                    time.sleep(0.02)
                self.assertTrue(socket.exists(), "packaged daemon did not start")
                with subprocess.Popen(
                    [binary, "mcp", "--socket", str(socket), "--profile", "full", "--reply-style", "json"],
                    cwd=fixture, env=environment, stdin=subprocess.PIPE,
                    stdout=subprocess.PIPE, stderr=log, text=True,
                ) as adapter:
                    try:
                        self.rpc(adapter, 1, "initialize", {
                            "protocolVersion": "2024-11-05", "capabilities": {},
                            "clientInfo": {"name": "release-smoke", "version": "1"},
                        })
                        assert adapter.stdin is not None
                        adapter.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
                        adapter.stdin.flush()
                        opened = self.rpc(adapter, 2, "tools/call", {
                            "name": "workspace_open", "arguments": {"root": str(fixture)},
                        })
                        envelope = json.loads(opened["content"][0]["text"])
                        self.assertEqual(envelope["outcome"], "ok", envelope)
                        provider = envelope["data"]["semantic_provider"]
                        log.flush()
                        log.seek(0)
                        self.assertIsNotNone(provider, f"{envelope}\n{log.read()}")
                        self.assertEqual(provider["state"], "healthy", envelope)
                        status = self.rpc(adapter, 3, "tools/call", {
                            "name": "language_server_status",
                            "arguments": {"workspace_id": envelope["workspace"]["id"]},
                        })
                        result = json.loads(status["content"][0]["text"])
                        # This clean init deliberately has no language server.
                        # The response must come from the installed kernel, with
                        # the external prerequisite reported explicitly.
                        self.assertEqual(result["code"], "language_server_unavailable", result)
                        self.assertEqual(result["data"]["provider"]["state"], "healthy", result)
                        self.assertEqual(result["data"]["missing_languages"], ["lua"], result)
                    finally:
                        adapter.terminate()
                        adapter.wait(timeout=10)
            finally:
                daemon.terminate()
                daemon.wait(timeout=10)

    def rpc(self, process, request_id, method, params):
        process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": request_id,
                                        "method": method, "params": params}) + "\n")
        process.stdin.flush()
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                self.assertTrue(selector.select(max(0, deadline - time.monotonic())), "MCP reply timed out")
                line = process.stdout.readline()
                self.assertTrue(line, "MCP adapter exited")
                response = json.loads(line)
                if response.get("id") == request_id:
                    self.assertNotIn("error", response, response)
                    return response["result"]
        self.fail("MCP reply timed out")


if __name__ == "__main__":
    assert_scratch()
    unittest.main(verbosity=2)
