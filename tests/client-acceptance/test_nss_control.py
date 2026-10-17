"""Synthetic NSS boundary regressions. No processes, files, sockets or services."""
import copy
import io
import json
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

sys.dont_write_bytecode = True
import nss_control as nss

JOB = Path("/home/job/.cache/hf/ca")
NATIVE = Path("/home/job/.cache/hf/nvim/bin/nvim")
UUID = "59cae96a-8999-4931-9d5b-34fdf4ea74d7"
PREFIX = "/slot-2/" + UUID
MOUNTS = "\n".join([
    "609 572 0:84 /newroot / ro,nosuid - tmpfs tmpfs rw",
    "619 609 7:0 " + PREFIX + ".identity/passwd /etc/passwd ro,nosuid - ext4 /dev/loop0 rw",
    "620 609 7:0 " + PREFIX + ".identity/group /etc/group ro,nosuid - ext4 /dev/loop0 rw",
    "613 609 8:17 /etc/nsswitch.conf /etc/nsswitch.conf ro,nosuid - ext4 /dev/sdb1 rw",
    "633 609 7:0 " + PREFIX + "/.chaldea-home /home/job rw,nosuid - ext4 /dev/loop0 rw",
])
IDENTITY = {"HOME": "/home/job", "uid": [0, 0, 0], "gid": [0, 0, 0], "groups": [0],
            "status": [["Uid:", "0", "0", "0", "0"], ["Gid:", "0", "0", "0", "0"], ["Groups:", "0"]],
            "uid_map": "0 0 1\n", "gid_map": "0 0 1\n",
            "namespaces": {"user": "user:[4026532796]", "mnt": "mnt:[1]", "pid": "pid:[2]", "net": "net:[3]"}}
POLICY = b"passwd: files\ngroup: files\n"


class SourceGuards(unittest.TestCase):
    def test_all_three_zero_indexed_agent_a_slots(self):
        for slot in range(3):
            nss.validate_mounts(MOUNTS.replace("/slot-2/", "/slot-" + str(slot) + "/"), IDENTITY)
        with self.assertRaises(AssertionError):
            nss.validate_mounts(MOUNTS.replace("/slot-2/", "/slot-3/"), IDENTITY)

    def test_account_mount_provenance_cannot_be_relaxed(self):
        changes = [
            (PREFIX + ".identity/passwd", "/etc/passwd"),
            (PREFIX + ".identity/group", PREFIX + "/group"),
            (UUID + ".identity/group", "00000000-8999-4931-9d5b-34fdf4ea74d7.identity/group"),
            (".identity/passwd /etc/passwd ro,", ".identity/passwd /etc/passwd rw,"),
            ("620 609 7:0", "620 609 8:17"),
            ("/newroot / ro,", "/newroot / rw,"),
            ("/.chaldea-home /home/job", "/.chaldea-home-other /home/job"),
            ("/home/job rw,nosuid - ext4", "/home/job rw,nosuid - tmpfs"),
        ]
        for old, new in changes:
            with self.subTest(old=old), self.assertRaises(AssertionError):
                nss.validate_mounts(MOUNTS.replace(old, new), IDENTITY)
        for suffix in ("\n" + MOUNTS.splitlines()[1],
                       "\n700 609 8:17 /etc /etc ro,nosuid - ext4 /dev/sdb1 rw"):
            with self.assertRaises(AssertionError):
                nss.validate_mounts(MOUNTS + suffix, IDENTITY)
        with self.assertRaises(AssertionError):
            nss.validate_mounts("\n".join(MOUNTS.splitlines()[1:]), IDENTITY)

    def test_identity_maps_home_and_namespace_are_exact(self):
        for key, wrong in (("HOME", "/root"), ("uid", [0, 1, 0]), ("gid", [1, 1, 1]),
                           ("groups", [0, 1]), ("uid_map", "0 0 4294967295\n"),
                           ("gid_map", "0 0 2\n"),
                           ("status", [["Uid:", "0", "0", "0", "1"], *IDENTITY["status"][1:]]),
                           ("namespaces", {**IDENTITY["namespaces"], "user": "user:[4026531837]"})):
            with self.subTest(key=key), self.assertRaises(AssertionError):
                nss.validate_mounts(MOUNTS, {**IDENTITY, key: wrong})

    def test_full_records_and_files_only_policy(self):
        nss.validate_records(dict(nss.ACCOUNTS), POLICY)
        for name, raw in (("passwd", b"root:x:0:0::/root:/bin/sh\n"),
                          ("passwd", nss.ACCOUNTS["passwd"] + b"\n"),
                          ("passwd", nss.ACCOUNTS["passwd"].replace(b"/home/job", b"/changed")),
                          ("group", b"job:x:0:other\n")):
            with self.subTest(name=name, raw=raw), self.assertRaises(AssertionError):
                nss.validate_records({**nss.ACCOUNTS, name: raw}, POLICY)
        for policy in (b"passwd: files systemd\ngroup: files\n",
                       b"passwd: files\ngroup: compat\n", POLICY + b"passwd: files\n",
                       b"passwd: files [SUCCESS=return]\ngroup: files\n", b"passwd: files\n"):
            with self.subTest(policy=policy), self.assertRaises(AssertionError):
                nss.validate_records(dict(nss.ACCOUNTS), policy)

    def test_empty_and_unfamiliar_overrides_are_refused(self):
        nss.require_clean({"HOME": "/home/job", "PATH": "/usr/bin"})
        for key in (*nss.LOADER_KEYS, "NSS_WRAPPER_MODULE_SO_PATH", "NSS_WRAPPER_HOSTNAME"):
            with self.subTest(key=key), self.assertRaises(AssertionError):
                nss.require_clean({key: ""})

    def test_invalid_mounts_stop_before_account_reads_or_writes(self):
        with patch.dict(nss.os.environ, {"HOME": "/home/job"}, clear=True), \
             patch.object(nss.Path, "resolve", lambda p: p), \
             patch.object(nss.Path, "is_dir", return_value=True), \
             patch.object(nss.Path, "exists", return_value=False), \
             patch.object(nss.Path, "stat", return_value=type("Info", (), {"st_uid": 0})()), \
             patch.object(nss, "identity", return_value=IDENTITY), \
             patch.object(nss.Path, "read_text", return_value=MOUNTS.replace(".identity/passwd", "/passwd")), \
             patch.object(nss, "bounded") as read_accounts, patch.object(nss.Path, "mkdir") as mkdir:
            with self.assertRaises(AssertionError):
                nss.sources(JOB)
            read_accounts.assert_not_called()
            mkdir.assert_not_called()


