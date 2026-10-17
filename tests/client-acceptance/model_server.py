"""Loopback-only deterministic Messages/Responses/Chat SSE and passive evidence controller."""
import asyncio
import base64
import gzip
import json
from pathlib import Path
import re
from urllib.parse import urlsplit

from common import FULL, LEAN, canonical, digest, raw_record, record, require, write_json
from workflow import results

class Controller:
    def __init__(self, case, lean_schemas):
        self.case = Path(case)
        self.lean_schemas = lean_schemas
        self.schemas = {}
        self.workflows = {}
        self.pending = {}
        self.receipts = []
        self.subscribers = []
        self.armed = set()
        self.released = set()
        self.clean = {}
        self.held = {}
        self.deliveries = {}
        self.condition = asyncio.Condition()
        self.failures = []
        self.count = 0
        self.serial = 0
        self.events = self.case / "events.jsonl"
        self.http_log = self.case / "model-http.jsonl"
        self.catalogs = {}
        self.initialized = set()
        self.initialize_replies = set()
        self.lsp_initializations = []
        self.listed = set()

    def fail(self, error):
        self.failures.append(repr(error))
        record(self.events, fatal=repr(error))

    async def start(self):
        self.control_path = self.case / "control.sock"
        require(len(str(self.control_path).encode()) < 100, "use a shorter --job path for Unix sockets")
        self.control_server = await asyncio.start_unix_server(self.control_request, path=self.control_path,
                                                                      limit=8*1024*1024)
        self.http_server = await asyncio.start_server(self.http_request, "127.0.0.1", 0)
        self.port = self.http_server.sockets[0].getsockname()[1]
        return self

    async def close(self):
        for writer in self.subscribers:
            writer.close()
        for server in (self.control_server, self.http_server):
            server.close()
            await server.wait_closed()

    async def control_request(self, reader, writer):
        try:
            line = await asyncio.wait_for(reader.readline(), 30)
            event = json.loads(line)
            if event["kind"] == "subscribe":
                self.subscribers.append(writer)
                record(self.events, event=event)
                await reader.read()
                return
            async with self.condition:
                answer = await self.observe(event)
                self.condition.notify_all()
            writer.write((canonical(answer) + "\n").encode())
            await writer.drain()
        except Exception as error:
            self.fail(error)
            writer.write((canonical({"error": repr(error)}) + "\n").encode())
            await writer.drain()
        finally:
            writer.close()

    async def observe(self, event):
        record(self.events, event=event)
        kind = event["kind"]
        if kind == "mcp":
            profile = event["profile"]
            m = event["message"]
            key = (event.get("connection"), profile, m.get("id"))
            # All adapters in one process profile have an independent MCP ID space.
            # The recorder supplies connection=<pid>, never reuse only request ID.
            if event["direction"] == "to-candidate":
                method = m.get("method")
                if method == "initialize":
                    self.pending[key] = event
                if method == "notifications/initialized":
                    require((event["connection"], profile) in self.initialize_replies,
                            "initialized notification preceded actual initialize result")
                    self.initialized.add((event["connection"], profile))
                if method == "tools/call":
                    p = m["params"]
                    require(p["name"] in (FULL if profile == "full" else LEAN), "unexpected MCP tool")
                    root = Path(p["arguments"]["root"]).resolve()
                    require(root.is_relative_to((self.case / "fixture").resolve()), "foreign Huyang root")
                    if p["name"] == "edit_apply":
                        for wf in self.workflows.values():
                            if str(wf.root) == str(root) and wf.pending and wf.pending["step"] == "edit":
                                if wf.profile == "full":
                                    self.armed.add((root / "main.go").as_uri())
                    self.pending[key] = event
                elif method == "tools/list":
                    self.pending[key] = event
            elif key in self.pending:
                request = self.pending.pop(key)
                method = request["message"]["method"]
                require("error" not in m, "MCP protocol error: " + canonical(m))
                if method == "initialize":
                    require("protocolVersion" in m["result"], "candidate did not negotiate MCP initialize")
                    self.initialize_replies.add((event["connection"], profile))
                elif method == "tools/list":
                    require((event["connection"], profile) in self.initialized, "tools/list before initialized")
                    tools = m["result"]["tools"]
                    require({t["name"] for t in tools} == (FULL if profile == "full" else LEAN),
                            "wrong actual MCP profile catalog")
                    require(not any("outputSchema" in t for t in tools), "plain tools advertise outputSchema")
                    self.catalogs[profile] = tools
                    self.listed.add((event["connection"], profile))
                    for t in tools:
                        self.schemas[(profile, t["name"])] = t["inputSchema"]
                    write_json(self.case / ("mcp-catalog-" + profile + ".json"), tools)
                else:
                    result = m["result"]
                    require("structuredContent" not in result, "candidate emitted structuredContent")
                    require(len(result.get("content", [])) == 1 and result["content"][0]["type"] == "text",
                            "candidate did not emit exactly one TextContent")
                    receipt = {"connection": event["connection"], "request_id": m["id"],
                               "params": request["message"]["params"], "result": result,
                               "before": request.get("before"), "after": event.get("after"), "matched": False}
                    self.receipts.append(receipt)
        elif kind == "lsp-initialize":
            caps = event["message"]["params"]["capabilities"]["textDocument"]
            require(caps.get("publishDiagnostics", {}).get("versionSupport") is True,
                    "real LSP did not negotiate versioned push publications")
            require("diagnostic" not in caps, "pull diagnostics was unexpectedly enabled")
            self.lsp_initializations.append(event)
        elif kind == "lsp-publication":
            p = event["message"]["params"]
            uri = p["uri"]
            if not p["diagnostics"] and uri not in self.armed:
                require(p.get("version") is not None, "baseline publication is unversioned")
            hold = uri in self.armed and uri not in self.released and (
                p.get("version") is not None and p.get("version") == event.get("post_version"))
            return {"hold": hold}
        elif kind == "lsp-held":
            self.held.setdefault(event["uri"], []).append(event)
        elif kind == "lsp-delivered":
            if event["message"].get("method") == "textDocument/publishDiagnostics":
                p = event["message"]["params"]
                if not p["diagnostics"] and p["uri"] not in self.armed:
                    self.clean[p["uri"]] = event
                if event["delayed"]:
                    held = self.held.get(p["uri"], [])
                    require(any(h["sha256"] == event["sha256"] for h in held),
                            "released diagnostic differs from upstream frame bytes")
                    self.deliveries.setdefault(p["uri"], []).append(event)
        return {"ok": True}

    async def baseline(self, root):
        uri = (Path(root) / "main.go").as_uri()
        async with self.condition:
            await self.condition.wait_for(lambda: uri in self.clean or self.failures)
        require(not self.failures, str(self.failures))

    async def release(self, root, visible_receipt):
        uri = (Path(root) / "main.go").as_uri()
        async with self.condition:
            await asyncio.wait_for(self.condition.wait_for(
                lambda: any(h["finding"] for h in self.held.get(uri, [])) or self.failures), 30)
            require(not self.failures, str(self.failures))
            require(uri not in self.released, "diagnostic gate released twice")
            self.released.add(uri)
            record(self.events, release=uri, actual_client_visible_receipt=visible_receipt)
            for writer in self.subscribers:
                if not writer.is_closing():
                    writer.write((canonical({"release": uri}) + "\n").encode())
                    await writer.drain()

    async def delivered(self, root):
        uri = (Path(root) / "main.go").as_uri()
        async with self.condition:
            await self.condition.wait_for(lambda: any(
                any("missingAcceptanceSymbol" in d.get("message", "")
                    for d in event["message"]["params"]["diagnostics"])
                for event in self.deliveries.get(uri, [])) or self.failures)
        require(not self.failures, str(self.failures))

    def match_reply(self, call, visible):
        for receipt in self.receipts:
            if not receipt["matched"] and receipt["params"].get("name") == call["base"] and (
                    receipt["params"].get("arguments") == call["arguments"]):
                text = receipt["result"]["content"][0]["text"]
                require(text == visible, "client model-visible result differs from real MCP text")
                receipt["matched"] = True
                return receipt
        raise AssertionError("no exact MCP call/reply matches actual model result " + call["call_id"])

    async def http_request(self, reader, writer):
        try:
            async with asyncio.timeout(90):
                head = await reader.readuntil(b"\r\n\r\n")
                request_line, *header_lines = head[:-4].split(b"\r\n")
                method, target, _ = request_line.decode().split(" ", 2)
                headers = dict(h.decode().split(":", 1) for h in header_lines)
                headers = {k.lower(): v.strip() for k, v in headers.items()}
                if headers.get("transfer-encoding", "").lower() == "chunked":
                    chunks = []
                    while True:
                        size = int((await reader.readline()).split(b";")[0], 16)
                        if not size:
                            require(await reader.readline() == b"\r\n", "chunked trailers unsupported")
                            break
                        chunks.append(await reader.readexactly(size))
                        require(await reader.readexactly(2) == b"\r\n", "invalid HTTP chunk")
                    raw = b"".join(chunks)
                else:
                    length = int(headers.get("content-length", "0"))
                    require(length <= 16*1024*1024, "HTTP body exceeds bound")
                    raw = await reader.readexactly(length)
                raw_record(self.http_log, "request", raw, method=method, target=target, headers=headers)
                encoding = headers.get("content-encoding", "identity")
                require(encoding in ("identity", "gzip"), "unknown client protocol encoding: " + encoding)
                if encoding == "gzip":
                    raw = gzip.decompress(raw)
                body = json.loads(raw) if raw else {}
                path = urlsplit(target).path
                if method == "HEAD" and path == "/api/hello":
                    await self.send(writer, b"", "application/json", head_only=True)
                    return
                if path == "/v1/messages/count_tokens":
                    await self.send(writer, b'{"input_tokens":64}', "application/json")
                    return
                if path == "/v1/messages":
                    protocol, provider = "anthropic", "acceptance"
                elif path == "/v1/responses":
                    protocol, provider = "responses", "acceptance"
                elif path in ("/acceptance/v1/chat/completions", "/ollama/v1/chat/completions"):
                    protocol, provider = "chat", path.split("/")[1]
                else:
                    raise AssertionError("unknown primary client HTTP endpoint: " + target)
                self.count += 1
                require(self.count <= 500, "model request count exceeded 500")
                # Auxiliary title requests are explicitly classified; never advance a workflow.
                system = body.get("system", body.get("instructions", ""))
                for message in body.get("messages", []):
                    if message.get("role") in ("system", "developer"):
                        system = str(system) + str(message.get("content", ""))
                auxiliary = not body.get("tools") and re.search(
                    r"generate.{0,40}title|title.{0,40}generation|short.{0,20}title", str(system), re.I)
                if auxiliary:
                    record(self.http_log, auxiliary="title", request=body)
                    answer = {"text": "Huyang acceptance fixture"}
                else:
                    matches = re.findall(r"HUYANG_ACCEPTANCE case=([A-Za-z0-9_-]+)", canonical(body))
                    require(matches, "unclassified primary/auxiliary request; refusing inference")
                    nonce = matches[-1]
                    require(nonce in self.workflows, "unknown acceptance nonce " + nonce)
                    wf = self.workflows[nonce]
                    require(wf.provider == provider, "providerID misrouting: expected " + wf.provider + " got " + provider)
                    record(self.http_log, primary=nonce, protocol=protocol, providerID=provider,
                           sessionId=wf.session, request=body)
                    answer = await wf.advance(body, protocol)
                self.serial += 1
                if not body.get("stream"):
                    require(auxiliary, "unknown primary non-streaming protocol")
                    payload = self.nonstream(protocol, answer["text"], body, self.serial)
                    raw_record(self.http_log, "auxiliary-response-json", payload, protocol=protocol)
                    await self.send(writer, payload, "application/json")
                else:
                    payload = self.encode(protocol, answer, body, self.serial)
                    raw_record(self.http_log, "response-sse", payload, protocol=protocol, answer=answer)
                    await self.send(writer, payload, "text/event-stream")
        except Exception as error:
            self.fail(error)
            try:
                await self.send(writer, canonical({"error": {"message": repr(error), "type": "acceptance_failure"}}).encode(),
                                "application/json", status=500)
            except (OSError, ConnectionError):
                pass
        finally:
            writer.close()

    async def send(self, writer, payload, content_type, status=200, head_only=False):
        head = ("HTTP/1.1 " + str(status) + (" OK" if status == 200 else " Acceptance failure")
                + "\r\nContent-Type: " + content_type + "\r\nContent-Length: " + str(len(payload))
                + "\r\nCache-Control: no-cache\r\nConnection: close\r\n\r\n").encode()
        writer.write(head + (b"" if head_only else payload))
        await writer.drain()

    @staticmethod
    def nonstream(protocol, text, body, serial):
        if protocol == "chat":
            value = {"id": "aux_" + str(serial), "object": "chat.completion", "created": 1,
                     "model": body.get("model", "mock"),
                     "choices": [{"index": 0, "message": {"role": "assistant", "content": text},
                                  "finish_reason": "stop"}],
                     "usage": {"prompt_tokens": 64, "completion_tokens": 8, "total_tokens": 72}}
        elif protocol == "anthropic":
            value = {"id": "aux_" + str(serial), "type": "message", "role": "assistant",
                     "model": body["model"], "content": [{"type": "text", "text": text}],
                     "stop_reason": "end_turn", "stop_sequence": None,
                     "usage": {"input_tokens": 64, "output_tokens": 8}}
        else:
            value = {"id": "aux_" + str(serial), "object": "response", "status": "completed",
                     "model": body["model"], "output": [{"id": "aux_msg_" + str(serial), "type": "message",
                         "role": "assistant", "status": "completed",
                         "content": [{"type": "output_text", "text": text, "annotations": []}]}],
                     "usage": {"input_tokens": 64, "output_tokens": 8, "total_tokens": 72}}
        return canonical(value).encode()

    @staticmethod
    def encode(protocol, answer, body, serial):
        tool = "arguments" in answer
        ident = str(serial)
        usage = {"input_tokens": 64, "output_tokens": 32}
        events = []
        def sse(event, data):
            events.append(("event: " + event + "\n" if event else "") + "data: " + canonical(data) + "\n\n")
        if protocol == "anthropic":
            model = body.get("model", "claude-sonnet-4-6")
            sse("message_start", {"type": "message_start", "message": {
                "id": "msg_" + ident, "type": "message", "role": "assistant", "model": model,
                "content": [], "stop_reason": None, "stop_sequence": None, "usage": usage}})
            if tool:
                block = {"type": "tool_use", "id": answer["call_id"], "name": answer["name"], "input": {}}
                delta = {"type": "input_json_delta", "partial_json": canonical(answer["arguments"])}
            else:
                block = {"type": "text", "text": ""}
                delta = {"type": "text_delta", "text": answer["text"]}
            sse("content_block_start", {"type": "content_block_start", "index": 0, "content_block": block})
            sse("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": delta})
            sse("content_block_stop", {"type": "content_block_stop", "index": 0})
            sse("message_delta", {"type": "message_delta",
                                 "delta": {"stop_reason": "tool_use" if tool else "end_turn", "stop_sequence": None},
                                 "usage": {"output_tokens": 32}})
            sse("message_stop", {"type": "message_stop"})
        elif protocol == "chat":
            base = {"id": "chatcmpl_" + ident, "object": "chat.completion.chunk", "created": 1,
                    "model": body.get("model", "mock")}
            def chunk(delta, reason=None):
                sse(None, {**base, "choices": [{"index": 0, "delta": delta, "finish_reason": reason}]})
            chunk({"role": "assistant"})
            if tool:
                chunk({"tool_calls": [{"index": 0, "id": answer["call_id"], "type": "function",
                                      "function": {"name": answer["name"],
                                                   "arguments": canonical(answer["arguments"])}}]})
            else:
                chunk({"content": answer["text"]})
            chunk({}, "tool_calls" if tool else "stop")
            sse(None, {**base, "choices": [], "usage": {"prompt_tokens": 64, "completion_tokens": 32, "total_tokens": 96}})
            events.append("data: [DONE]\n\n")
        else:
            seq = 0
            def event(kind, **data):
                nonlocal seq
                sse(kind, {"type": kind, "sequence_number": seq, **data})
                seq += 1
            response = {"id": "resp_" + ident, "object": "response", "created_at": 1,
                        "model": body.get("model", "gpt-6.1-sol"), "status": "in_progress",
                        "error": None, "incomplete_details": None, "output": [],
                        "parallel_tool_calls": False, "usage": None}
            event("response.created", response=response)
            event("response.in_progress", response=response)
            if tool:
                item = {"id": "fc_" + ident, "type": "function_call", "status": "in_progress",
                        "call_id": answer["call_id"], "name": answer["name"], "arguments": ""}
                if answer.get("namespace"):
                    item["namespace"] = answer["namespace"]
                event("response.output_item.added", output_index=0, item=item.copy())
                arguments = canonical(answer["arguments"])
                event("response.function_call_arguments.delta", item_id=item["id"], output_index=0,
                      delta=arguments)
                event("response.function_call_arguments.done", item_id=item["id"], output_index=0,
                      arguments=arguments)
                item = {**item, "arguments": arguments, "status": "completed"}
            else:
                item = {"id": "msg_" + ident, "type": "message", "status": "in_progress",
                        "role": "assistant", "content": []}
                event("response.output_item.added", output_index=0, item=item.copy())
                part = {"type": "output_text", "text": "", "annotations": []}
                event("response.content_part.added", item_id=item["id"], output_index=0, content_index=0, part=part)
                event("response.output_text.delta", item_id=item["id"], output_index=0, content_index=0,
                      delta=answer["text"])
                event("response.output_text.done", item_id=item["id"], output_index=0, content_index=0, text=answer["text"])
                part = {**part, "text": answer["text"]}
                event("response.content_part.done", item_id=item["id"], output_index=0, content_index=0, part=part)
                item = {**item, "status": "completed", "content": [part]}
            event("response.output_item.done", output_index=0, item=item)
            response = {**response, "status": "completed", "output": [item], "usage": {
                **usage, "total_tokens": 96, "input_tokens_details": {"cached_tokens": 0},
                "output_tokens_details": {"reasoning_tokens": 0}}}
            event("response.completed", response=response)
        return "".join(events).encode()

    def report(self):
        return {"failures": self.failures, "http_request_count": self.count,
                "initialized_connections": sorted(self.initialized),
                "listed_connections": sorted(self.listed), "released_uris": sorted(self.released),
                "baseline": self.clean, "held": self.held, "deliveries": self.deliveries,
                "lsp_initializations": self.lsp_initializations,
                "workflows": [w.report() for w in self.workflows.values()]}
