"""Model-driven workflow. Guards are extracted exclusively from client HTTP input."""
import asyncio
import copy
import json
from pathlib import Path
import re

from common import FULL, LEAN, canonical, digest, file_hash, record, require, snapshot, write_json

REV = re.compile(r"revision_id=(docrev_[0-9a-f]{64})")
WSREV = re.compile(r"revision=(wsrev_[0-9]+)")
SET = re.compile(r"result_set=(set_[0-9a-f]+)")
WORKSPACE = re.compile(r"id=(ws_[0-9a-f]+)")

def token(pattern, text):
    match = pattern.search(text)
    require(match is not None, "missing plain token " + pattern.pattern + ": " + text)
    assert match is not None
    return match[1]

def flatten_text(value):
    if isinstance(value, str):
        return value
    if isinstance(value, list):
        return "\n".join(flatten_text(v) for v in value)
    if isinstance(value, dict):
        if value.get("type") in ("text", "input_text", "output_text"):
            return value.get("text", "")
        if "content" in value:
            return flatten_text(value["content"])
    return ""

def plain_result(value):
    # Some clients serialize the real MCP content array inside a function output string.
    # Decode that envelope only; never look for guards in an out-of-band channel.
    if isinstance(value, str):
        try:
            parsed = json.loads(value)
        except (ValueError, TypeError):
            return value
        if isinstance(parsed, dict) and "content" in parsed:
            require("structuredContent" not in parsed, "client output contains structuredContent")
            return flatten_text(parsed["content"])
        if isinstance(parsed, list) and all(isinstance(v, dict) and v.get("type") == "text" for v in parsed):
            return flatten_text(parsed)
        return value
    return flatten_text(value)

def plain_data(text):
    for line in text.splitlines():
        if line.startswith("data: "):
            return json.loads(line[len("data: "):])
    raise AssertionError("plain reply lacks its documented data: line")

def dictionaries(value):
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from dictionaries(child)
    elif isinstance(value, list):
        for child in value:
            yield from dictionaries(child)

def results(body, protocol):
    found = {}
    if protocol == "anthropic":
        for message in body.get("messages", []):
            for block in message.get("content", []) if isinstance(message.get("content"), list) else []:
                if block.get("type") == "tool_result":
                    found[block["tool_use_id"]] = flatten_text(block.get("content"))
    elif protocol == "responses":
        for item in body.get("input", []) if isinstance(body.get("input"), list) else []:
            if item.get("type") == "function_call_output":
                found[item["call_id"]] = plain_result(item.get("output", ""))
    else:
        for message in body.get("messages", []):
            if message.get("role") == "tool":
                found[message["tool_call_id"]] = flatten_text(message.get("content"))
    return found

def tool_catalog(body):
    answer = {}
    def visit(items, namespace=None):
        for item in items:
            if item.get("type") == "namespace":
                visit(item.get("tools", []), item["name"])
                continue
            definition = item.get("function", item)
            name = definition.get("name", "")
            qualified = (namespace + "." if namespace else "") + name
            if "huyang" not in qualified:
                continue
            matches = [base for base in FULL if name == base or name.endswith("__" + base)
                       or name.endswith("_" + base)]
            require(len(matches) == 1, "unknown Huyang model tool " + qualified)
            base = matches[0]
            require(base not in answer, "duplicate exposed Huyang tool " + base)
            schema = definition.get("input_schema", definition.get("parameters"))
            require(isinstance(schema, dict), "missing direct model input schema " + qualified)
            require(not definition.get("defer_loading"), "deferred tool discovery unsupported: " + qualified)
            answer[base] = {"name": name, "namespace": namespace, "schema": schema}
    visit(body.get("tools", []))
    # Responses can carry formal declarations in developer additional_tools items.
    # Inspect those descriptors only, never tool descriptions or orchestration text.
    for item in body.get("input", []) if isinstance(body.get("input"), list) else []:
        if item.get("type") == "additional_tools":
            visit(item.get("tools", []))
    return answer

def shape(schema):
    """Ignore documentation/dialect metadata, preserve all validation constraints."""
    if isinstance(schema, list):
        return [shape(v) for v in schema]
    if isinstance(schema, dict):
        return {k: shape(v) for k, v in schema.items()
                if k not in ("description", "$schema", "title")}
    return schema

