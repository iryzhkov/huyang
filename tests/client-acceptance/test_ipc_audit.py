"""Pure synthetic IPC evidence tests; no processes, sockets, /proc or disk writes."""
import copy
import io
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

sys.dont_write_bytecode = True
import ipc_audit as ipc


BINARY = "/scratch/opencode"
FD = "5<socket:[900]>"
PORT = 44187
MODEL_PORT = 18001


def sockaddr(port, address="127.0.0.1"):
    return ('{sa_family=AF_INET, sin_port=htons(' + str(port)
            + '), sin_addr=inet_addr("' + address + '")}')


def syscall(tid, stamp, text, label="client"):
    path = "audit-" + label + "." + str(tid)
    original = {"path": path, "line": stamp + " " + text}
    return ipc._record(tid, path, [original], ipc._ns(stamp),
                       ipc._ns(stamp), text, True)


def parse_memory(files):
    entries = [SimpleNamespace(name=name, path="/synthetic/" + name,
                               is_file=lambda **kwargs: True) for name in files]

    def open_memory(path, mode):
        return io.BytesIO(files[path.name].encode())

    with patch.object(ipc, "_entries", return_value=entries), \
            patch.object(Path, "open", open_memory):
        return ipc.read_trace_records("/synthetic")


def evidence():
    namespaces = {"net": "net:[1]", "mnt": "mnt:[2]", "user": "user:[3]"}

    def identity(pid, tgid, ppid, ticks):
        return {"pid": pid, "tgid": tgid, "ppid": ppid, "start_ticks": ticks,
                "uid": 17, "namespaces": namespaces.copy()}

    root = identity(1, 1, 0, 0)
    acp = identity(10, 10, 1, 0)
    server = identity(20, 20, 10, 2)
    acp_thread = identity(11, 10, 1, 6)
    server_thread = identity(21, 20, 10, 3)

    def native(owner, signature):
        return {"identity": owner, "executable": BINARY, "sha256": ipc.PINNED_SHA256,
                "exe_device": 1, "exe_inode": 2, "signature": signature}

    initial = {
        "version": 1, "violations": [], "model_port": MODEL_PORT,
        "begin_ns": ipc._ns("1000.080000"), "end_ns": ipc._ns("1000.090000"),
        "root_pid": 1, "root": root, "acp": native(acp, "acp"),
        "server": native(server, "serve --stdio --port 0"),
        "listener": {"fd": 5, "inode": 900, "address": "127.0.0.1", "port": PORT},
        "ancestry": [root, acp, server], "acp_tasks": [acp, acp_thread],
        "server_tasks": [server, server_thread],
        "expected_binary_path": BINARY, "expected_sha256": ipc.PINNED_SHA256,
        "clock_ticks": 100, "boot_realtime_ns": ipc._ns("1000.000000"),
    }
    final = copy.deepcopy(initial)
    final.update(begin_ns=ipc._ns("1000.300000"), end_ns=ipc._ns("1000.310000"))
    records = [
        syscall(10, "1000.010000", 'execve("' + BINARY + '", ["' + BINARY
                + '", "--print-logs", "acp"], 0x1 /* 1 vars */) = 0 <0.000001>'),
        syscall(10, "1000.020000", "clone(child_stack=NULL, flags=SIGCHLD) = 20 <0.000001>"),
        syscall(20, "1000.030000", 'execve("' + BINARY + '", ["' + BINARY
                + '", "serve", "--stdio", "--port", "0"], 0x1 /* 1 vars */) = 0 <0.000001>'),
        syscall(20, "1000.035000", "clone(child_stack=0x1, flags=CLONE_THREAD|CLONE_FILES"
                + "|CLONE_VM|CLONE_SIGHAND) = 21 <0.000001>"),
        syscall(21, "1000.040000", "bind(" + FD + ", " + sockaddr(0) + ", 16) = 0 <0.000001>"),
        syscall(21, "1000.050000", "listen(" + FD + ", 128) = 0 <0.000001>"),
        syscall(21, "1000.060000", "getsockname(" + FD + ", " + sockaddr(PORT)
                + ", [16]) = 0 <0.000001>"),
        syscall(10, "1000.065000", "clone(child_stack=0x1, flags=CLONE_THREAD|CLONE_FILES"
                + "|CLONE_VM|CLONE_SIGHAND) = 11 <0.000001>"),
        syscall(11, "1000.070000", "connect(7<socket:[901]>, " + sockaddr(PORT)
                + ", 16) = -1 EINPROGRESS (Operation now in progress) <0.000001>"),
        syscall(11, "1000.200000", "connect(8<socket:[902]>, " + sockaddr(PORT)
                + ", 16) = 0 <0.000001>"),
    ]
    return initial, final, records