class PinnedInputs(unittest.TestCase):
    def inputs(self):
        source = {"identity": copy.deepcopy(IDENTITY), "mountinfo": MOUNTS, "nsswitch_sha256": nss.digest(POLICY)}
        report = {"status": "provisioned-not-accepted", "source": source,
                  "package_url": nss.PACKAGE_URL, "package_version": "1.1.16-1",
                  "package_sha256": nss.PACKAGE_SHA256, "library_sha256": nss.LIBRARY_SHA256,
                  "native_sha256": nss.NATIVE_SHA256,
                  "fixture_sha256": {k: nss.digest(v) for k, v in nss.ACCOUNTS.items()}}
        root, package, library = nss.paths(JOB)
        data = {root / "manifest.json": json.dumps(report).encode(), package: b"package", library: b"library",
                NATIVE: b"native", **{root / k: v for k, v in nss.ACCOUNTS.items()}}
        return source, report, data

    def verify(self, data, source):
        real_digest = nss.digest
        pins = {b"package": nss.PACKAGE_SHA256, b"library": nss.LIBRARY_SHA256, b"native": nss.NATIVE_SHA256}
        with patch.object(nss, "sources", return_value=(source, dict(nss.ACCOUNTS))), \
             patch.object(nss, "bounded", side_effect=lambda p, maximum: data[Path(p)]), \
             patch.object(nss, "digest", side_effect=lambda raw: pins.get(raw, real_digest(raw))), \
             patch.object(nss.os, "access", return_value=True):
            return nss.verify(JOB, NATIVE)

    def test_all_fixed_pins_and_fixtures_are_rechecked(self):
        source, report, data = self.inputs()
        self.assertEqual(self.verify(data, source), report)
        for path in [p for p in data if p.name != "manifest.json"]:
            changed = {**data, path: b"tampered"}
            with self.subTest(path=path), self.assertRaises(AssertionError):
                self.verify(changed, source)

    def test_manifest_cannot_authorize_different_bytes_or_identity(self):
        for key, value in (("package_sha256", "0" * 64), ("library_sha256", "0" * 64),
                           ("native_sha256", "0" * 64), ("package_version", "1.1.17"),
                           ("package_url", "https://example.invalid/nss.deb"), ("source", {})):
            source, report, data = self.inputs()
            report[key] = value
            data[JOB / "nss/manifest.json"] = json.dumps(report).encode()
            with self.subTest(key=key), self.assertRaises(AssertionError):
                self.verify(data, source)


