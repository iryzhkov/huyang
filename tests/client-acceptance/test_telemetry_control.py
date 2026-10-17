"""Pure telemetry-control regressions. No filesystem writes or native processes.

Mocked command/profile data does not qualify Go, a tracer, NSS or any client.
"""
import copy
from datetime import datetime, timezone
import io
import os
from pathlib import Path
import stat
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

sys.dont_write_bytecode = True
import telemetry_control as telemetry
from common import environment, require_no_telemetry_overrides

CASE = Path("/home/job/.cache/hf/ca/c/claude")
ENV = {"HOME": "/home/job", "XDG_CONFIG_HOME": str(CASE / "config"), "GOTELEMETRY": "off"}
START = datetime(2026, 10, 17, 23, 59, 59, tzinfo=timezone.utc)
END = datetime(2026, 10, 18, 0, 0, 1, tzinfo=timezone.utc)
RAW = b"off 2026-10-17"
MODE = CASE / "config/go/telemetry/mode"


def info(mode=stat.S_IFREG | 0o600, **changes):
    values = dict(st_dev=1, st_ino=42, st_mode=mode, st_uid=0, st_gid=0,
                  st_nlink=1, st_size=14, st_mtime_ns=10, st_ctime_ns=11, st_atime_ns=12)
    values.update(changes)
    return SimpleNamespace(**values)


class ModeInput(io.BytesIO):
    def fileno(self):
        return 3


