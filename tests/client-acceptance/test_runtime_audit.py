"""Synthetic runtime-audit and pinned-catalog regressions; no writes or processes."""
import copy
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

sys.dont_write_bytecode = True
import ipc_audit as ipc
import run as acceptance


CASE = Path("/synthetic/case")
JOB = Path("/synthetic")
MODEL_PORT = 18001
KERNEL = "{sa_family=AF_NETLINK, nl_pid=0, nl_groups=00000000}"


def ipv4(port, host="127.0.0.1"):
    return ('{sa_family=AF_INET, sin_port=htons(' + str(port)
            + '), sin_addr=inet_addr("' + host + '")}')


def outbound(address, call="connect", complete=True):
    if call == "connect":
        args = "7, " + address + ", 16"
    elif call == "sendto":
        # Payload deliberately contains another family; only sockaddr is audited.
        args = '7, "sa_family=AF_INET payload", 25, 0, ' + address + ", 12"
    else:
        args = '7, {msg_name=' + address + ', msg_namelen=12, msg_iov=[]}, 0'
    text = call + "(" + args + ") = 0 <0.000001>"
    original = {"path": "audit-client.17", "line": "1000.100000 " + text}
    record = ipc._record(17, original["path"], [original], 1000100000000,
                         1000100000000, text, True)
    if not complete:
        # Exercise the parser's incomplete-record contract without owning parser tests.
        record.update(complete=False, result="?", text=call + "(" + args + ") = ?")
        record["original_records"] = [
            {"path": original["path"],
             "line": "1000.100000 " + call + "(" + args + " <unfinished ...>) = ?"},
            {"path": original["path"], "line": "1000.110000 +++ killed by SIGTERM +++"}]
    return record


def audit(records, proof=None, parser_violations=None):
    with patch.object(ipc, "read_trace_records", return_value={
            "records": records, "violations": parser_violations or []}):
        return acceptance.network_audit(CASE, MODEL_PORT, JOB, proof)


def proof_for(record):
    return {"violations": [], "allowed_connect_records": record["original_records"]}


class KernelDestinationTests(unittest.TestCase):
    def test_exact_kernel_sockaddr_is_recorded_without_model_or_ipc_credit(self):
        for call in ("connect", "sendto", "sendmsg"):
            with self.subTest(call=call):
                kernel = outbound(KERNEL, call)
                model = outbound(ipv4(MODEL_PORT))
                report = audit([model, kernel])
                self.assertEqual(report["violations"], [])
                self.assertEqual(len(report["model_connects"]), 1)
                self.assertEqual(report["acp_ipc_records"], [])
                entry, = report["local_kernel_queries"]
                self.assertEqual(entry["destination"], KERNEL)
                self.assertEqual(entry["classification"], "local-kernel-query")
                self.assertEqual(entry["original_records"], kernel["original_records"])
                self.assertTrue(entry["complete"])

    def test_kernel_only_does_not_satisfy_model_connect_requirement(self):
        kernel = outbound(KERNEL, "sendto")
        report = audit([kernel])
        self.assertEqual(report["model_connects"], [])
        self.assertIn("no real model-port connect captured", report["violations"])

    def test_kernel_record_cannot_consume_an_ipc_proof_grant(self):
        kernel = outbound(KERNEL)
        report = audit([outbound(ipv4(MODEL_PORT)), kernel], proof_for(kernel))
        self.assertEqual(len(report["local_kernel_queries"]), 1)
        self.assertEqual(report["acp_ipc_records"], [])
        self.assertIn("ACP IPC proof records do not exactly match audited connect records",
                      report["violations"])

    def test_other_netlink_families_ports_and_ambient_unix_stay_violations(self):
        forbidden = [
            KERNEL.replace("nl_pid=0", "nl_pid=42"),
            KERNEL.replace("00000000", "00000001"),
            KERNEL.replace("AF_NETLINK", "AF_UNSPEC"),
            KERNEL.replace("nl_pid=0", "nl_pid=00"),
            KERNEL[:-1] + ", extra=0}",
            ipv4(1234), ipv4(8000), ipv4(443, "203.0.113.1"),
            '{sa_family=AF_UNIX, sun_path="/var/run/nscd/socket"}',
        ]
        for destination in forbidden:
            with self.subTest(destination=destination):
                report = audit([outbound(ipv4(MODEL_PORT)), outbound(destination, "sendto")])
                self.assertTrue(report["violations"])
                self.assertEqual(report["local_kernel_queries"], [])
                self.assertEqual(report["acp_ipc_records"], [])
                self.assertEqual(len(report["model_connects"]), 1)

    def test_incomplete_calls_retain_destinations_but_do_not_grant_connections(self):
        model = outbound(ipv4(MODEL_PORT), complete=False)
        kernel = outbound(KERNEL, "sendto", complete=False)
        report = audit([model, kernel])
        self.assertEqual(report["model_connects"], [])
        self.assertEqual(report["acp_ipc_records"], [])
        self.assertEqual(report["ip_destinations"][0]["destination"], ipv4(MODEL_PORT))
        self.assertFalse(report["ip_destinations"][0]["complete"])
        self.assertEqual(len(report["local_kernel_queries"]), 1)
        self.assertEqual(report["terminal_incomplete_records"], [model, kernel])
        self.assertIn("no real model-port connect captured", report["violations"])

    def test_incomplete_external_and_acp_destinations_still_fail(self):
        for destination in (ipv4(443, "203.0.113.1"), ipv4(44187)):
            with self.subTest(destination=destination):
                attempt = outbound(destination, complete=False)
                report = audit([outbound(ipv4(MODEL_PORT)), attempt], proof_for(attempt))
                self.assertEqual(report["acp_ipc_records"], [])
                self.assertTrue(any("unexpected IP destination" in v
                                    for v in report["violations"]))
                self.assertEqual(report["ip_destinations"][-1]["destination"], destination)
                self.assertEqual(report["terminal_incomplete_records"], [attempt])

    def test_terminal_unknown_and_parser_errors_grant_nothing(self):
        unknown = {"kind": "terminal-unknown", "complete": False,
                   "original_records": [{"path": "audit-client.17",
                                         "line": "1000.100000 ???(<unfinished ...>"}]}
        clean = audit([outbound(ipv4(MODEL_PORT)), unknown])
        self.assertEqual(clean["violations"], [])
        self.assertEqual(clean["terminal_incomplete_records"], [unknown])
        self.assertEqual(len(clean["model_connects"]), 1)
        self.assertEqual(clean["acp_ipc_records"], [])
        report = audit([unknown], parser_violations=["malformed terminal position"])
        self.assertEqual(report["terminal_incomplete_records"], [unknown])
        self.assertEqual(report["ip_destinations"], [])
        self.assertEqual(report["local_kernel_queries"], [])
        self.assertEqual(report["acp_ipc_records"], [])
        self.assertEqual(report["model_connects"], [])
        self.assertIn("malformed terminal position", report["violations"])