class NativeBoundary(unittest.TestCase):
    def test_native_exec_alone_gets_nss_and_preserves_argv_home(self):
        init = JOB / "c/claude/init.lua"
        raw = nss.PRODUCER_LUA.encode() + b"-- synthetic init\n"
        argv = ["--embed", "--headless", "--clean", "-u", str(init)]
        env = {"HOME": "/home/job", "HUYANG_HEADLESS_INIT": str(init), "PATH": "/usr/bin"}
        data = {init: raw, init.parent / "nss-init.json": json.dumps({"sha256": nss.digest(raw)}).encode()}
        with patch.dict(nss.os.environ, env, clear=True), \
             patch.object(nss, "verify") as verify, patch.object(nss.Path, "resolve", lambda p: p), \
             patch.object(nss, "bounded", side_effect=lambda p, maximum: data[p]), \
             patch.object(nss.os, "execve") as execute:
            nss.exec_native(NATIVE, JOB, argv)
            verify.assert_called_once_with(JOB, NATIVE)
            native, actual, child_env = execute.call_args.args
            self.assertEqual(native, str(NATIVE))
            self.assertEqual(actual, [str(NATIVE), *argv])
            self.assertEqual(dict(nss.os.environ), env)
            self.assertEqual(child_env["HOME"], "/home/job")
            self.assertEqual(child_env["LD_PRELOAD"], str(JOB / "nss" / nss.LIBRARY_RELATIVE))
            self.assertEqual(child_env["NSS_WRAPPER_PASSWD"], str(JOB / "nss/passwd"))
            self.assertEqual(child_env["NSS_WRAPPER_GROUP"], str(JOB / "nss/group"))
            self.assertNotIn("LD_LIBRARY_PATH", child_env)

    def test_missing_or_sibling_init_cannot_exec_native(self):
        for init, argv in (("/home/job/.cache/hf/other/init.lua", ["--embed", "--headless"]),
                           (str(JOB / "c/claude/init.lua"), ["--embed", "--headless", "-u", "NONE"])):
            with patch.dict(nss.os.environ, {"HUYANG_HEADLESS_INIT": init}, clear=True), \
                 patch.object(nss, "verify"), patch.object(nss.Path, "resolve", lambda p: p), \
                 patch.object(nss.os, "execve") as execute:
                with self.assertRaises(AssertionError):
                    nss.exec_native(NATIVE, JOB, argv)
                execute.assert_not_called()

    def test_changed_init_seal_refuses_exec(self):
        init = JOB / "c/claude/init.lua"
        data = {init: nss.PRODUCER_LUA.encode() + b"-- modified",
                init.parent / "nss-init.json": b'{"sha256":"' + b"0" * 64 + b'"}'}
        with patch.dict(nss.os.environ, {"HUYANG_HEADLESS_INIT": str(init)}, clear=True), \
             patch.object(nss, "verify"), patch.object(nss.Path, "resolve", lambda p: p), \
             patch.object(nss, "bounded", side_effect=lambda p, maximum: data[p]), \
             patch.object(nss.os, "execve") as execute:
            with self.assertRaises(AssertionError):
                nss.exec_native(NATIVE, JOB, ["--embed", "--headless", "--clean", "-u", str(init)])
            execute.assert_not_called()

    def test_symlink_input_refused_before_open(self):
        with patch.object(nss.Path, "resolve", return_value=Path("/outside")), \
             patch.object(nss.os, "open") as opened:
            with self.assertRaises(AssertionError):
                nss.bounded(JOB / "nss/passwd", 1024)
            opened.assert_not_called()

    def test_missing_or_wrong_native_binding_cannot_emit_proof(self):
        case = JOB / "c/claude"
        for files in ([], [case / "nss-bindings.69"]):
            with patch.object(nss, "verify"), patch.object(nss.Path, "glob", return_value=files), \
                 patch.object(nss, "bounded", return_value=b"binding file /usr/bin/python3\n"), \
                 patch.object(nss, "write_json") as write:
                with self.assertRaises(AssertionError):
                    nss.finish_case(case, JOB, NATIVE)
                write.assert_not_called()

    def test_binding_evidence_requires_exact_native_library_and_symbol(self):
        library = JOB / "nss" / nss.LIBRARY_RELATIVE
        line = ("  69:\tbinding file " + str(NATIVE) + " [0] to " + str(library)
                + " [0]: normal symbol " + chr(96) + "getpwuid' [GLIBC_2.2.5]")
        self.assertEqual(nss.native_binding_lines(line.encode(), NATIVE, library), [line])
        for old, new in ((str(NATIVE), "/usr/bin/python3"), (str(library), "/other/libnss_wrapper.so"),
                         ("getpwuid'", "getpwuid_r'"), ("GLIBC_2.2.5", "OTHER")):
            self.assertEqual(nss.native_binding_lines(line.replace(old, new).encode(), NATIVE, library), [])

    def test_relay_refuses_overrides_before_any_maps_or_artifact_access(self):
        with patch.dict(nss.os.environ, {"NSS_WRAPPER_PASSWD": "/somewhere"}, clear=True), \
             patch.object(nss.Path, "open") as opened, patch.object(nss, "write_json") as write:
            with self.assertRaises(AssertionError):
                nss.producer_boundary(JOB / "c/claude/lsp-wire.jsonl", [str(JOB / "bin/gopls")])
            opened.assert_not_called()
            write.assert_not_called()

    def test_relay_rejects_mapped_wrapper_even_with_clean_environment(self):
        with patch.dict(nss.os.environ, {}, clear=True), \
             patch.object(nss.Path, "is_file", return_value=True), \
             patch.object(nss.Path, "resolve", lambda p: p), patch.object(nss, "verify"), \
             patch.object(nss.Path, "open", return_value=io.BytesIO(b"/private/libnss_wrapper.so\n")), \
             patch.object(nss, "write_json") as write:
            with self.assertRaises(AssertionError):
                nss.producer_boundary(JOB / "c/claude/lsp-wire.jsonl", [str(JOB / "bin/gopls")])
            write.assert_not_called()


if __name__ == "__main__":
    unittest.main()
