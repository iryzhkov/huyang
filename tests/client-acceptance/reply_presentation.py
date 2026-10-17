"""Bounded native presentation checks. Never substitute wire text for visible text."""
import copy
import json
import re

from common import canonical, digest, require

CODEX_SHA = "399ff69c431c066e40de318fba45c2b07f45a76b19939d247ee4828fc70c4215"
CLAUDE_SHA = "bc1aad9982c7b59c7d889ff92595d61cf46e0a6eae074cb551d932a989bcd222"
CODEX_SOURCE = "83086188ae0614bd882818ff1aa23f3194962012"
HEADER = re.compile(r"Wall time: [0-9]+\.[0-9]{4} seconds\nOutput:")


def text_evidence(text):
    raw = text.encode("utf-8")
    return {"sha256": digest(raw), "bytes": len(raw)}


def call_binding(call, receipt, meta_key):
    """Bind one unused receipt to the emitted call, not merely identical arguments."""
    require(not receipt["matched"], "MCP receipt already consumed")
    params = receipt["params"]
    require(params.get("_meta", {}).get(meta_key) == call["call_id"],
            "MCP metadata does not bind originating model call")
    require(params.get("name") == call["base"]
            and canonical(params.get("arguments")) == canonical(call["arguments"]),
            "MCP tool/arguments differ from originating model call")
    request, response = receipt["request"], receipt["response"]
    require(receipt["connection"] is not None
            and receipt["connection"] == receipt["request_connection"]
            and receipt["connection"] == receipt["response_connection"],
            "MCP connection identity changed")
    require(receipt["profile"] == call["profile"], "MCP profile identity changed")
    require(receipt["request_id"] is not None
            and canonical(request.get("id")) == canonical(receipt["request_id"])
            and canonical(response.get("id")) == canonical(receipt["request_id"])
            and request.get("jsonrpc") == response.get("jsonrpc") == "2.0"
            and request.get("method") == "tools/call" and "error" not in response
            and request.get("params") == params and response.get("result") == receipt["result"],
            "MCP request/response identity changed")
    result = receipt["result"]
    require(type(result.get("isError", False)) is bool, "invalid MCP error flag")
    require("structuredContent" not in result and len(result.get("content", [])) == 1,
            "wire result must have one TextContent and no structuredContent")
    block = result["content"][0]
    require(set(block) == {"type", "text"} and block["type"] == "text"
            and isinstance(block["text"], str), "unsupported wire TextContent")
    return {"model_call_id": call["call_id"], "model_tool": call["name"],
            "model_arguments": copy.deepcopy(call["arguments"]),
            "connection": receipt["connection"], "request_id": receipt["request_id"],
            "mcp_meta": copy.deepcopy(params["_meta"])}


