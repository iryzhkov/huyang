"""Pure witness-retention regressions; all file IO is mocked, no native client."""
import copy
import json
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

sys.dont_write_bytecode = True
import opencode


PREFIX = '{"kind":"setup","version":"2.0.30"}\n'
INVALID = {
    "malformed": PREFIX + "not JSON\n",
    "truncated": PREFIX + '{"kind":"readiness-failure"',
    "missing": FileNotFoundError("synthetic missing witness"),
    "empty": "",
    "non-object": PREFIX + "[]\n",
}


class WitnessEvidenceTests(unittest.TestCase):
    def fixtures(self):
        owner = SimpleNamespace(case=Path("/synthetic/opencode"))
        acp = SimpleNamespace(notifications=[{"session": "a"}], selections=[], failures=[])
        initial = {"violations": [], "phase": "initial"}
        final = {"violations": ["existing terminal IPC failure"], "phase": "final"}
        return owner, acp, initial, final

    def retain(self, raw, owner, acp, initial, final, reports, original=None):
        # Bytes stay in this immutable fixture; the only writes are captured reports.
        def read(path, *args, **kwargs):
            self.assertEqual(path, owner.case / "plugin-witness.jsonl")
            if isinstance(raw, Exception):
                raise raw
            return raw

        def write(path, value):
            reports[path.name] = copy.deepcopy(value)

        with patch.object(Path, "read_text", read), \
                patch.object(Path, "write_text", side_effect=AssertionError("raw witness write")), \
                patch.object(opencode, "write_json", side_effect=write):
            opencode.retain_final_evidence(owner, acp, ["a", "b"], initial, final, original)

    def assert_retained(self, owner, acp, initial, final, reports, raw):
        self.assertEqual(list(reports), ["acp-ipc-final.json", "acp-report.json"])
        self.assertEqual(reports["acp-ipc-final.json"], final)
        self.assertEqual(owner.acp_ipc_captures, (initial, final))
        report = reports["acp-report.json"]
        self.assertEqual(report["sessions"], ["a", "b"])
        self.assertEqual(report["notifications"], acp.notifications)
        self.assertEqual(report["failures"], acp.failures)
        self.assertTrue(report["witness_parse_error"]["error"])
        self.assertTrue(acp.failures, "unusable witness must fail acceptance")
        self.assertIn("witness parse failed:", acp.failures[-1])
        expected_prefix = [json.loads(PREFIX)] if isinstance(raw, str) and raw.startswith(PREFIX) else []
        self.assertEqual(report["witness"], expected_prefix)
        expected_line = 2 if expected_prefix else None
        self.assertEqual(report["witness_parse_error"]["line"], expected_line)

    def test_unusable_witness_retains_both_reports_then_fails_acceptance(self):
        for name, raw in INVALID.items():
            with self.subTest(name=name):
                owner, acp, initial, final = self.fixtures()
                reports = {}
                with self.assertRaisesRegex(AssertionError, "witness parse failed:"):
                    self.retain(raw, owner, acp, initial, final, reports)
                self.assert_retained(owner, acp, initial, final, reports, raw)
                self.assertIsNone(reports["acp-report.json"]["original_failure"])

    def test_unusable_witness_does_not_replace_an_original_failure(self):
        for name, raw in INVALID.items():
            with self.subTest(name=name):
                owner, acp, initial, final = self.fixtures()
                reports = {}
                original = RuntimeError("original primary inference failure")
                with self.assertRaises(RuntimeError) as caught:
                    try:
                        raise original
                    finally:
                        self.retain(raw, owner, acp, initial, final, reports, sys.exception())
                self.assertIs(caught.exception, original)
                self.assert_retained(owner, acp, initial, final, reports, raw)
                self.assertEqual(reports["acp-report.json"]["original_failure"], repr(original))

    def test_valid_witness_is_retained_without_a_false_parse_failure(self):
        owner, acp, initial, final = self.fixtures()
        reports = {}
        raw = PREFIX + '{"kind":"readiness-ready","sessionID":"a"}\n'
        self.retain(raw, owner, acp, initial, final, reports)
        self.assertEqual(list(reports), ["acp-ipc-final.json", "acp-report.json"])
        self.assertEqual(reports["acp-ipc-final.json"], final)
        self.assertEqual(reports["acp-report.json"]["witness"],
                         [json.loads(line) for line in raw.splitlines()])
        self.assertIsNone(reports["acp-report.json"]["witness_parse_error"])
        self.assertEqual(acp.failures, [])


if __name__ == "__main__":
    unittest.main()