def audit(initial, final, records, violations=None):
    with patch.object(ipc, "read_trace_records",
                      return_value={"records": records, "violations": violations or []}):
        return ipc.audit_acp_ipc("/synthetic", MODEL_PORT, initial, final)


class TraceParserTests(unittest.TestCase):
    def test_split_connect_preserves_failed_attempt_and_original_lines(self):
        first = ("1000.100000 connect(7<socket:[901]>, " + sockaddr(PORT)
                 + ", 16 <unfinished ...>")
        second = ("1000.110000 <... connect resumed>) = -1 EINPROGRESS "
                  "(Operation now in progress) <0.010000>")
        parsed = parse_memory({"audit-client.11": first + "\n" + second + "\n"})
        self.assertEqual(parsed["violations"], [])
        record, = parsed["records"]
        self.assertEqual(record["tid"], 11)
        self.assertEqual(record["call"], "connect")
        self.assertEqual(ipc.destination_text(record), sockaddr(PORT))
        self.assertIn("EINPROGRESS", record["result"])
        self.assertEqual(record["original_records"],
                         [{"path": "audit-client.11", "line": first},
                          {"path": "audit-client.11", "line": second}])
        self.assertEqual(record["end_ns"], ipc._ns("1000.110000"))

    def test_unfinished_or_orphaned_resume_or_missing_timing_fails_closed(self):
        bad = [
            "1000.100000 connect(7, " + sockaddr(PORT) + ", 16 <unfinished ...>\n",
            "1000.100000 <... connect resumed>) = 0 <0.001000>\n",
            "connect(7, " + sockaddr(PORT) + ", 16) = 0 <0.001000>\n",
            "1000.100000 connect(7, " + sockaddr(PORT) + ", 16) = 0\n",
            "1000.100000 connect(7, " + sockaddr(PORT) + ", 16) = 0 <0.001000>",
        ]
        for raw in bad:
            with self.subTest(raw=raw):
                self.assertTrue(parse_memory({"audit-client.11": raw})["violations"])

    def test_metadata_and_payload_are_not_destinations(self):
        for call in ("bind", "getsockname", "accept"):
            record = syscall(21, "1000.100000",
                             call + "(" + FD + ", " + sockaddr(PORT)
                             + ", [16]) = 0 <0.000001>")
            self.assertIsNone(ipc.destination_text(record))
        payload = r'"payload AF_INET inet_addr(\"8.8.8.8\")"'
        sent = syscall(11, "1000.100000",
                       "sendto(7, " + payload + ", 42, 0, NULL, 0) = 42 <0.000001>")
        self.assertIsNone(ipc.destination_text(sent))

    def test_sendto_and_sendmsg_use_only_explicit_address(self):
        sent = syscall(11, "1000.100000", 'sendto(7, "AF_INET", 7, 0, '
                       + sockaddr(8000) + ", 16) = -1 ECONNREFUSED (refused) <0.000001>")
        self.assertEqual(ipc.destination_text(sent), sockaddr(8000))
        message = syscall(11, "1000.100000", "sendmsg(7, {msg_name=" + sockaddr(1234)
                          + ', msg_namelen=16, msg_iov=[{iov_base="msg_name=NULL", iov_len=13}],'
                          + " msg_iovlen=1, msg_control=NULL, msg_controllen=0, msg_flags=0}, 0)"
                          + " = -1 ECONNREFUSED (refused) <0.000001>")
        self.assertEqual(ipc.destination_text(message), sockaddr(1234))
        with self.assertRaises(ipc.EvidenceError):
            ipc.destination_text({"call": "sendmsg", "args": ["7", "{...}", "0"]})
        with self.assertRaises(ipc.EvidenceError):
            ipc.destination_text({"call": "sendmmsg", "args": []})

    def test_terminal_unknown_service_kill_is_incomplete_and_has_no_destination(self):
        first = "1000.400000 ???( <unfinished ...>"
        last = "1000.400001 +++ killed by SIGKILL +++"
        parsed = parse_memory({"audit-service.9325": first + "\n" + last + "\n"})
        self.assertEqual(parsed["violations"], [])
        record, terminal = parsed["records"]
        self.assertFalse(record["complete"])
        self.assertEqual(record["tid"], 9325)
        self.assertEqual(record["call"], "???")
        self.assertEqual(record["args"], [])
        self.assertEqual(record["result"], "?")
        self.assertIsNone(ipc.destination_text(record))
        self.assertEqual(record["original_records"],
                         [{"path": "audit-service.9325", "line": first}])
        self.assertEqual(terminal["kind"], "exit")
        self.assertEqual(terminal["original_records"],
                         [{"path": "audit-service.9325", "line": last}])

    def test_terminal_named_calls_retain_explicit_destinations_in_both_forms(self):
        address = sockaddr(443, "8.8.8.8")
        prefixes = [
            ("connect(7, " + address + ", 16", address),
            ('sendto(7, "payload", 7, 0, ' + address + ", 16", address),
            ("sendmsg(7, {msg_name=" + address + ", msg_namelen=16, "
             + "msg_iov=[], msg_iovlen=0, msg_control=NULL, msg_controllen=0, msg_flags=0}, 0",
             address),
            ('sendto(7, "payload AF_INET", 15, 0, NULL, 0', None),
            ("sendmsg(7, {msg_name=NULL, msg_namelen=0, msg_iov=[], msg_iovlen=0}, 0", None),
        ]
        for prefix, expected in prefixes:
            for suffix in (" <unfinished ...>", " <unfinished ...>) = ?"):
                for exit_text in ("+++ exited with 0 +++", "+++ killed by SIGKILL +++"):
                    with self.subTest(prefix=prefix, suffix=suffix, exit_text=exit_text):
                        first = "1000.400000 " + prefix + suffix
                        parsed = parse_memory({"audit-client.11":
                                               first + "\n1000.400001 " + exit_text + "\n"})
                        self.assertEqual(parsed["violations"], [])
                        record, terminal = parsed["records"]
                        self.assertFalse(record["complete"])
                        self.assertEqual(ipc.destination_text(record), expected)
                        self.assertEqual(record["original_records"],
                                         [{"path": "audit-client.11", "line": first}])
                        self.assertEqual(terminal["kind"], "exit")

    def test_terminal_unknown_or_interrupted_shapes_fail_closed(self):
        killed = "1000.400002 +++ killed by SIGKILL +++\n"
        named = "1000.400000 connect(7, " + sockaddr(PORT) + ", 16"
        bad = [
            "1000.400000 ???( <unfinished ...>\n1000.400002 +++ exited with 0 +++\n",
            "1000.400000 ???(7 <unfinished ...>\n" + killed,
            "1000.400000 ???( <unfinished ...>) = ?\n" + killed,
            "1000.400000 ???() = ? <0.000001>\n" + killed,
            "1000.400000 ???( <detached ...>\n" + killed,
            named + ") = ?\n" + killed,
            named + " <unfinished ...>) = ?\n",
            named + " <unfinished ...>\n1000.400001 --- SIGTERM {} ---\n" + killed,
            named + " <unfinished ...>) = ?\n1000.400001 --- SIGTERM {} ---\n" + killed,
            named + " <unfinished ...>\n1000.400001 close(9) = 0 <0.000001>\n" + killed,
            named + " <unfinished ...>) = ?\n"
            + "1000.400001 <... connect resumed>) = 0 <0.000001>\n" + killed,
            named + " <unfinished ...>\n" + killed
            + "1000.400003 close(9) = 0 <0.000001>\n",
        ]
        for raw in bad:
            with self.subTest(raw=raw):
                self.assertTrue(parse_memory({"audit-client.11": raw})["violations"])
        self.assertTrue(parse_memory({
            "audit-client.11": named + " <unfinished ...>\n",
            "audit-client.12": killed,
        })["violations"])

    def test_terminal_unrecoverable_destination_fails_closed(self):
        prefixes = [
            "connect(7",
            "connect(7, {sa_family=AF_INET",
            "connect(7, " + sockaddr(PORT),
            'sendto(7, "payload", 7, 0',
            "sendmsg(7, {...}, 0",
            "sendmsg(7, {msg_iov=[]}, 0",
            "sendmmsg(7, [], 0, 0",
            "socketcall(3, 0x1",
        ]
        for prefix in prefixes:
            for suffix in (" <unfinished ...>", " <unfinished ...>) = ?"):
                with self.subTest(prefix=prefix, suffix=suffix):
                    parsed = parse_memory({"audit-client.11":
                                           "1000.400000 " + prefix + suffix
                                           + "\n1000.400001 +++ killed by SIGKILL +++\n"})
                    self.assertTrue(parsed["violations"])

    def test_legacy_c237_gopls_teardown_still_fails_without_required_trace_flags(self):
        parsed = parse_memory({"audit-service.9325":
                               'execve("/scratch/gopls", ["/scratch/gopls"], 0x1) = 0\n'
                               "???( <unfinished ...>\n"
                               "+++ killed by SIGKILL +++\n"})
        self.assertTrue(parsed["violations"])

    def test_terminal_conversion_obeys_record_bound(self):
        with patch.object(ipc, "MAX_TRACE_RECORDS", 1):
            parsed = parse_memory({"audit-service.9325":
                                   "1000.400000 ???( <unfinished ...>\n"
                                   "1000.400001 +++ killed by SIGKILL +++\n"})
        self.assertTrue(parsed["violations"])

    def test_no_return_exit_has_no_duration(self):
        parsed = parse_memory({"audit-client.20":
                               "1000.400000 exit_group(0) = ?\n"
                               "1000.400001 +++ exited with 0 +++\n"})
        self.assertEqual(parsed["violations"], [])
        self.assertEqual(parsed["records"][0]["call"], "exit_group")
        self.assertEqual(parsed["records"][1]["kind"], "exit")

    def test_trace_byte_and_record_limits_fail_closed(self):
        raw = "1000.100000 listen(5<socket:[900]>, 128) = 0 <0.000001>\n"
        with patch.object(ipc, "MAX_TRACE_BYTES", 8):
            self.assertTrue(parse_memory({"audit-client.21": raw})["violations"])
        with patch.object(ipc, "MAX_TRACE_RECORDS", 1):
            self.assertTrue(parse_memory({"audit-client.21": raw + raw})["violations"])


