#!/usr/bin/env python3
"""Workshop MCP transport: explicit endpoint, one response, exact receipt."""
import argparse
import json
import subprocess
import sys

parser = argparse.ArgumentParser()
parser.add_argument("--socket", required=True)
parser.add_argument("--binary", default="/tmp/huyang-5d244ad-workshop")
group = parser.add_mutually_exclusive_group(required=True)
group.add_argument("--list", action="store_true")
group.add_argument("--describe")
group.add_argument("--call", help='JSON object with name and arguments')
args = parser.parse_args()
process = subprocess.Popen(
    [args.binary, "mcp", "--profile", "experimental", "--socket", args.socket],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=sys.stderr, text=True,
)
def send(frame):
    process.stdin.write(json.dumps(frame) + "\n")
    process.stdin.flush()
def request(identifier, method, params):
    send({"jsonrpc": "2.0", "id": identifier, "method": method, "params": params})
    while True:
        line = process.stdout.readline()
        if not line:
            raise RuntimeError("MCP adapter closed before response")
        frame = json.loads(line)
        if frame.get("id") == identifier:
            return frame
try:
    initialized = request(1, "initialize", {
        "protocolVersion": "2025-06-18", "capabilities": {},
        "clientInfo": {"name": "workshop-explicit-endpoint", "version": "1"},
    })
    if "error" in initialized:
        raise RuntimeError(str(initialized["error"]))
    send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    if args.call:
        response = request(2, "tools/call", json.loads(args.call))
        # Print the complete result, with neither product compaction nor manual
        # filtering hidden. Redirect to an owned artifact for large results.
        print(json.dumps(response, ensure_ascii=False, separators=(",", ":")))
    else:
        response = request(2, "tools/list", {})
        if "error" in response:
            raise RuntimeError(str(response["error"]))
        catalog = response["result"]["tools"]
        selected = [t for t in catalog if t["name"] == args.describe] if args.describe else [
            {"name": t["name"], "description": t["description"]} for t in catalog
        ]
        print(json.dumps(selected, ensure_ascii=False, separators=(",", ":")))
finally:
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()
