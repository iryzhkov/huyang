"""Synthetic checks only; real client acceptance still requires a fresh complete job."""
import base64
import copy
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

from common import FULL, canonical, digest
from model_server import Controller, wire_evidence
from reply_presentation import CLAUDE_SHA, CODEX_SHA, present_reply
from workflow import Workflow, results

GUARD = "docrev_" + "a" * 64
CURRENT = "docrev_" + "b" * 64
REFUSAL = (
    "document_content_changed: document_content_changed: main.go changed from " + GUARD + " to " + CURRENT
    + '\nworkspace revision=wsrev_2\nnext: edit_apply path="main.go" revision_id="' + CURRENT
    + '" (retry_with_the_current_revision_id); edit_apply path="main.go"'
    + " (retry_as_replace_literal_with_the_old_text)\n")
HTTP = {"method": "POST", "target": "/v1/responses", "sha256": "synthetic-http", "bytes": 100}


def case(client="codex"):
    codex = client == "codex"
    protocol = "responses" if codex else "anthropic"
    call = {"call_id": "call_fixture_1", "base": "workspace_open" if codex else "edit_apply",
            "name": "workspace_open" if codex else "mcp__huyang__edit_apply",
            "namespace": "mcp__huyang" if codex else None, "profile": "full",
            "step": "open" if codex else "stale", "current_revision": CURRENT,
            "arguments": {"root": "/synthetic/fixture"}, "model_item_id": "fc_1"}
    if not codex:
        call["arguments"]["operation"] = {
            "kind": "create_file", "replace": True, "path": "main.go", "revision_id": GUARD}
    text = "Workspace id=ws_abc revision=wsrev_1\névidence\n" if codex else REFUSAL
    meta = {"callId": call["call_id"], "itemId": "fc_1"} if codex else {
        "claudecode/toolUseId": call["call_id"]}
    params = {"name": call["base"], "arguments": copy.deepcopy(call["arguments"]), "_meta": meta}
    result: dict = {"content": [{"type": "text", "text": text}]}
    if not codex:
        result["isError"] = True
    receipt = {"params": params, "result": result, "connection": 41, "request_id": 2,
               "request_connection": 41, "response_connection": 41, "profile": "full",
               "request": {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": params},
               "response": {"jsonrpc": "2.0", "id": 2, "result": result}, "matched": False}
    if codex:
        output = {"type": "function_call_output", "call_id": call["call_id"], "id": "fco_different",
                  "output": [{"type": "input_text", "text": "Wall time: 0.3234 seconds\nOutput:"},
                             {"type": "input_text", "text": text}]}
        origin = {"type": "function_call", "call_id": call["call_id"], "id": "fc_1",
                  "name": call["name"], "namespace": call["namespace"],
                  "arguments": canonical(call["arguments"])}
        body = {"model": "gpt-6.1-sol", "input": [origin, output]}
    else:
        output = {"type": "tool_result", "tool_use_id": call["call_id"], "is_error": True,
                  "content": text[:-1], "cache_control": {"type": "ephemeral"}}
        origin = {"type": "tool_use", "id": call["call_id"],
                  "name": call["name"], "input": copy.deepcopy(call["arguments"])}
        body = {"messages": [{"role": "assistant", "content": [origin]},
                             {"role": "user", "content": [output]}]}
    manifest = {"client": client, "binary": {
        "version": "codex-cli 0.163.0" if codex else "2.1.301 (Claude Code)",
        "sha256": CODEX_SHA if codex else CLAUDE_SHA}}
    if codex:
        manifest["codex_model_catalog"] = {
            "coverage": "explicit-direct-tools-only", "model_slug": "gpt-6.1-sol",
            "source_entry_sha256": "873c956b8ef4766fb4e059d25924ee2c672e29149c99e1fa936e89e83410c518",
            "overrides": {"tool_mode": "direct", "supports_search_tool": False}}
    return [manifest, protocol, call, output, body, receipt]


class PresentationTests(unittest.TestCase):
    def rejected(self, fixture):
        with self.assertRaises((AssertionError, ValueError, TypeError, KeyError)):
            present_reply(*fixture)

    def test_codex_lossless_typed_payload_and_separate_ids(self):
        f = case()
        before = copy.deepcopy(f)
        visible, evidence = present_reply(*f)
        self.assertEqual(f, before)
        self.assertEqual(visible.encode(), f[-1]["result"]["content"][0]["text"].encode())
        self.assertTrue(visible.endswith("\n"))
        self.assertEqual(evidence["lost_bytes"], 0)
        self.assertEqual(evidence["raw_typed_output"], f[3])
        self.assertEqual(evidence["metadata"]["header"], f[3]["output"][0]["text"])
        self.assertEqual(evidence["wire_text"], evidence["visible_payload"])
        self.assertEqual(evidence["binding"]["model_item_id"], "fc_1")
        self.assertEqual(evidence["binding"]["output_item_id"], "fco_different")

    def test_service_text_that_looks_like_timing_metadata_is_untouched(self):
        f = case()
        text = "Wall time: 99.9999 seconds\nOutput:\nguard=docrev_original\n"
        f[-1]["result"]["content"][0]["text"] = text
        f[3]["output"][1]["text"] = text
        visible, _ = present_reply(*f)
        self.assertEqual(visible, text)

    def test_codex_error_flag_independent_of_timing_header(self):
        f = case()
        f[-1]["result"]["isError"] = True
        _, evidence = present_reply(*f)
        self.assertIs(evidence["mcp_isError"], True)

    def test_codex_header_grammar(self):
        for header in ("Wall time: 0.0000 seconds\nOutput:", "Wall time: 123.1234 seconds\nOutput:"):
            f = case()
            f[3]["output"][0]["text"] = header
            present_reply(*f)
        for header in ("Wall time: -0.1234 seconds\nOutput:", "Wall time: +0.1234 seconds\nOutput:",
                       "Wall time: 0.123 seconds\nOutput:", "Wall time: 0.12345 seconds\nOutput:",
                       "Wall time: 1e3 seconds\nOutput:", "Wall time: NaN seconds\nOutput:",
                       "Wall time: 0.1234 seconds\r\nOutput:", "Wall time: 0.1234 seconds\nOutput:\n",
                       "prefix\nWall time: 0.1234 seconds\nOutput:", "Wall time: ٠.1234 seconds\nOutput:",
                       "Wall time: 0.1234 seconds\nextra\nOutput:", "Wall time: .1234 seconds\nOutput:"):
            with self.subTest(header=header):
                f = case()
                f[3]["output"][0]["text"] = header
                self.rejected(f)

    def test_codex_shape_and_payload_negatives(self):
        mutations = [
            lambda o: o["output"].reverse(),
            lambda o: o["output"].append({"type": "input_text", "text": ""}),
            lambda o: o["output"].pop(),
            lambda o: o["output"][0].update(extra="unknown"),
            lambda o: o["output"][1].update(extra="unknown"),
            lambda o: o["output"][1].update(type="text"),
            lambda o: o["output"][0].update(type="output_text"),
            lambda o: o["output"][1].update(text=42),
            lambda o: o.update(output="flattened header and text"),
            lambda o: o.update(extra="unknown"),
            lambda o: o.update(id=42),
            lambda o: o.update(call_id="another"),
            lambda o: o["output"][1].update(text=o["output"][1]["text"][:-1]),
            lambda o: o["output"][1].update(text="... output truncated ..."),
            lambda o: o["output"][1].update(text=""),
            lambda o: o["output"][1].update(text=o["output"][1]["text"].replace("wsrev_1", "wsrev_2")),
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutations.index(mutation)):
                f = case()
                mutation(f[3])
                self.rejected(f)

    def test_client_pin_api_and_codex_mode_negatives(self):
        for client in ("codex", "claude"):
            for key, value in (("client", "opencode"), ("version", "other"), ("sha256", "other"),
                               ("protocol", "chat")):
                with self.subTest(client=client, key=key):
                    f = case(client)
                    if key == "client":
                        f[0][key] = value
                    elif key == "protocol":
                        f[1] = value
                    else:
                        f[0]["binary"][key] = value
                    self.rejected(f)
        for key, value in (("coverage", "default-code-mode"), ("model_slug", "other"),
                           ("source_entry_sha256", "other"),
                           ("overrides", {"tool_mode": "code_mode_only", "supports_search_tool": True})):
            f = case()
            f[0]["codex_model_catalog"][key] = value
            self.rejected(f)
        f = case()
        f[4]["model"] = "other"
        self.rejected(f)

    def test_same_call_binding_negatives_for_both_clients(self):
        for client in ("codex", "claude"):
            mutations = [
                lambda f: f[-1].update(matched=True),
                lambda f: f[-1].update(connection=None),
                lambda f: f[-1].update(connection=99),
                lambda f: f[-1].update(request_connection=99),
                lambda f: f[-1].update(response_connection=99),
                lambda f: f[-1].update(profile="lean"),
                lambda f: f[-1]["response"].update(id=3),
                lambda f: f[-1]["request"].update(id=3),
                lambda f: f[-1]["request"].update(id=True),
                lambda f: f[-1]["response"].update(error={}),
                lambda f: f[-1]["params"].update(name="read"),
                lambda f: f[-1]["params"]["arguments"].update(root="/foreign"),
                lambda f: f[-1]["params"].update(_meta={}),
                lambda f: f[2].update(call_id="another_call_same_args"),
                lambda f: f[-1]["result"].update(structuredContent={}),
                lambda f: f[-1]["result"].update(isError="true"),
                lambda f: f[-1]["result"]["content"].append({"type": "text", "text": ""}),
            ]
            for index, mutation in enumerate(mutations):
                with self.subTest(client=client, mutation=index):
                    f = case(client)
                    mutation(f)
                    self.rejected(f)
        for field in ("name", "namespace", "arguments", "id"):
            f = case()
            f[4]["input"][0][field] = "changed"
            self.rejected(f)
        f = case()
        f[-1]["params"]["_meta"]["itemId"] = "other"
        self.rejected(f)
        for client in ("codex", "claude"):
            f = case(client)
            origins = f[4]["input"] if client == "codex" else f[4]["messages"][0]["content"]
            origins.append(copy.deepcopy(origins[0]))
            self.rejected(f)

    def test_claude_exact_first_and_visible_one_lf_loss(self):
        f = case("claude")
        original = copy.deepcopy(f)
        visible, evidence = present_reply(*f)
        self.assertEqual(f, original)
        self.assertEqual(visible, REFUSAL[:-1])
        self.assertEqual(evidence["lost_bytes"], 1)
        self.assertNotEqual(evidence["wire_text"], evidence["visible_payload"])
        self.assertEqual(evidence["raw_typed_output"]["content"], visible)
        f[3]["content"] = REFUSAL
        _, evidence = present_reply(*f)
        self.assertEqual(evidence["mapping"], "exact-text")
        self.assertEqual(evidence["lost_bytes"], 0)

    def test_claude_exact_success_with_absent_error_flags(self):
        f = case("claude")
        f[2]["step"] = "open"
        f[-1]["result"].pop("isError")
        f[-1]["result"]["content"][0]["text"] = "success\n"
        f[3].pop("is_error")
        f[3]["content"] = "success\n"
        visible, evidence = present_reply(*f)
        self.assertEqual(visible, "success\n")
        self.assertEqual(evidence["mapping"], "exact-text")

    def test_claude_observed_success_array_is_exact_and_not_a_lossy_mapping(self):
        f = case("claude")
        f[-1]["result"].pop("isError")
        f[-1]["result"]["content"][0]["text"] = "success\n"
        f[3].pop("is_error")
        f[3]["content"] = [{"type": "text", "text": "success\n"}]
        visible, evidence = present_reply(*f)
        self.assertEqual(visible, "success\n")
        self.assertEqual(evidence["mapping"], "exact-text")
        self.assertEqual(evidence["raw_typed_output"]["content"], f[3]["content"])
        f[3]["content"][0]["text"] = "success"
        self.rejected(f)
        f[3]["content"] = [{"type": "text", "text": "success\n"}, {"type": "text", "text": ""}]
        self.rejected(f)

    def test_claude_refusal_gate_negatives(self):
        mutations = [
            lambda f: f[2].update(step="edit"),
            lambda f: f[2].update(base="read"),
            lambda f: f[-1]["result"].pop("isError"),
            lambda f: f[-1]["result"].update(isError=False),
            lambda f: f[3].pop("is_error"),
            lambda f: f[3].update(is_error=False),
            lambda f: f[3].update(content=[{"type": "text", "text": REFUSAL[:-1]}]),
            lambda f: f[3].update(tool_use_id="other"),
            lambda f: f[2]["arguments"]["operation"].update(path="other.go"),
            lambda f: f[2].update(current_revision=GUARD),
            lambda f: f[4]["messages"][0]["content"][0].update(name="other"),
            lambda f: f[4]["messages"][0]["content"][0].update(input={}),
        ]
        for index, mutation in enumerate(mutations):
            with self.subTest(mutation=index):
                f = case("claude")
                mutation(f)
                self.rejected(f)

    def test_claude_whitespace_guards_and_success_remain_strict(self):
        for text, visible in (
            (REFUSAL, REFUSAL[:-2]),
            (REFUSAL + "\n", REFUSAL[:-1]),
            (REFUSAL + "\n", REFUSAL),
            (REFUSAL[:-1] + "\r\n", REFUSAL[:-1] + "\r"),
            (REFUSAL, REFUSAL[:-1].replace("workspace revision", "workspace  revision")),
            (REFUSAL, REFUSAL[:-1].replace(GUARD, CURRENT)),
            (REFUSAL, REFUSAL[:-1].replace("next:", "")),
            (REFUSAL, "\n".join(reversed(REFUSAL[:-1].split("\n")))),
            ("other_refusal\n", "other_refusal"),
        ):
            f = case("claude")
            f[-1]["result"]["content"][0]["text"] = text
            f[3]["content"] = visible
            self.rejected(f)
        f = case("claude")
        f[-1]["result"]["isError"] = False
        f[-1]["result"]["content"][0]["text"] = "success\n"
        f[3]["is_error"] = False
        f[3]["content"] = "success"
        self.rejected(f)

    def test_duplicate_native_outputs_rejected_before_dictionary_overwrite(self):
        for client in ("codex", "claude"):
            f = case(client)
            container = f[4]["input"] if client == "codex" else f[4]["messages"][1]["content"]
            container.append(copy.deepcopy(f[3]))
            with self.assertRaisesRegex(AssertionError, "duplicate"):
                results(f[4], f[1])


class ControllerTests(unittest.TestCase):
    @staticmethod
    def opencode_case(text=REFUSAL):
        manifest = {"client": "opencode", "binary": {"version": "opencode v2.0.30",
                    "sha256": "3eb9af9b4ab07d4de85b68a2678a1e17534f9cfa833ade80b80831e51972da72"}}
        call = {"call_id": "call_oc_stale", "name": "huyang_full_edit_apply", "base": "edit_apply",
                "arguments": {"root": "/synthetic/fixture", "operation": {"path": "main.go"}},
                "profile": "full", "step": "stale"}
        result = {"content": [{"type": "text", "text": text}], "isError": True}
        params = {"name": call["base"], "arguments": copy.deepcopy(call["arguments"])}
        receipt = {"params": params, "result": result, "profile": "full", "matched": False,
                   "connection": 41, "request_id": 2,
                   "request": {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": params},
                   "response": {"jsonrpc": "2.0", "id": 2, "result": result}}
        return [manifest, call, receipt]

    def chat_match(self, fixture, visible, extra_receipts=()):
        manifest, call, receipt = fixture
        owner = Controller("/synthetic", {})
        owner.receipts = [receipt, *extra_receipts]
        body = {"messages": [{"role": "tool", "tool_call_id": call["call_id"], "content": visible}]}
        original_body, original_result = copy.deepcopy(body), copy.deepcopy(receipt["result"])
        with patch.object(Path, "read_text", return_value=json.dumps(manifest)):
            matched = owner.match_reply(call, results(body, "chat")[call["call_id"]], "chat", body)
        self.assertEqual(body, original_body)
        self.assertEqual(receipt["result"], original_result)
        return matched

    def test_opencode_stale_envelope_preserves_raw_values_and_maps_one_lf(self):
        fixture = self.opencode_case()
        visible = json.dumps({"error": {"type": "tool.execution", "message": REFUSAL[:-1]}, "content": []})
        matched = self.chat_match(fixture, visible)
        self.assertTrue(matched["matched"])
        self.assertEqual(matched["model_visible_mapping"], "opencode-tool.execution-single-final-newline")
        self.assertEqual(matched["model_visible_plain_text"], REFUSAL[:-1])
        self.assertEqual(matched["model_visible_text"], visible)
        self.assertEqual(matched["result"]["content"][0]["text"], REFUSAL)

    def test_opencode_exact_first_including_stale_error_and_success(self):
        for error, step in ((True, "stale"), (False, "reread"), (None, "read")):
            with self.subTest(error=error, step=step):
                fixture = self.opencode_case()
                fixture[1]["step"] = step
                if error is None:
                    fixture[2]["result"].pop("isError")
                else:
                    fixture[2]["result"]["isError"] = error
                matched = self.chat_match(fixture, REFUSAL)
                self.assertTrue(matched["matched"])
                self.assertEqual(matched["model_visible_mapping"], "exact-text")
                self.assertEqual(matched["model_visible_plain_text"], REFUSAL)
                self.assertEqual(matched["model_visible_text"], REFUSAL)
        fixture = self.opencode_case("success\n")
        fixture[1]["step"] = "read"
        fixture[2]["result"]["isError"] = False
        with self.assertRaises(AssertionError):
            self.chat_match(fixture, "success")
        self.assertFalse(fixture[2]["matched"])

    def test_opencode_envelope_rejects_whitespace_and_error_gate_changes(self):
        for label, text, message, step, error in (
            ("crlf", REFUSAL[:-1] + "\r\n", REFUSAL[:-1] + "\r", "stale", True),
            ("double-terminal-lf", REFUSAL + "\n", REFUSAL, "stale", True),
            ("two-lfs-removed", REFUSAL + "\n", REFUSAL[:-1], "stale", True),
            ("two-bytes-removed", REFUSAL, REFUSAL[:-2], "stale", True),
            ("non-stale", REFUSAL, REFUSAL[:-1], "edit", True),
            ("absent-error", REFUSAL, REFUSAL[:-1], "stale", None),
            ("false-error", REFUSAL, REFUSAL[:-1], "stale", False),
            ("success-lf-removed", "success\n", "success", "read", False),
        ):
            with self.subTest(label=label):
                fixture = self.opencode_case(text)
                fixture[1]["step"] = step
                if error is None:
                    fixture[2]["result"].pop("isError")
                else:
                    fixture[2]["result"]["isError"] = error
                visible = json.dumps({"error": {"type": "tool.execution", "message": message}, "content": []})
                with self.assertRaises(AssertionError):
                    self.chat_match(fixture, visible)
                self.assertFalse(fixture[2]["matched"])

    def test_opencode_envelope_rejects_unknown_keys_type_or_nonempty_content(self):
        changes = (
            ("extra-envelope-key", lambda e: e.update(extra="unknown")),
            ("extra-error-key", lambda e: e["error"].update(extra="unknown")),
            ("wrong-error-type", lambda e: e["error"].update(type="other")),
            ("nonempty-content", lambda e: e.update(content=[{"type": "text", "text": "unexpected"}])),
        )
        for label, change in changes:
            with self.subTest(label=label):
                fixture = self.opencode_case()
                envelope = {"error": {"type": "tool.execution", "message": REFUSAL[:-1]}, "content": []}
                change(envelope)
                with self.assertRaises(AssertionError):
                    self.chat_match(fixture, json.dumps(envelope))
                self.assertFalse(fixture[2]["matched"])

    def test_opencode_chat_rejects_wrong_pin_and_ambiguous_receipts(self):
        for field, wrong in (("client", "claude"), ("version", "opencode v2.0.31"), ("sha256", "wrong")):
            with self.subTest(field=field):
                fixture = self.opencode_case()
                if field == "client":
                    fixture[0][field] = wrong
                else:
                    fixture[0]["binary"][field] = wrong
                with self.assertRaisesRegex(AssertionError, "unsupported native client/API"):
                    self.chat_match(fixture, REFUSAL)
                self.assertFalse(fixture[2]["matched"])
        fixture = self.opencode_case()
        duplicate = copy.deepcopy(fixture[2])
        duplicate["connection"], duplicate["request_id"] = 42, 3
        with self.assertRaisesRegex(AssertionError, "ambiguous"):
            self.chat_match(fixture, REFUSAL, [duplicate])
        self.assertFalse(fixture[2]["matched"])
        self.assertFalse(duplicate["matched"])

    def test_claude_match_reply_uses_original_recorded_frames_and_api(self):
        # This test may write only inside the disposable Chaldea HOME.
        self.assertEqual(os.environ.get("HOME"), "/home/job", "requires disposable scratch HOME")
        home = Path("/home/job")
        self.assertEqual(home.resolve(), home)
        self.assertTrue(home.is_dir())
        f = case("claude")
        request = (json.dumps(f[-1]["request"], ensure_ascii=False) + "\n").encode()
        response = (json.dumps(f[-1]["response"], ensure_ascii=False) + "\n").encode()
        rows = []
        for direction, chunk in (
            ("to-candidate", request[:17]), ("to-candidate", request[17:]),
            ("to-client", response[:23]), ("to-client", response[23:]),
        ):
            rows.append({"pid": 41, "direction": direction,
                         "bytes_b64": base64.b64encode(chunk).decode(), "sha256": digest(chunk)})
        recording = ("\n".join(json.dumps(row) for row in rows) + "\n").encode()
        raw_http = json.dumps(f[4], ensure_ascii=False).encode()
        evidence = {"method": "POST", "target": "/v1/messages?beta=true",
                    "sha256": digest(raw_http), "bytes": len(raw_http)}
        with tempfile.TemporaryDirectory(prefix="reply-presentation-", dir=home) as directory:
            directory = Path(directory)
            manifest_path, wire_path = directory / "case-manifest.json", directory / "mcp-wire.jsonl"
            manifest_path.write_text(json.dumps(f[0]))
            wire_path.write_bytes(recording)
            owner = Controller(directory, {})
            owner.receipts = [f[-1]]
            for wrong in ({**evidence, "method": "GET"}, {**evidence, "target": "/v1/responses"}):
                with self.assertRaisesRegex(AssertionError, "wrong native HTTP API"):
                    owner.match_reply(f[2], f[3], f[1], f[4], wrong)
                self.assertFalse(f[-1]["matched"])
            before = copy.deepcopy(f)
            receipt = owner.match_reply(f[2], results(f[4], f[1])[f[2]["call_id"]], f[1], f[4], evidence)
            presentation = receipt["presentation"]
            self.assertEqual(presentation["mapping"], "claude-messages-stale-error-single-final-newline")
            self.assertEqual(presentation["wire_request"]["sha256"], digest(request))
            self.assertEqual(presentation["wire_request"]["bytes"], len(request))
            self.assertEqual(presentation["wire_response"]["sha256"], digest(response))
            self.assertEqual(presentation["wire_response"]["bytes"], len(response))
            self.assertEqual(presentation["wire_request"]["connection"], 41)
            self.assertEqual(presentation["binding"]["request_id"], 2)
            self.assertEqual(presentation["binding"]["mcp_meta"]["claudecode/toolUseId"], f[2]["call_id"])
            self.assertEqual(presentation["raw_typed_output"], before[3])
            self.assertEqual(presentation["http_request"], evidence)
            self.assertEqual(presentation["wire_text"]["sha256"], digest(REFUSAL.encode()))
            self.assertEqual(presentation["visible_payload"]["sha256"], digest(REFUSAL[:-1].encode()))
            self.assertEqual(receipt["model_visible_plain_text"], REFUSAL[:-1])
            self.assertEqual(receipt["result"], before[-1]["result"])
            self.assertEqual(receipt["request"], before[-1]["request"])
            self.assertEqual(receipt["response"], before[-1]["response"])
            self.assertEqual(f[4], before[4])
            self.assertEqual(wire_path.read_bytes(), recording)

    def test_binding_ambiguity_reuse_other_call_and_wrong_api(self):
        f = case()
        owner = Controller("/synthetic", {})
        owner.receipts = [f[-1]]
        with patch.object(Path, "read_text", return_value=json.dumps(f[0])), patch(
                "model_server.wire_evidence", return_value={"sha256": "synthetic-wire", "bytes": 100}):
            for evidence in (None, {**HTTP, "target": "/v1/messages"}, {**HTTP, "method": "GET"}):
                with self.assertRaises(AssertionError):
                    owner.match_reply(f[2], f[3], f[1], f[4], evidence)
            owner.receipts.append(copy.deepcopy(f[-1]))
            with self.assertRaisesRegex(AssertionError, "ambiguous"):
                owner.match_reply(f[2], f[3], f[1], f[4], HTTP)
            owner.receipts.pop()
            owner.receipts[0]["params"]["_meta"]["callId"] = "other_call_identical_arguments"
            with self.assertRaisesRegex(AssertionError, "missing/ambiguous"):
                owner.match_reply(f[2], f[3], f[1], f[4], HTTP)
            owner.receipts[0]["params"]["_meta"]["callId"] = f[2]["call_id"]
            receipt = owner.match_reply(f[2], f[3], f[1], f[4], HTTP)
            self.assertTrue(receipt["matched"])
            self.assertEqual(receipt["presentation"]["http_request"], HTTP)
            with self.assertRaisesRegex(AssertionError, "consumed"):
                owner.match_reply(f[2], f[3], f[1], f[4], HTTP)

    def test_original_wire_frame_hash_across_chunks_and_duplicate_rejection(self):
        message = {"jsonrpc": "2.0", "id": 2, "result": {"content": []}}
        frame = json.dumps(message, indent=None).encode() + b"\n"
        def recording(chunks):
            return "".join(json.dumps({"pid": 41, "direction": "to-client",
                "bytes_b64": base64.b64encode(chunk).decode(), "sha256": digest(chunk)}) + "\n"
                for chunk in chunks)
        path = SimpleNamespace(stat=lambda: SimpleNamespace(st_size=1000),
                               open=lambda: io.StringIO(recording([frame[:13], frame[13:]])))
        evidence = wire_evidence(path, 41, "to-client", message)
        self.assertEqual(evidence["sha256"], digest(frame))
        self.assertEqual(evidence["bytes"], len(frame))
        path.open = lambda: io.StringIO(recording([frame, frame]))
        with self.assertRaisesRegex(AssertionError, "ambiguous"):
            wire_evidence(path, 41, "to-client", message)
        with self.assertRaisesRegex(AssertionError, "missing"):
            wire_evidence(path, 42, "to-client", message)
        path.open = lambda: io.StringIO(recording([frame]).replace(digest(frame), "wrong-hash"))
        with self.assertRaisesRegex(AssertionError, "hash changed"):
            wire_evidence(path, 41, "to-client", message)
        path.stat = lambda: SimpleNamespace(st_size=128 * 1024 * 1024 + 1)
        with self.assertRaisesRegex(AssertionError, "exceeds evidence bound"):
            wire_evidence(path, 41, "to-client", message)

    def test_failure_log_is_bounded_and_keeps_original(self):
        owner = Controller("/synthetic", {})
        with patch("model_server.record") as log:
            for _ in range(20):
                owner.fail(AssertionError("original catalog failure"))
            for i in range(20):
                owner.fail(AssertionError(str(i)))
        self.assertEqual(owner.failures[0], "AssertionError('original catalog failure')")
        self.assertEqual(len(owner.failures), 16)
        self.assertEqual(log.call_count, 16)


class FatalWorkflowTests(unittest.IsolatedAsyncioTestCase):
    async def test_mcp_request_and_response_reuse_fail(self):
        owner = Controller("/synthetic", {})
        owner.initialized.add((41, "full"))
        request = {"kind": "mcp", "direction": "to-candidate", "connection": 41, "profile": "full",
                   "message": {"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                               "params": {"name": "read", "arguments": {"root": "/synthetic/fixture"}}}}
        response = {"kind": "mcp", "direction": "to-client", "connection": 41, "profile": "full",
                    "message": {"jsonrpc": "2.0", "id": 2,
                                "result": {"content": [{"type": "text", "text": "read reply"}]}}}
        key = (41, "full", 2)
        with patch("model_server.record"):
            await owner.observe(request)
            with self.assertRaisesRegex(AssertionError, "reused MCP connection/request ID"):
                await owner.observe(request)
            for method in ("ping", "roots/list"):
                server_request = {**response, "message": {"jsonrpc": "2.0", "id": 2, "method": method}}
                await owner.observe(server_request)
                self.assertIs(owner.pending[key], request)
                self.assertEqual(owner.receipts, [])
            await owner.observe(response)
            self.assertNotIn(key, owner.pending)
            self.assertEqual(len(owner.receipts), 1)
            self.assertEqual(owner.receipts[0]["request"], request["message"])
            self.assertEqual(owner.receipts[0]["response"], response["message"])
            for method in ("ping", "roots/list"):
                server_request = {**response, "message": {"jsonrpc": "2.0", "id": 2, "method": method}}
                await owner.observe(server_request)
                self.assertNotIn(key, owner.pending)
                self.assertEqual(len(owner.receipts), 1)
            with self.assertRaisesRegex(AssertionError, "reused MCP response identity"):
                await owner.observe(response)
        self.assertEqual(len(owner.receipts), 1)
        self.assertEqual(owner.failures, [])

    def workflow(self):
        owner = Controller("/synthetic", {})
        schema = {"type": "object", "properties": {}}
        owner.schemas = {("full", name): schema for name in FULL}
        body = {"tools": [{"type": "function", "function": {
            "name": "huyang_full_" + name, "parameters": schema}} for name in sorted(FULL)]}
        with patch("workflow.snapshot", return_value={}):
            wf = Workflow(owner, "fixture", "/synthetic/fixture")
        return owner, wf, body

    async def test_first_catalog_failure_is_terminal_even_if_retry_catalog_is_correct(self):
        owner, wf, body = self.workflow()
        with patch("workflow.record"):
            with self.assertRaisesRegex(AssertionError, "wrong actual model catalog") as first:
                await wf.advance({}, "chat")
            owner.failures.append(repr(first.exception))
            with self.assertRaises(AssertionError) as retry:
                await wf.advance(body, "chat")
        self.assertIs(retry.exception, first.exception)
        self.assertEqual(wf.step, "open")
        self.assertFalse(wf.done)
        self.assertEqual(wf.calls, [])
        self.assertEqual(len(wf.catalogs), 1)

    async def test_consumed_receipt_then_downstream_failure_cannot_rematch_or_advance(self):
        owner, wf, body = self.workflow()
        call = {"call_id": "call_hover", "base": "navigate", "name": "huyang_full_navigate",
                "arguments": {"root": "/synthetic/fixture", "relation": "hover"}, "step": "warm",
                "profile": "full"}
        receipt = {"params": {"name": "navigate", "arguments": call["arguments"]},
                   "profile": "full", "result": {"content": [{"type": "text", "text": "hover\n"}]},
                   "matched": False}
        manifest = {"client": "opencode", "binary": {"version": "opencode v2.0.30",
                    "sha256": "3eb9af9b4ab07d4de85b68a2678a1e17534f9cfa833ade80b80831e51972da72"}}
        owner.receipts = [receipt]
        wf.step, wf.pending, wf.calls = "warm", call, [copy.deepcopy(call)]
        body["messages"] = [{"role": "tool", "tool_call_id": "call_hover", "content": "hover\n"}]
        failure = AssertionError("original baseline/catalog failure")
        owner.baseline = AsyncMock(side_effect=failure)
        with patch("workflow.record"), patch.object(Path, "read_text", return_value=json.dumps(manifest)), patch.object(
                owner, "match_reply", wraps=owner.match_reply) as matcher:
            with self.assertRaises(AssertionError) as first:
                await wf.advance(body, "chat")
            with self.assertRaises(AssertionError) as retry:
                await wf.advance(body, "chat")
        self.assertIs(first.exception, failure)
        self.assertIs(retry.exception, failure)
        self.assertEqual(matcher.call_count, 1)
        self.assertTrue(receipt["matched"])
        self.assertIsNone(wf.pending)
        assert wf.failed_call is not None
        self.assertEqual(wf.failed_call["call_id"], "call_hover")
        self.assertEqual(wf.step, "warm")
        self.assertFalse(wf.done)
        self.assertEqual(len(wf.calls), 1)
        self.assertEqual(len(wf.replies), 1)

    async def test_existing_global_fatal_blocks_before_receipt_consumption(self):
        owner, wf, body = self.workflow()
        owner.failures.append("original catalog failure")
        with patch("workflow.record"), patch.object(owner, "match_reply") as matcher:
            with self.assertRaisesRegex(AssertionError, "original catalog failure"):
                await wf.advance(body, "chat")
        matcher.assert_not_called()
        self.assertEqual(wf.calls, [])
        self.assertEqual(wf.step, "open")
        self.assertFalse(wf.done)


if __name__ == "__main__":
    unittest.main()