class ListenerCaptureTests(unittest.TestCase):
    def listener(self, extras):
        def row(address, inode):
            return ("0: " + address + ":" + format(PORT, "04X")
                    + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000 17 0 "
                    + str(inode) + "\n")

        tables = {"tcp": "local_address\n" + row("0100007F", 900),
                  "tcp6": "local_address\n"}
        for table, address, inode in extras:
            tables[table] += row(address, inode)
        entries = [SimpleNamespace(name="5", path="/synthetic/fd/5")]
        with patch.object(ipc, "_entries", return_value=entries), \
                patch.object(ipc.os, "readlink", return_value="socket:[900]"), \
                patch.object(ipc.os, "getuid", return_value=17), \
                patch.object(ipc, "_bounded_bytes",
                             side_effect=lambda path, limit: tables[path.name].encode()):
            return ipc._owned_listener(20, MODEL_PORT)

    def test_unique_namespace_listener_is_owned(self):
        self.assertEqual(self.listener([]),
                         {"fd": 5, "inode": 900, "address": "127.0.0.1", "port": PORT})

    def test_other_listener_at_control_port_is_ambiguous_even_if_unowned(self):
        alternatives = [
            ("tcp", "0100007F", 901),
            ("tcp", "00000000", 901),
            ("tcp6", "00000000000000000000000000000000", 901),
            ("tcp", "0100007F", 900),
        ]
        for other in alternatives:
            with self.subTest(other=other), self.assertRaises(ipc.EvidenceError):
                self.listener([other])


