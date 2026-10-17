#!/usr/bin/env python3
"""Transparent MCP stdio recorder and byte-preserving real-gopls delay relay."""
import argparse
import asyncio
import json
import os
from pathlib import Path
import signal
import sys

from common import canonical, control, digest, raw_record, record, require, snapshot

async def mcp(args):
    command = args.command
    if command[:1] == ["--"]:
        command = command[1:]
    require(command, "missing candidate command")
    # OpenCode's real plugin requires basename(command[0]) == huyang and argv[1] == mcp.
    # The job-local launcher embeds control/fixture paths and passes the absolute candidate.
    if args.candidate:
        command = [args.candidate, "mcp", *command]
    child = await asyncio.create_subprocess_exec(*command, stdin=asyncio.subprocess.PIPE,
                                                stdout=asyncio.subprocess.PIPE)
    assert child.stdin is not None and child.stdout is not None
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        loop.add_signal_handler(sig, lambda s=sig: child.send_signal(s) if child.returncode is None else None)
    pending = {}
    fixture = Path(os.environ["ACCEPTANCE_FIXTURE"])
    async def observe(message, direction):
        event = {"kind": "mcp", "direction": direction, "message": message,
                 "profile": args.profile, "connection": os.getpid()}
        if direction == "to-candidate" and message.get("method") == "tools/call":
            p = message["params"]
            pending[message["id"]] = p
            if p["name"] == "edit_apply":
                op = p.get("arguments", {}).get("operation", {})
                root = Path(p.get("arguments", {}).get("root", str(fixture)))
                path = (root / op.get("path", "")).resolve()
                require(path.is_relative_to(fixture.resolve()), "edit escaped fixture")
                if path.is_file():
                    event["before"] = snapshot(path)
        elif direction == "to-client" and message.get("id") in pending:
            p = pending.pop(message["id"])
            event["call"] = p
            if p["name"] == "edit_apply":
                path = Path(p["arguments"]["root"]) / p["arguments"]["operation"]["path"]
                if path.is_file():
                    event["after"] = snapshot(path)
        await control(event)

    async def pump(source, destination, direction):
        buffer = b""
        while True:
            chunk = await source.read(65536)
            if not chunk:
                break
            raw_record(args.log, direction, chunk)
            # Observe copies before forwarding; never serialize/modify protocol bytes.
            buffer += chunk
            require(len(buffer) <= 8 * 1024 * 1024, "MCP frame exceeds 8 MiB")
            while b"\n" in buffer:
                line, buffer = buffer.split(b"\n", 1)
                if line.strip():
                    await observe(json.loads(line), direction)
            destination.write(chunk)
            await destination.drain()
        require(not buffer.strip(), "unterminated MCP frame")
        try:
            destination.write_eof()
        except (AttributeError, OSError, NotImplementedError):
            destination.close()

    stdin = asyncio.StreamReader(limit=8 * 1024 * 1024)
    await loop.connect_read_pipe(lambda: asyncio.StreamReaderProtocol(stdin), sys.stdin.buffer)
    transport, protocol = await loop.connect_write_pipe(asyncio.streams.FlowControlMixin, sys.stdout.buffer)
    stdout = asyncio.StreamWriter(transport, protocol, None, loop)
    incoming = asyncio.create_task(pump(stdin, child.stdin, "to-candidate"))
    outgoing = asyncio.create_task(pump(child.stdout, stdout, "to-client"))
    waiter = asyncio.create_task(child.wait())
    try:
        done, _ = await asyncio.wait([incoming, outgoing, waiter], return_when=asyncio.FIRST_COMPLETED)
        for task in done:
            task.result()
        if waiter not in done and incoming not in done:
            child.terminate()
        await asyncio.wait_for(waiter, 10)
        await asyncio.wait_for(outgoing, 5)
    finally:
        for task in (incoming, outgoing):
            task.cancel()
        if child.returncode is None:
            child.kill()
            await child.wait()
    require(child.returncode == 0, "candidate MCP exit " + str(child.returncode))

async def frame(reader):
    head = await reader.readuntil(b"\r\n\r\n")
    lengths = [int(x.split(b":", 1)[1]) for x in head[:-4].split(b"\r\n")
               if x.lower().startswith(b"content-length:")]
    require(len(lengths) == 1 and 0 <= lengths[0] <= 8*1024*1024, "invalid LSP length")
    body = await reader.readexactly(lengths[0])
    return head + body, json.loads(body)