class TelemetryControlTests(unittest.TestCase):
    def test_wrong_or_missing_manifest_pin_refused_before_binary_read(self):
        for seal in (None, {}, {**telemetry.PIN, "sha256": "0" * 64},
                     {**telemetry.PIN, "path": "/usr/bin/go"}):
            with self.subTest(seal=seal), patch.object(telemetry, "file_hash") as read:
                with self.assertRaises(AssertionError):
                    telemetry.pin(seal)
                read.assert_not_called()

    def test_wrong_binary_hash_refused(self):
        with patch.object(Path, "resolve", lambda p: p), \
             patch.object(Path, "lstat", return_value=info(stat.S_IFREG | 0o755)), \
             patch.object(telemetry.os, "access", return_value=True), \
             patch.object(telemetry, "file_hash", return_value="0" * 64):
            with self.assertRaises(AssertionError):
                telemetry.pin(dict(telemetry.PIN))

    def test_overrides_even_empty_are_rejected_in_ambient_and_passed_environment(self):
        keys = ("TEST_TELEMETRY_DIR", "GOTELEMETRYDIR", "GO_TELEMETRY_CHILD",
                "GO_TELEMETRY_CHILD_UPLOAD", "GO_TELEMETRY_FUTURE")
        for key in keys:
            with self.subTest(key=key), self.assertRaises(AssertionError):
                require_no_telemetry_overrides({key: ""})
            with patch.dict(os.environ, {key: ""}, clear=True), \
                 patch.object(Path, "mkdir") as mkdir:
                with self.assertRaises(AssertionError):
                    environment(CASE, "/usr/bin")
                mkdir.assert_not_called()
            with patch.dict(os.environ, {"HOME": "/home/job"}, clear=True):
                with self.assertRaises(AssertionError):
                    telemetry.profile_path(CASE, {**ENV, key: ""})

    def test_wrong_home_xdg_and_noncanonical_case_refused(self):
        with patch.dict(os.environ, {"HOME": "/home/job"}, clear=True), \
             patch.object(Path, "resolve", lambda p: p):
            for env in ({**ENV, "HOME": "/root"},
                        {**ENV, "XDG_CONFIG_HOME": "/home/job/outer/config"}):
                with self.subTest(env=env), self.assertRaises(AssertionError):
                    telemetry.profile_path(CASE, env)
            with self.assertRaises(AssertionError):
                telemetry.profile_path(Path("/outside/case"), ENV)
        with patch.dict(os.environ, {"HOME": "/home/job"}, clear=True), \
             patch.object(Path, "resolve", return_value=CASE.parent / "alias"):
            with self.assertRaises(AssertionError):
                telemetry.profile_path(CASE, ENV)

    def test_symlink_wrong_uid_and_nonprivate_profile_directories_refused(self):
        for mode, uid in ((stat.S_IFLNK | 0o700, 0), (stat.S_IFDIR | 0o755, 0),
                          (stat.S_IFDIR | 0o700, 1)):
            with self.subTest(mode=mode, uid=uid), \
                 patch.object(Path, "lstat", return_value=info(mode, st_uid=uid)), \
                 patch.object(telemetry.os, "geteuid", return_value=0):
                with self.assertRaises(AssertionError):
                    telemetry.directory(CASE, private=True)

    def test_only_canonical_off_bytes_inside_actual_utc_window(self):
        telemetry.validate_mode(RAW, START, END)
        telemetry.validate_mode(b"off 2026-10-18", START, END)
        for raw in (b"off", RAW + b"\n", b"on 2026-10-17", b"off 2026-10-16",
                    b"off 2026-10-19", b"off 2026-1-17"):
            with self.subTest(raw=raw), self.assertRaises(AssertionError):
                telemetry.validate_mode(raw, START, END)
        with self.assertRaises(AssertionError):
            telemetry.validate_mode(RAW, END, START)

    def test_natural_zero_empty_outputs_required(self):
        telemetry.validate_exit(0, 0, 0)
        for result in ((1, 0, 0, False), (-9, 0, 0, False), (None, 0, 0, True),
                       (0, 1, 0, False), (0, 0, 1, False), (0, 0, 0, True)):
            with self.subTest(result=result), self.assertRaises(AssertionError):
                telemetry.validate_exit(*result)

    def test_profile_checks_mode_metadata_and_only_telemetry_children(self):
        # No inventory of the whole XDG root is needed, so client config siblings
        # do not become a false telemetry failure.
        def children(path):
            self.assertEqual(path, MODE.parent)
            return iter([MODE])
        with patch.object(telemetry, "profile_path", return_value=(MODE, {})), \
             patch.object(telemetry, "directory", return_value=telemetry.metadata(info(stat.S_IFDIR | 0o700))), \
             patch.object(Path, "iterdir", children), \
             patch.object(telemetry.os, "open", return_value=3) as opened, \
             patch.object(telemetry.os, "fdopen", side_effect=lambda *a: ModeInput(RAW)), \
             patch.object(telemetry.os, "fstat", return_value=info()), \
             patch.object(Path, "lstat", return_value=info()), \
             patch.object(telemetry.os, "geteuid", return_value=0):
            result = telemetry.profile(CASE, ENV, START, END)
            self.assertEqual(result["bytes_hex"], RAW.hex())
            self.assertTrue(opened.call_args.args[1] & os.O_NOFOLLOW)
            for bad in (info(stat.S_IFREG | 0o644), info(st_uid=1), info(st_nlink=2),
                        info(st_size=15), info(stat.S_IFLNK | 0o600)):
                with patch.object(telemetry.os, "fstat", return_value=bad):
                    with self.assertRaises(AssertionError):
                        telemetry.profile(CASE, ENV, START, END)
            with patch.object(Path, "iterdir", return_value=iter([MODE, MODE.parent / "local"])):
                with self.assertRaises(AssertionError):
                    telemetry.profile(CASE, ENV, START, END)
            with patch.object(Path, "lstat", return_value=info(st_ino=99)):
                with self.assertRaises(AssertionError):
                    telemetry.profile(CASE, ENV, START, END)

    def test_recheck_rejects_bytes_hash_or_metadata_changes_but_ignores_atime(self):
        before = {"sha256": "sealed", "metadata": telemetry.metadata(info())}
        prepared = {"started_utc": START.isoformat(), "finished_utc": END.isoformat(), "before": before}
        changed = []
        for key in before["metadata"]:
            candidate = copy.deepcopy(before)
            candidate["metadata"][key] += 1
            changed.append(candidate)
        changed.append({**before, "sha256": "changed"})
        self.assertEqual(telemetry.metadata(info()), telemetry.metadata(info(st_atime_ns=999)))
        with patch.object(telemetry, "guard_scratch_job", return_value=CASE), \
             patch.object(telemetry, "pin", return_value=dict(telemetry.PIN)), \
             patch.object(telemetry, "profile", return_value=before) as read, \
             patch.object(telemetry, "write_json") as write, patch.object(Path, "chmod"):
            telemetry.recheck(CASE, ENV, prepared, telemetry.PIN, lambda: None)
            self.assertEqual(write.call_args.args[1]["status"], "stable-not-qualified")
            for after in changed:
                read.return_value = after
                with self.subTest(after=after), self.assertRaises(AssertionError):
                    telemetry.recheck(CASE, ENV, prepared, telemetry.PIN, lambda: None)
                self.assertEqual(write.call_args.args[1]["status"], "failed")