class OwnershipProofTests(unittest.TestCase):
    def test_positive_tids_failed_connect_and_exact_entry_scope(self):
        initial, final, records = evidence()
        service = syscall(40, "1000.200000", "connect(7, " + sockaddr(PORT)
                          + ", 16) = 0 <0.000001>", label="service")
        discovery = syscall(11, "1000.210000", "connect(9, " + sockaddr(8000)
                            + ", 16) = -1 ECONNREFUSED (refused) <0.000001>")
        result = audit(initial, final, records + [service, discovery])
        self.assertEqual(result["violations"], [])
        self.assertEqual(result["allowed_connect_records"],
                         records[-2]["original_records"] + records[-1]["original_records"])
        self.assertNotIn(service["original_records"][0], result["allowed_connect_records"])
        self.assertNotIn(discovery["original_records"][0], result["allowed_connect_records"])

    def test_two_snapshots_without_lifecycle_cannot_grant(self):
        initial, final, records = evidence()
        records = [record for record in records if record["call"] not in
                   ("bind", "listen", "getsockname")]
        result = audit(initial, final, records)
        self.assertTrue(result["violations"])
        self.assertEqual(result["allowed_connect_records"], [])

    def test_snapshot_identity_inode_endpoint_or_pin_changes_refuse(self):
        mutations = [
            lambda final: final["server"]["identity"].update(start_ticks=3),
            lambda final: final["listener"].update(inode=901),
            lambda final: final["listener"].update(port=1234),
            lambda final: final["listener"].update(address="0.0.0.0"),
            lambda final: final.update(expected_sha256="0" * 64),
            lambda final: final.update(violations=["capture failed"]),
        ]
        for mutation in mutations:
            initial, final, records = evidence()
            mutation(final)
            result = audit(initial, final, records)
            self.assertTrue(result["violations"])
            self.assertEqual(result["allowed_connect_records"], [])

    def test_missing_thread_creation_or_inode_annotation_refuses(self):
        initial, final, records = evidence()
        missing_thread = [record for record in records
                          if not (record["tid"] == 20 and record["call"] == "clone")]
        self.assertTrue(audit(initial, final, missing_thread)["violations"])
        missing_inode = copy.deepcopy(records)
        next(record for record in missing_inode if record["call"] == "listen")["args"][0] = "5"
        self.assertTrue(audit(initial, final, missing_inode)["violations"])

    def test_listener_lifecycle_must_precede_first_connect(self):
        initial, final, records = evidence()
        listen = next(record for record in records if record["call"] == "listen")
        listen["end_ns"] = ipc._ns("1000.075000")
        self.assertTrue(audit(initial, final, records)["violations"])

    def test_close_replacement_and_exit_from_server_thread_refuse(self):
        operations = [
            "close(" + FD + ") = 0 <0.000001>",
            "close_range(3, ~0U, 0) = 0 <0.000001>",
            "dup2(9<socket:[999]>, " + FD + ") = 5<socket:[999]> <0.000001>",
            "dup(" + FD + ") = 9<socket:[900]> <0.000001>",
            "fcntl(" + FD + ", F_DUPFD_CLOEXEC, 0) = 9<socket:[900]> <0.000001>",
            "fcntl(" + FD + ", F_DUPFD, 0) = 9<socket:[900]> <0.000001>",
            "fcntl(9<socket:[999]>, F_DUPFD, 5) = 5<socket:[999]> <0.000001>",
            "socket(AF_INET, SOCK_STREAM, IPPROTO_TCP) = 5<socket:[999]> <0.000001>",
            "shutdown(" + FD + ", SHUT_RDWR) = 0 <0.000001>",
            "exit_group(0) = ?",
            "unshare(CLONE_FILES) = 0 <0.000001>",
        ]
        for text in operations:
            with self.subTest(text=text):
                initial, final, records = evidence()
                records.append(syscall(21, "1000.150000", text))
                result = audit(initial, final, records)
                self.assertTrue(result["violations"])
                self.assertEqual(result["allowed_connect_records"], [])

    def test_listener_reuseport_before_bind_refuses_even_if_snapshots_are_unique(self):
        for caller in (10, 21):
            for option in ("[1]", "[0]"):
                with self.subTest(caller=caller, option=option):
                    initial, final, records = evidence()
                    records.append(syscall(caller, "1000.037000",
                                           "setsockopt(" + FD + ", SOL_SOCKET, SO_REUSEPORT, "
                                           + option + ", 4) = 0 <0.000001>"))
                    result = audit(initial, final, records)
                    self.assertTrue(result["violations"])
                    self.assertEqual(result["allowed_connect_records"], [])

    def test_failed_listener_reuseport_does_not_enable_another_listener(self):
        initial, final, records = evidence()
        records.append(syscall(21, "1000.037000",
                               "setsockopt(" + FD + ", SOL_SOCKET, SO_REUSEPORT, [1], 4)"
                               + " = -1 ENOPROTOOPT (Protocol not available) <0.000001>"))
        self.assertEqual(audit(initial, final, records)["violations"], [])

    def test_fcntl_hex_flags_do_not_require_a_descriptor_return(self):
        initial, final, records = evidence()
        for command, result in (("F_GETFL", "0x802 (flags O_RDWR|O_NONBLOCK)"),
                                ("F_GETFD", "0x1 (flags FD_CLOEXEC)")):
            records.append(syscall(21, "1000.150000",
                                   "fcntl(" + FD + ", " + command + ") = "
                                   + result + " <0.000001>"))
        result = audit(initial, final, records)
        self.assertEqual(result["violations"], [])
        self.assertEqual(result["allowed_connect_records"],
                         records[8]["original_records"] + records[9]["original_records"])

    def test_server_or_other_descendant_cannot_receive_control_grants(self):
        for tid in (20, 21, 30):
            with self.subTest(tid=tid):
                initial, final, records = evidence()
                if tid == 30:
                    records.append(syscall(10, "1000.100000",
                                           "clone(child_stack=NULL, flags=SIGCHLD) = 30 <0.000001>"))
                records.append(syscall(tid, "1000.150000",
                                       "connect(9, " + sockaddr(PORT) + ", 16) = 0 <0.000001>"))
                result = audit(initial, final, records)
                self.assertTrue(result["violations"])
                self.assertEqual(result["allowed_connect_records"], [])

    def test_incomplete_calls_cannot_supply_exec_birth_listener_or_connect_proof(self):
        for index in (0, 1, 2, 3, 4, 5, 6, 7, 8, 9):
            with self.subTest(index=index):
                initial, final, records = evidence()
                records[index]["complete"] = False
                result = audit(initial, final, records)
                self.assertTrue(result["violations"])
                self.assertEqual(result["allowed_connect_records"], [])

    def test_unknown_clone_identity_still_fails_closed(self):
        initial, final, records = evidence()
        records.append(syscall(20, "1000.150000",
                               "clone(child_stack=NULL, flags=SIGCHLD)"
                               + " = ? ERESTARTNOINTR (To be restarted) <0.000001>"))
        result = audit(initial, final, records)
        self.assertTrue(result["violations"])
        self.assertEqual(result["allowed_connect_records"], [])

    def test_terminal_incomplete_control_connect_never_gets_a_grant(self):
        initial, final, records = evidence()
        parsed = parse_memory({"audit-client.11":
                               "1000.250000 connect(7, " + sockaddr(PORT)
                               + ", 16 <unfinished ...>) = ?\n"
                               "1000.250001 +++ killed by SIGKILL +++\n"})
        self.assertEqual(parsed["violations"], [])
        result = audit(initial, final, records + parsed["records"])
        self.assertTrue(result["violations"])
        self.assertEqual(result["allowed_connect_records"], [])

    def test_terminal_unknown_in_server_interval_fails_but_later_teardown_is_safe(self):
        for stamp, expected_failure in (("1000.150000", True), ("1000.400000", False)):
            with self.subTest(stamp=stamp):
                initial, final, records = evidence()
                parsed = parse_memory({"audit-client.21":
                                       stamp + " ???( <unfinished ...>\n"
                                       + stamp + " +++ killed by SIGKILL +++\n"})
                self.assertEqual(parsed["violations"], [])
                result = audit(initial, final, records + parsed["records"])
                self.assertEqual(bool(result["violations"]), expected_failure)
                if expected_failure:
                    self.assertEqual(result["allowed_connect_records"], [])

    def test_client_and_service_terminal_records_are_both_parsed(self):
        initial, final, records = evidence()
        parsed = parse_memory({
            "audit-client.21": "1000.400000 futex(0x1, 0, 0 <unfinished ...>) = ?\n"
                               "1000.400001 +++ killed by SIGKILL +++\n",
            "audit-service.9325": "1000.400000 ???( <unfinished ...>\n"
                                  "1000.400001 +++ killed by SIGKILL +++\n",
        })
        self.assertEqual(parsed["violations"], [])
        self.assertEqual(len(parsed["records"]), 4)
        result = audit(initial, final, records + parsed["records"])
        self.assertEqual(result["violations"], [])
        self.assertEqual(result["allowed_connect_records"],
                         records[-2]["original_records"] + records[-1]["original_records"])

    def test_split_connect_grants_exactly_both_lines_and_any_service_error_clears_them(self):
        initial, final, records = evidence()
        first = "1000.200000 connect(8<socket:[902]>, " + sockaddr(PORT) + ", 16 <unfinished ...>"
        last = "1000.210000 <... connect resumed>) = 0 <0.010000>"
        files = {"audit-client.11": first + "\n" + last + "\n"}
        parsed = parse_memory(files)
        self.assertEqual(parsed["violations"], [])
        result = audit(initial, final, records[:-1] + parsed["records"])
        self.assertEqual(result["violations"], [])
        self.assertEqual(result["allowed_connect_records"],
                         records[-2]["original_records"]
                         + [{"path": "audit-client.11", "line": first},
                            {"path": "audit-client.11", "line": last}])
        files["audit-service.9325"] = "1000.400000 ???( <unfinished ...>\n"
        parsed = parse_memory(files)
        self.assertTrue(parsed["violations"])
        result = audit(initial, final, records[:-1] + parsed["records"], parsed["violations"])
        self.assertTrue(result["violations"])
        self.assertEqual(result["allowed_connect_records"], [])

    def test_teardown_after_last_connect_does_not_invalidate_interval(self):
        initial, final, records = evidence()
        records.append(syscall(21, "1000.400000", "close(" + FD + ") = 0 <0.000001>"))
        records.append(syscall(21, "1000.410000", "exit_group(0) = ?"))
        self.assertEqual(audit(initial, final, records)["violations"], [])

    def test_error_after_grants_start_accumulating_clears_every_grant(self):
        initial, final, records = evidence()
        records.append(copy.deepcopy(records[-1]))
        result = audit(initial, final, records)
        self.assertTrue(result["violations"])
        self.assertEqual(result["allowed_connect_records"], [])

    def test_late_connect_unknown_client_or_trace_error_clears_all_grants(self):
        initial, final, records = evidence()
        late = syscall(11, "1000.400000", "connect(7, " + sockaddr(PORT)
                       + ", 16) = 0 <0.000001>")
        alien = syscall(99, "1000.200000", "connect(7, " + sockaddr(PORT)
                        + ", 16) = 0 <0.000001>")
        for extra, errors in (([late], []), ([alien], []), ([], ["bad trace"])):
            result = audit(initial, final, records + extra, errors)
            self.assertTrue(result["violations"])
            self.assertEqual(result["allowed_connect_records"], [])


if __name__ == "__main__":
    unittest.main()