class ExactUnixDestinationTests(unittest.TestCase):
    def unix(self, destination, role="client", call="connect"):
        record = outbound('{sa_family=AF_UNIX, sun_path="'
                          + destination + '"}', call)
        record["path"] = "audit-" + role + ".17"
        for original in record["original_records"]:
            original["path"] = record["path"]
        return record

    def test_only_exact_case_sockets_with_expected_trace_roles_are_allowed(self):
        for name, roles in (("control.sock", ("client", "service")), ("h.sock", ("client",))):
            for role in roles:
                with self.subTest(name=name, role=role):
                    attempt = self.unix(str(CASE / name), role)
                    report = audit([outbound(ipv4(MODEL_PORT)), attempt])
                    self.assertEqual(report["violations"], [])
                    entry, = report["unix_destinations"]
                    self.assertEqual(entry["resolved_destination"], str(CASE / name))
                    self.assertEqual(entry["trace_role"], role)
                    self.assertEqual(entry["original_records"], attempt["original_records"])
                    self.assertEqual(len(report["model_connects"]), 1)
                    self.assertEqual(report["acp_ipc_records"], [])
                    self.assertEqual(report["unix_allowed_sockets"], {
                        str(CASE / "control.sock"): ("client", "service"),
                        str(CASE / "h.sock"): ("client",),
                    })

    def test_job_siblings_stale_temporary_external_and_noncanonical_paths_fail(self):
        for destination in (
                "/synthetic/other/control.sock", "/synthetic/other/h.sock",
                "/synthetic/control.sock", str(CASE / "stale.sock"),
                str(CASE / "h.sock.old"), str(CASE / "tmp/control.sock"),
                str(CASE / "unrelated"), "/var/run/nscd/socket", "control.sock",
                str(CASE) + "/../case/h.sock", str(CASE) + "//control.sock",
                str(CASE) + "/./h.sock"):
            with self.subTest(destination=destination):
                report = audit([outbound(ipv4(MODEL_PORT)), self.unix(destination)])
                self.assertTrue(report["violations"])
                self.assertEqual(len(report["unix_destinations"]), 1)
                self.assertEqual(report["acp_ipc_records"], [])

    def test_wrong_trace_roles_and_outbound_shapes_cannot_use_private_sockets(self):
        for role, name, call in (
                ("service", "h.sock", "connect"), ("unknown", "control.sock", "connect"),
                ("client", "h.sock", "sendto"), ("service", "control.sock", "sendmsg")):
            with self.subTest(role=role, name=name, call=call):
                attempt = self.unix(str(CASE / name), role, call)
                report = audit([outbound(ipv4(MODEL_PORT)), attempt])
                self.assertTrue(report["violations"])
        for address in (
                "{sa_family=AF_UNIX}", '{sa_family=AF_UNIX, sun_path=@"control.sock"}',
                '{sa_family=AF_UNIX, sun_path="/synthetic/case/control.sock", extra=1}',
                '{sa_family=AF_UNIX, sun_path=0x1234}'):
            with self.subTest(address=address):
                self.assertTrue(audit([outbound(ipv4(MODEL_PORT)), outbound(address)])["violations"])

    def test_symlink_alias_and_retargeted_exact_path_fail_without_disk_writes(self):
        resolve = Path.resolve
        endpoint = CASE / "control.sock"
        alias = CASE / "alias.sock"
        def resolve_alias(path, *args, **kwargs):
            return endpoint if path == alias else resolve(path, *args, **kwargs)
        def resolve_retargeted(path, *args, **kwargs):
            return JOB / "other/control.sock" if path == endpoint else resolve(path, *args, **kwargs)
        for destination, resolver in ((alias, resolve_alias), (endpoint, resolve_retargeted)):
            with self.subTest(destination=destination), patch.object(Path, "resolve", resolver):
                report = audit([outbound(ipv4(MODEL_PORT)), self.unix(str(destination))])
                self.assertTrue(report["violations"])

    def test_unix_connect_never_supplies_model_credit_or_consumes_acp_grants(self):
        attempt = self.unix(str(CASE / "control.sock"))
        report = audit([attempt], proof_for(attempt))
        self.assertEqual(report["model_connects"], [])
        self.assertEqual(report["acp_ipc_records"], [])
        self.assertIn("no real model-port connect captured", report["violations"])
        self.assertIn("ACP IPC proof records do not exactly match audited connect records",
                      report["violations"])