class PreparationOrderingTests(unittest.IsolatedAsyncioTestCase):
    async def test_command_retains_outputs_and_requires_natural_empty_exit(self):
        for code, size, timeout in ((0, 0, False), (1, 0, False), (-9, 0, False),
                                    (0, 1, False), (0, telemetry.OUTPUT_LIMIT + 1, False), (None, 0, True)):
            with self.subTest(code=code, size=size, timeout=timeout):
                process = SimpleNamespace(pid=123, returncode=code,
                    wait=AsyncMock(side_effect=TimeoutError() if timeout else None))
                report: dict = {"argv": [str(telemetry.GO_CLI), "telemetry", "off"]}
                with patch.object(Path, "open", side_effect=lambda *a: io.BytesIO()), \
                     patch.object(Path, "exists", return_value=True), \
                     patch.object(Path, "stat", return_value=info(st_size=size)), \
                     patch.object(telemetry, "file_hash", return_value="output-hash"), \
                     patch.object(telemetry.os, "umask", return_value=0o022) as umask, \
                     patch.object(telemetry, "spawn", new_callable=AsyncMock, return_value=process) as spawn, \
                     patch.object(telemetry, "stop", new_callable=AsyncMock) as stop:
                    if code == 0 and size == 0 and not timeout:
                        await telemetry.off_command(CASE, ENV, CASE / "telemetry-control", report)
                    else:
                        with self.assertRaises((AssertionError, TimeoutError)):
                            await telemetry.off_command(CASE, ENV, CASE / "telemetry-control", report)
                    self.assertEqual(spawn.call_args.args[:3], (report["argv"], ENV, CASE))
                    stop.assert_awaited_once_with(process)
                    self.assertEqual(report["returncode_before_cleanup"], code)
                    self.assertEqual(report["outputs"]["stdout"]["size"], size)
                    self.assertIn("finished_utc", report)
                    self.assertEqual(report.get("timed_out", False), timeout)
                    self.assertEqual(umask.call_args.args, (0o022,))

    async def test_unsafe_or_existing_profile_aborts_before_command(self):
        with patch.object(telemetry, "guard_scratch_job", return_value=CASE), \
             patch.object(Path, "mkdir"), patch.object(Path, "chmod"), \
             patch.object(telemetry, "write_json") as write, \
             patch.object(telemetry, "profile_path", return_value=(MODE, {})) as paths, \
             patch.object(telemetry.os.path, "lexists", return_value=True), \
             patch.object(telemetry, "off_command", new_callable=AsyncMock) as command:
            with self.assertRaises(AssertionError):
                await telemetry.prepare(CASE, ENV, telemetry.PIN, lambda: None)
            command.assert_not_awaited()
            self.assertEqual(write.call_args.args[1]["status"], "failed")
            paths.side_effect = AssertionError("unsafe profile")
            with self.assertRaises(AssertionError):
                await telemetry.prepare(CASE, ENV, telemetry.PIN, lambda: None)
            command.assert_not_awaited()

    async def test_abnormal_command_keeps_failure_proof_and_never_prepares(self):
        with patch.object(telemetry, "guard_scratch_job", return_value=CASE), \
             patch.object(Path, "mkdir"), patch.object(Path, "chmod"), \
             patch.object(telemetry, "write_json") as write, \
             patch.object(telemetry, "profile_path", return_value=(MODE, {})), \
             patch.object(telemetry.os.path, "lexists", return_value=False), \
             patch.object(telemetry, "identity", return_value={"uid": [0, 0, 0]}), \
             patch.object(telemetry, "pin", return_value=dict(telemetry.PIN)), \
             patch.object(telemetry, "profile") as profile, \
             patch.object(telemetry, "off_command", new_callable=AsyncMock,
                          side_effect=AssertionError("nonempty stdout")):
            with self.assertRaises(AssertionError):
                await telemetry.prepare(CASE, ENV, telemetry.PIN, lambda: None)
            report = write.call_args.args[1]
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["argv"], [str(telemetry.GO_CLI), "telemetry", "off"])
            self.assertEqual(report["environment"], ENV)
            profile.assert_not_called()


if __name__ == "__main__":
    unittest.main()
