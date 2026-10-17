"""Synthetic checks only; real client acceptance still requires a fresh complete job."""
import base64
import copy
import io
import json
from pathlib import Path
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
            (REFUSAL[:-1] + "\r\n", REFUSAL[:-1]),
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
        with patch("model_server.record"):
            await owner.observe(request)
            with self.assertRaisesRegex(AssertionError, "reused MCP connection/request ID"):
                await owner.observe(request)
            await owner.observe(response)
            with self.assertRaisesRegex(AssertionError, "reused MCP response identity"):
                await owner.observe(response)
        self.assertEqual(len(owner.receipts), 1)

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