class CodexCatalogTests(unittest.TestCase):
    def test_actual_committed_entry_generates_only_the_two_authorized_overrides(self):
        raw = (acceptance.HERE / "codex-model-entry.json").read_bytes()
        writes = {}
        def save(path, data):
            writes[str(path)] = data
            return len(data)

        with patch.object(Path, "write_bytes", save), \
                patch.object(acceptance, "file_hash",
                             side_effect=lambda path: acceptance.digest(writes[str(path)])), \
                patch.object(acceptance, "write_json") as write_json:
            provenance = acceptance.codex_direct_catalog(CASE)

        self.assertEqual(len(raw), 66833)
        self.assertFalse(raw.endswith(b"\n"))
        self.assertEqual(provenance["source_entry_sha256"],
                         "873c956b8ef4766fb4e059d25924ee2c672e29149c99e1fa936e89e83410c518")
        original = acceptance.json.loads(raw)
        expected = copy.deepcopy(original)
        expected.update(tool_mode="direct", supports_search_tool=False)
        catalog = writes[str(CASE / "codex/model-catalog.json")]
        self.assertEqual(acceptance.json.loads(catalog), {"models": [expected]})
        self.assertEqual(provenance["catalog_sha256"],
                         "2bd3950c9f255abedd4cf7bdfb31e5091d80b4034db100a3c1e8e2eb50275558")
        self.assertEqual(provenance["source_copy_sha256"],
                         "943ca7fe1d19ed019054158303f0dbbd80b3bef43b3aa425a71aa7cb3fb52c2b")
        self.assertEqual(provenance["source_entry_bytes"], len(raw))
        self.assertIn("upstream-raw-not-independently-verified", provenance["source_verification"])
        write_json.assert_called_once_with(CASE / "codex-model-catalog-provenance.json", provenance)

    def test_changed_source_entry_fails_before_any_catalog_write(self):
        raw = (acceptance.HERE / "codex-model-entry.json").read_bytes()
        with patch.object(Path, "read_bytes", return_value=raw + b"\n"), \
                patch.object(Path, "write_bytes") as write_bytes, \
                patch.object(acceptance, "write_json") as write_json:
            with self.assertRaisesRegex(AssertionError, "pinned Codex source model entry changed"):
                acceptance.codex_direct_catalog(CASE)
        write_bytes.assert_not_called()
        write_json.assert_not_called()


if __name__ == "__main__":
    unittest.main()