async def lsp(args):
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    child = await asyncio.create_subprocess_exec(*command, stdin=asyncio.subprocess.PIPE,
                                                stdout=asyncio.subprocess.PIPE)
    assert child.stdin is not None and child.stdout is not None
    loop = asyncio.get_running_loop()
    stdin = asyncio.StreamReader(limit=8*1024*1024)
    await loop.connect_read_pipe(lambda: asyncio.StreamReaderProtocol(stdin), sys.stdin.buffer)
    transport, protocol = await loop.connect_write_pipe(asyncio.streams.FlowControlMixin, sys.stdout.buffer)
    stdout = asyncio.StreamWriter(transport, protocol, None, loop)
    log = str(Path(args.log).with_name(Path(args.log).stem + "-" + str(os.getpid()) + ".jsonl"))
    lock = asyncio.Lock()
    held = []
    released = set()
    post = {}
    baseline = set()
    async def emit(raw, message, delayed=False):
        stdout.write(raw)
        await stdout.drain()
        raw_record(log, "to-neovim", raw, delayed=delayed)
        await control({"kind": "lsp-delivered", "message": message,
                       "sha256": digest(raw), "delayed": delayed, "relay": os.getpid()})

    async def subscriptions():
        reader, writer = await asyncio.open_unix_connection(os.environ["ACCEPTANCE_CONTROL"])
        writer.write((canonical({"kind": "subscribe", "relay": os.getpid()}) + "\n").encode())
        await writer.drain()
        while line := await reader.readline():
            event = json.loads(line)
            if event.get("release"):
                async with lock:
                    released.add(event["release"])
                    keep = []
                    for raw, message in held:
                        if message["params"]["uri"] == event["release"]:
                            await emit(raw, message, True)
                        else:
                            keep.append((raw, message))
                    held[:] = keep

    async def upstream():
        while True:
            try:
                raw, message = await frame(stdin)
            except asyncio.IncompleteReadError:
                child.stdin.close()
                return
            raw_record(log, "to-gopls", raw)
            method = message.get("method")
            p = message.get("params", {})
            if method == "initialize":
                await control({"kind": "lsp-initialize", "message": message, "relay": os.getpid()})
            if method in ("textDocument/didOpen", "textDocument/didChange"):
                doc = p["textDocument"]
                texts = [doc.get("text", "")] if method.endswith("didOpen") else [
                    c.get("text", "") for c in p.get("contentChanges", [])]
                if any("missingAcceptanceSymbol" in t for t in texts):
                    post[doc["uri"]] = doc["version"]
                await control({"kind": "lsp-document", "message": message, "relay": os.getpid()})
            child.stdin.write(raw)
            await child.stdin.drain()

    async def downstream():
        while True:
            try:
                raw, message = await frame(child.stdout)
            except asyncio.IncompleteReadError:
                return
            raw_record(log, "from-gopls", raw)
            async with lock:
                if message.get("method") == "textDocument/publishDiagnostics":
                    p = message["params"]
                    uri, version = p["uri"], p.get("version")
                    finding = any("missingAcceptanceSymbol" in d.get("message", "") for d in p["diagnostics"])
                    answer = await control({"kind": "lsp-publication", "message": message,
                                            "sha256": digest(raw), "relay": os.getpid(),
                                            "post_version": post.get(uri)})
                    if finding:
                        require(version is not None and version == post.get(uri),
                                "gopls finding missing exact postimage version")
                    if (answer.get("hold") or any(m["params"]["uri"] == uri for _, m in held)) and uri not in released:
                        held.append((raw, message))
                        raw_record(log, "held", raw)
                        await control({"kind": "lsp-held", "uri": uri, "version": version,
                                       "sha256": digest(raw), "finding": finding, "relay": os.getpid()})
                        continue
                await emit(raw, message)
    tasks = [asyncio.create_task(f()) for f in (upstream, downstream, subscriptions)]
    waiter = asyncio.create_task(child.wait())
    try:
        done, _ = await asyncio.wait([*tasks, waiter], return_when=asyncio.FIRST_COMPLETED)
        for task in done:
            task.result()
        if waiter not in done:
            child.terminate()
        await asyncio.wait_for(waiter, 10)
    finally:
        for task in tasks:
            task.cancel()
        if child.returncode is None:
            child.kill()
            await child.wait()

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["mcp", "lsp"])
    parser.add_argument("--log", required=True)
    parser.add_argument("--candidate")
    parser.add_argument("--profile", default="full")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    a = parser.parse_args()
    try:
        asyncio.run(mcp(a) if a.mode == "mcp" else lsp(a))
    except BaseException as error:
        record(a.log, fatal=repr(error))
        raise