class Workflow:
    def __init__(self, owner, nonce, root, profile="full", session=None, provider="acceptance", probe=False):
        self.owner, self.nonce, self.root = owner, nonce, Path(root)
        self.profile, self.session, self.provider = profile, session, provider
        self.probe = probe
        self.step = "probe" if probe else ("open" if profile == "full" else "read")
        self.calls, self.replies, self.catalogs = [], [], []
        self.pending = None
        self.done = False
        self.late_gate_delivered = False
        self.notice_occurrences = []
        self.edit_args = None
        self.guard = self.current = self.wsrev = self.set_handle = self.workspace = ""
        self.transaction = self.finding_id = ""
        self.edit_evidence_ids = []
        self.edit_evidence = []
        self.diagnostic_item = None
        self.before = snapshot(self.root / "main.go")
        self.after = None
        self.late = None
        self.final = ""
        self.log = owner.case / "workflow.jsonl"

    def prompt(self):
        return "HUYANG_ACCEPTANCE case=" + self.nonce + "; use only the scripted Huyang tools."

    async def advance(self, body, protocol):
        require(not self.done, "primary request after workflow completion")
        catalog = tool_catalog(body)
        self.catalogs.append({"sha256": digest(canonical(catalog).encode()), "tools": catalog,
                              "sessionId": self.session, "providerID": self.provider})
        record(self.log, nonce=self.nonce, step=self.step, catalog=self.catalogs[-1])
        require(set(catalog) == (FULL if self.profile == "full" else LEAN),
                self.nonce + ": wrong actual model catalog: " + str(sorted(catalog)))
        expected_prefix = "huyang_full_" if self.profile == "full" else "huyang_lean_"
        if self.session is not None:
            require(all(v["name"].startswith(expected_prefix) for v in catalog.values()),
                    "plugin exposed wrong registration")
        for base, definition in catalog.items():
            expected = self.owner.schemas.get((self.profile, base))
            require(expected is not None, "no actual MCP tools/list schema for " + self.profile + "/" + base)
            require(shape(definition["schema"]) == shape(expected),
                    "model schema differs from actual MCP catalog for " + base)
            if self.profile == "lean":
                require(shape(expected) == shape(self.owner.lean_schemas[base]),
                        "candidate lean schema differs from M7 closed schema: " + base)
        if self.pending:
            outputs = results(body, protocol)
            call_id = self.pending["call_id"]
            require(call_id in outputs, "missing real client tool output for " + call_id)
            text = outputs[call_id]
            wire = self.owner.match_reply(self.pending, text, protocol)
            text = wire["model_visible_plain_text"]
            self.replies.append({"call_id": call_id, "text": text, "mcp": wire})
            record(self.log, nonce=self.nonce, step=self.step, call_id=call_id, plain=text, mcp=wire)
            self.observe_notice(text, wire)
            await self.consume(text, wire)
            self.validate_notice()
            self.pending = None
        if self.step == "finish":
            if self.profile == "full" and not self.probe:
                require(len(self.notice_occurrences) == 1, "expected exactly one actual late notice")
                self.validate_notice()
            self.done = True
            require(self.probe or self.after is not None, "missing committed mutation snapshot")
            self.final = ("HUYANG_ACCEPTANCE_COMPLETE " + self.nonce + " revision=" + self.current
                          + " result_set=" + self.set_handle + " finding=" + self.finding_id)
            return {"text": self.final}
        name, arguments = self.next_call()
        require(name in catalog, "required native direct tool missing: " + name)
        definition = catalog[name]
        call_id = "call_" + self.nonce + "_" + str(len(self.calls) + 1)
        call = {"name": definition["name"], "namespace": definition["namespace"],
                "arguments": arguments, "call_id": call_id, "base": name, "step": self.step}
        self.calls.append(copy.deepcopy(call))
        self.pending = call
        record(self.log, nonce=self.nonce, emit=call)
        return call

    def observe_notice(self, text, wire):
        if not self.late_gate_delivered:
            return
        # Scan the newly returned reply only, not repeated tool history in HTTP input.
        for line in text.splitlines():
            if "missingAcceptanceSymbol" not in line or "(late finding;" not in line:
                continue
            attribution = re.search(r"\(late finding; attribution: ([^)]*)\)", line)
            require(attribution is not None and "not attributed to this call" not in line,
                    "late notice lacks explicit transaction attribution")
            assert attribution is not None and self.pending is not None
            fields = dict(part.split("=", 1) for part in attribution[1].split() if "=" in part)
            candidates = fields.get("candidates", "").split(",")
            require(any(candidates), "late notice has no transaction candidates")
            occurrence = {"call_id": self.pending["call_id"], "step": self.pending["step"],
                          "mcp_connection": wire["connection"], "mcp_request_id": wire["request_id"],
                          "line": line, "attribution": attribution[1], "candidates": candidates}
            self.notice_occurrences.append(occurrence)
            record(self.log, nonce=self.nonce, late_notice=occurrence)
            require(len(self.notice_occurrences) == 1, "same late finding delivered more than once")
            self.late = line

    def validate_notice(self):
        # The first notice may precede the evidence_get reply that names the transaction.
        if self.transaction and self.notice_occurrences:
            require(self.transaction in self.notice_occurrences[0]["candidates"],
                    "late notice attributed to a different edit transaction")

    def next_call(self):
        root = {"root": str(self.root)}
        guarded = lambda: {"kind": "create_file", "path": "main.go", "replace": True,
                           "revision_id": self.guard, "content": self.postimage}
        if self.step == "probe":
            return "read", {**root, "target": {"path": "main.go"}}
        if self.step in ("open", "verify_workspace"):
            return "workspace_open", root
        if self.step == "read":
            return "read", {**root, "target": {"path": "main.go"}}
        if self.step == "attach":
            return "language_server_status", {**root, "languages": ["go"]}
        if self.step == "warm":
            return "navigate", {**root, "relation": "hover",
                                "target": {"symbol_locator": {"path": "main.go", "name_path": "Marker"}}}
        if self.step == "edit_evidence":
            return "evidence_get", {**root, "evidence_id": self.edit_evidence_ids[0]}
        if self.step == "edit":
            self.edit_args = {**root, "operation": guarded()}
            if self.profile == "full":
                self.edit_args["idempotency_key"] = self.nonce + "-edit"
                self.edit_args["format"] = False
            return "edit_apply", copy.deepcopy(self.edit_args)
        if self.step == "search":
            return "search", {**root, "query": "alpha", "paths": ["main.go"], "limit": 1}
        if self.step == "refine":
            return "search", {**root, "query": "alpha", "result_set_handle": self.set_handle,
                              "refine": {"matched_text": {"literal": "alpha"}}}
        if self.step == "diagnostics":
            return "diagnostics", {**root, "full": True}
        if self.step in ("quiet", "reread"):
            return "read", {**root, "target": {"path": "main.go"}}
        if self.step == "replay":
            return "edit_apply", copy.deepcopy(self.edit_args)
        if self.step == "stale":
            assert self.edit_args is not None
            stale = copy.deepcopy(self.edit_args)
            if self.profile == "full":
                stale["idempotency_key"] = self.nonce + "-stale-new-key"
            stale["operation"]["content"] = "// should never commit\n"
            return "edit_apply", stale
        raise AssertionError("unknown state " + self.step)

    async def consume(self, text, wire):
        error = wire["result"].get("isError", False)
        if self.step != "stale":
            require(not error, "unexpected tool error in " + self.step + ": " + text)
        if self.step == "probe":
            token(REV, text)
            self.step = "finish"
        elif self.step == "open":
            self.workspace = token(WORKSPACE, text)
            self.step = "read"
        elif self.step == "read":
            self.guard = token(REV, text)
            match = re.match(r"main\.go \(revision_id=[^;]+; lines=1-(\d+)/(\d+)\)\n", text)
            require(match is not None and match[1] == match[2], "incomplete plain source read")
            assert match is not None
            lines = text[match.end():].splitlines(keepends=True)
            require(len(lines) >= int(match[1]), "read source line count mismatch")
            source = "".join(lines[:int(match[1])])
            require(digest(source.encode()) == self.before["sha256"], "read source differs from fixture")
            if self.profile == "full":
                self.postimage = source + "var _ = missingAcceptanceSymbol\n"
                self.step = "attach"
            else:
                self.postimage = source.replace("alpha one", "alpha changed")
                require(self.postimage != source, "lean marker missing")
                self.step = "edit"
        elif self.step == "attach":
            self.step = "warm"
        elif self.step == "warm":
            await asyncio.wait_for(self.owner.baseline(self.root), 30)
            self.step = "edit"
        elif self.step == "edit":
            require(text.startswith("Replaced main.go ("), "missing successful edit receipt")
            self.current, self.wsrev = token(REV, text), token(WSREV, text)
            require(self.current != self.guard, "edit did not change document revision")
            self.after = wire["after"]
            require(self.after["sha256"] == digest(self.postimage.encode()), "postimage hash mismatch")
            if self.profile == "full":
                require(re.search(r"provisional|incomplete|pending|unavailable", text, re.I),
                        "delayed edit incorrectly lacks provisional/incomplete evidence")
                await self.owner.release(self.root, text)
                await asyncio.wait_for(self.owner.delivered(self.root), 30)
                self.late_gate_delivered = True
                evidence_lines = [line for line in text.splitlines() if line.startswith("evidence: ")]
                require(evidence_lines, "delayed receipt lacks actual diagnostic evidence IDs")
                self.edit_evidence_ids = re.findall(r"ev_[0-9a-f]+", evidence_lines[0])
                require(self.edit_evidence_ids, "unrecognized evidence ID grammar")
                self.step = "edit_evidence"
            else:
                self.step = "search"
        elif self.step == "edit_evidence":
            data = plain_data(text)["evidence"]
            self.edit_evidence.append(data)
            payload = data["payload"]
            require(isinstance(payload, dict), "unknown candidate evidence payload grammar")
            if payload.get("document") == str(self.root / "main.go"):
                require(payload.get("document_revision") == self.current,
                        "edit diagnostic evidence does not address the returned postimage guard")
                require(payload.get("transaction_id"), "edit diagnostic evidence lacks real transaction")
                self.transaction = payload["transaction_id"]
            self.edit_evidence_ids.pop(0)
            if not self.edit_evidence_ids:
                require(self.transaction, "no exact edit diagnostic transaction in visible evidence")
                self.step = "search"
        elif self.step == "search":
            require("main.go:" in text and "alpha" in text, "search returned no actual hit")
            require("1 of 2 matches" in text, "limited search did not retain the real total")
            if self.profile == "full":
                self.set_handle = token(SET, text)
                self.step = "refine"
            else:
                self.step = "stale"
        elif self.step == "refine":
            require("refined from " + self.set_handle in text, "refinement lost returned set handle")
            require("main.go:4:// alpha two" in text, "refinement did not reveal the second real frozen hit")
            self.step = "diagnostics"
        elif self.step == "diagnostics":
            # Delivery was event-gated before the intervening calls. Collect once;
            # the one-shot notice may already have ridden on any of those replies.
            items = [item for item in dictionaries(plain_data(text))
                     if isinstance(item.get("finding"), dict)
                     and "missingAcceptanceSymbol" in item["finding"].get("message", "")]
            require(items, "diagnostics full report lacks actual finding item")
            item = items[0]
            uri = (self.root / "main.go").as_uri()
            versions = {event["message"]["params"]["version"]
                        for event in self.owner.deliveries[uri]}
            require(item.get("document_version") in versions, "finding version differs from gopls postimage")
            require(item.get("document_revision") == self.current, "finding attributed to wrong document revision")
            require(item.get("transaction_id") == self.transaction, "finding attributed to wrong edit transaction")
            require(self.transaction in item.get("attribution", {}).get("candidates", []),
                    "late attribution candidates lost original edit transaction")
            require("gopls" in item.get("producer", ""), "late finding did not come from real gopls")
            self.finding_id = item["id"]
            self.diagnostic_item = item
            self.step = "quiet"
        elif self.step == "quiet":
            require(token(REV, text) == self.current, "revision changed during late collection")
            self.step = "replay"
        elif self.step == "replay":
            require(self.calls[-1]["arguments"] == self.edit_args, "replay args/key changed")
            require(text.startswith("Replayed original receipt; no new mutation"), "missing replay receipt")
            require("revision_id=" + self.current + " (as of the original edit)" in text,
                    "replay lost original document revision")
            require(token(WSREV, text) == self.wsrev, "replay changed committed workspace revision")
            require(wire["before"] == self.after == wire["after"], "replay caused a second write")
            self.step = "stale"
        elif self.step == "stale":
            require(error and not text.startswith("Replaced"), "stale revision accepted")
            require(re.search(r"stale|revision|changed", text, re.I), "unrecognized stale refusal")
            require(wire["before"] == self.after == wire["after"], "stale refusal changed file")
            self.step = "reread"
        elif self.step == "reread":
            require(token(REV, text) == self.current, "final document revision changed")
            require(snapshot(self.root / "main.go") == self.after, "final bytes/metadata changed")
            self.step = "verify_workspace" if self.profile == "full" else "finish"
        elif self.step == "verify_workspace":
            require(token(WSREV, text) == self.wsrev, "replay/refusal bumped current workspace revision")
            self.step = "finish"
        else:
            raise AssertionError("unexpected output in " + self.step)

    def report(self):
        return {"nonce": self.nonce, "profile": self.profile, "done": self.done,
                "sessionId": self.session, "providerID": self.provider, "probe": self.probe,
                "guard": self.guard, "committed_revision_id": self.current,
                "committed_workspace_revision": self.wsrev, "result_set_handle": self.set_handle,
                "late_plain_attribution": self.late, "notice_occurrences": self.notice_occurrences,
                "late_gate_delivered": self.late_gate_delivered, "transaction_id": self.transaction,
                "finding_id": self.finding_id, "diagnostic_item": self.diagnostic_item,
                "edit_evidence": self.edit_evidence, "before": self.before, "after": self.after,
                "calls": self.calls, "replies": self.replies, "catalogs": self.catalogs,
                "final": self.final}