def present_reply(manifest, protocol, call, output, body, receipt):
    """Return the actual visible payload and evidence after strict same-call checks."""
    client = manifest.get("client")
    binary = manifest.get("binary", {})
    if protocol == "responses":
        require(client == "codex" and binary.get("version") == "codex-cli 0.163.0"
                and binary.get("sha256") == CODEX_SHA, "unsupported Codex client/pin")
        provenance = manifest.get("codex_model_catalog", {})
        require(provenance.get("coverage") == "explicit-direct-tools-only"
                and provenance.get("model_slug") == body.get("model") == "gpt-6.1-sol"
                and provenance.get("source_entry_sha256")
                == "873c956b8ef4766fb4e059d25924ee2c672e29149c99e1fa936e89e83410c518"
                and provenance.get("overrides") == {
                    "tool_mode": "direct", "supports_search_tool": False},
                "unqualified Codex model/API route")
        binding = call_binding(call, receipt, "callId")
        origins = [item for item in body.get("input", [])
                   if isinstance(item, dict) and item.get("type") == "function_call"
                   and item.get("call_id") == call["call_id"]]
        require(len(origins) == 1, "missing/ambiguous originating Responses call")
        origin = origins[0]
        require(origin.get("name") == call["name"] and origin.get("namespace") == call["namespace"]
                and isinstance(origin.get("arguments"), str)
                and canonical(json.loads(origin["arguments"])) == canonical(call["arguments"])
                and isinstance(origin.get("id"), str) and bool(origin["id"])
                and origin["id"] == call.get("model_item_id")
                == receipt["params"]["_meta"].get("itemId"),
                "Responses originating function identity changed")
        require(isinstance(output, dict)
                and {"type", "call_id", "output"} <= set(output) <= {"type", "call_id", "output", "id"}
                and output["type"] == "function_call_output"
                and output["call_id"] == call["call_id"], "unknown Responses output shape/call")
        if "id" in output:
            require(isinstance(output["id"], str) and bool(output["id"]), "invalid output item ID")
        blocks = output["output"]
        require(isinstance(blocks, list) and len(blocks) == 2
                and all(isinstance(b, dict) and set(b) == {"type", "text"}
                        and b["type"] == "input_text" and isinstance(b["text"], str) for b in blocks),
                "Codex output must have exactly two ordered input_text blocks")
        header, visible = blocks[0]["text"], blocks[1]["text"]
        require(HEADER.fullmatch(header) is not None, "unknown Codex timing header")
        require(visible.encode("utf-8") == receipt["result"]["content"][0]["text"].encode("utf-8"),
                "Codex payload differs from complete MCP text")
        binding.update(model_item_id=origin["id"], output_item_id=output.get("id"))
        mapping, lost = "codex-responses-two-input-text-lossless", 0
        metadata = {"header": header, "header_evidence": text_evidence(header),
                    "source_commit": CODEX_SOURCE,
                    "source_verification": "official-unsigned-tag-and-commit; no-binary-source-attestation",
                    "client_catalog_provenance": copy.deepcopy(provenance)}
    else:
        require(protocol == "anthropic" and client == "claude"
                and binary.get("version") == "2.1.301 (Claude Code)"
                and binary.get("sha256") == CLAUDE_SHA, "unsupported Claude client/pin/API")
        binding = call_binding(call, receipt, "claudecode/toolUseId")
        origins = [b for m in body.get("messages", []) if m.get("role") == "assistant"
                   for b in (m.get("content") if isinstance(m.get("content"), list) else [])
                   if isinstance(b, dict) and b.get("type") == "tool_use"
                   and b.get("id") == call["call_id"]]
        require(len(origins) == 1 and origins[0].get("name") == call["name"]
                and canonical(origins[0].get("input")) == canonical(call["arguments"]),
                "missing/ambiguous/changed originating Anthropic call")
        require(isinstance(output, dict) and output.get("type") == "tool_result"
                and output.get("tool_use_id") == call["call_id"], "unknown Anthropic result shape/call")
        require(output.get("is_error", False) is receipt["result"].get("isError", False),
                "Anthropic/MCP error flags disagree")
        content = output.get("content")
        if isinstance(content, str):
            visible = content
        else:
            # Observed successful Messages replies use one ordinary text block.
            require(isinstance(content, list) and len(content) == 1
                    and isinstance(content[0], dict) and set(content[0]) == {"type", "text"}
                    and content[0]["type"] == "text" and isinstance(content[0]["text"], str),
                    "unknown Anthropic content shape")
            assert isinstance(content, list)
            visible = content[0]["text"]
        text = receipt["result"]["content"][0]["text"]
        mapping, lost, metadata = "exact-text", 0, {}
        if visible.encode("utf-8") != text.encode("utf-8"):
            operation = call["arguments"].get("operation", {})
            guard, current = operation.get("revision_id"), call.get("current_revision")
            require(isinstance(content, str) and call["step"] == "stale" and call["base"] == "edit_apply"
                    and receipt["result"].get("isError") is True and output.get("is_error") is True
                    and operation.get("kind") == "create_file" and operation.get("replace") is True
                    and operation.get("path") == "main.go"
                    and isinstance(guard, str) and re.fullmatch(r"docrev_[0-9a-f]{64}", guard)
                    and isinstance(current, str) and re.fullmatch(r"docrev_[0-9a-f]{64}", current)
                    and guard != current
                    and text.startswith("document_content_changed: document_content_changed: main.go changed from "
                                        + guard + " to " + current + "\n")
                    and ('next: edit_apply path="main.go" revision_id="' + current + '"') in text
                    and "(retry_with_the_current_revision_id)" in text
                    and "(retry_as_replace_literal_with_the_old_text)" in text,
                    "not the pinned Claude stale edit refusal")
            require(text.endswith("\n") and not text.endswith(("\n\n", "\r\n"))
                    and visible == text[:-1] and visible + "\n" == text,
                    "Claude refusal differs by more than one terminal LF")
            mapping, lost = "claude-messages-stale-error-single-final-newline", 1
    text = receipt["result"]["content"][0]["text"]
    return visible, {"mapping": mapping, "lost_bytes": lost, "binding": binding,
                     "client": client, "binary": copy.deepcopy(binary), "protocol": protocol,
                     "raw_typed_output": copy.deepcopy(output), "metadata": metadata,
                     "wire_text": text_evidence(text), "visible_payload": text_evidence(visible),
                     "mcp_isError": receipt["result"].get("isError", False)}
