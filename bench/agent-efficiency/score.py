#!/usr/bin/env python3
"""Score a submitted benchmark batch from the T3 thread transcripts.

Usage: score.py --batch NAME [--db ~/.t3/userdata/state.sqlite] [--markdown]

For every row of runs/NAME.tsv the thread with the same title is looked up in the T3
state database. Its tool activities give the ordered tool calls with exact arguments and
results; the context-window events give the provider's own token counts per turn; the
final assistant message gives the agent's BENCH REPORT. Token counts for arguments and
results use the cl100k_base tokenizer through `go run ./bench/agent-efficiency/protocol
count`, the same tokenizer the protocol measurement uses.

The result is written to runs/NAME-scores.json and, with --markdown, printed as the
per-scenario table used in RESULTS.md.
"""
from __future__ import annotations

import argparse
import csv
import json
import os
import re
import sqlite3
import statistics
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", ".."))
DEFAULT_DB = os.path.expanduser("~/.t3/userdata/state.sqlite")


class Tokenizer:
    """Counts tokens through the Go protocol program so both measurements agree."""

    def __init__(self) -> None:
        self.process = subprocess.Popen(
            ["go", "run", "./bench/agent-efficiency/protocol", "count", "--lines"],
            cwd=REPO, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
        )

    def count(self, text: str) -> int:
        assert self.process.stdin and self.process.stdout
        self.process.stdin.write(json.dumps(text) + "\n")
        self.process.stdin.flush()
        return int(self.process.stdout.readline().strip())

    def close(self) -> None:
        assert self.process.stdin
        self.process.stdin.close()
        self.process.wait()


def tool_calls(connection: sqlite3.Connection, thread_id: str) -> list[dict]:
    """Return the completed tool calls of a thread in order."""
    calls = []
    rows = connection.execute(
        "select payload_json, created_at from projection_thread_activities "
        "where thread_id = ? and kind = 'tool.completed' order by sequence",
        (thread_id,),
    )
    for payload_json, created_at in rows:
        payload = json.loads(payload_json)
        data = payload.get("data") or {}
        item = data.get("item") or {}
        kind = payload.get("itemType")
        if kind == "mcp_tool_call":
            name = item.get("tool") or item.get("name") or payload.get("detail", "").split(":")[0]
            arguments = item.get("arguments")
            result = item.get("result") or {}
            content = result.get("content") or []
            text = "".join(c.get("text", "") for c in content if isinstance(c, dict))
            duration = item.get("durationMs")
        elif kind == "command_execution":
            name = "bash"
            arguments = payload.get("detail", "")
            text = item.get("aggregatedOutput", "")
            duration = item.get("durationMs")
        else:
            name = data.get("toolName") or kind or "unknown"
            arguments = data.get("input")
            output = data.get("output") or item.get("output") or ""
            text = output if isinstance(output, str) else json.dumps(output)
            duration = item.get("durationMs") or data.get("durationMs")
        calls.append({
            "tool": name, "kind": kind,
            "request": arguments if isinstance(arguments, str) else json.dumps(arguments, ensure_ascii=False),
            "response": text, "duration_ms": duration, "created_at": created_at,
        })
    return calls


def provider_tokens(connection: sqlite3.Connection, thread_id: str) -> dict:
    """Sum the provider's per-turn token counts for a thread."""
    totals = {"input": 0, "cached_input": 0, "output": 0, "turns": 0}
    rows = connection.execute(
        "select payload_json from projection_thread_activities "
        "where thread_id = ? and kind = 'context-window.updated' order by sequence",
        (thread_id,),
    )
    for (payload_json,) in rows:
        payload = json.loads(payload_json)
        totals["input"] += int(payload.get("lastInputTokens") or 0)
        totals["cached_input"] += int(payload.get("lastCachedInputTokens") or 0)
        totals["output"] += int(payload.get("lastOutputTokens") or 0)
        totals["turns"] += 1
    return totals


def bench_report(connection: sqlite3.Connection, thread_id: str) -> str:
    row = connection.execute(
        "select text from projection_thread_messages where thread_id = ? and role = 'assistant' "
        "order by created_at desc limit 1",
        (thread_id,),
    ).fetchone()
    if not row or not row[0]:
        return ""
    match = re.search(r"BENCH REPORT.*", row[0], re.S)
    return match.group(0) if match else ""


def find_thread(connection: sqlite3.Connection, title: str) -> str | None:
    row = connection.execute(
        "select thread_id from projection_threads where title = ? and deleted_at is null "
        "order by created_at desc limit 1",
        (title,),
    ).fetchone()
    return row[0] if row else None


def score_run(connection: sqlite3.Connection, tokenizer: Tokenizer, row: dict) -> dict:
    thread_id = find_thread(connection, row["title"])
    scored = dict(row, thread_id=thread_id, found=thread_id is not None)
    if thread_id is None:
        return scored
    calls = tool_calls(connection, thread_id)
    for call in calls:
        call["request_tokens"] = tokenizer.count(call["request"] or "")
        call["response_tokens"] = tokenizer.count(call["response"] or "")
    times = [c["created_at"] for c in calls]
    scored.update({
        "calls": calls, "call_count": len(calls),
        "request_tokens": sum(c["request_tokens"] for c in calls),
        "response_tokens": sum(c["response_tokens"] for c in calls),
        "tool_ms": sum(int(c["duration_ms"] or 0) for c in calls),
        "first_call_at": times[0] if times else None, "last_call_at": times[-1] if times else None,
        "provider_tokens": provider_tokens(connection, thread_id),
        "report": bench_report(connection, thread_id),
    })
    return scored


def summarise(runs: list[dict]) -> list[dict]:
    groups: dict[tuple, list[dict]] = {}
    for run in runs:
        if run.get("found"):
            groups.setdefault((run["scenario"], run["language"], run["family"]), []).append(run)
    rows = []
    for (scenario, language, family), items in sorted(groups.items()):
        def mean(key: str) -> float:
            return statistics.mean(item[key] for item in items)
        rows.append({
            "scenario": scenario, "language": language, "family": family, "runs": len(items),
            "calls": mean("call_count"), "request_tokens": mean("request_tokens"),
            "response_tokens": mean("response_tokens"), "tool_ms": mean("tool_ms"),
            "provider_input": statistics.mean(i["provider_tokens"]["input"] for i in items),
            "provider_output": statistics.mean(i["provider_tokens"]["output"] for i in items),
        })
    return rows


def markdown(rows: list[dict]) -> str:
    lines = [
        "| Scenario | Lang | Family | Runs | Calls | Req tokens | Resp tokens | Tool ms | Provider in | Provider out |",
        "|---|---|---|---|---|---|---|---|---|---|",
    ]
    for row in rows:
        lines.append(
            f"| {row['scenario']} | {row['language']} | {row['family']} | {row['runs']} | {row['calls']:.1f} | "
            f"{row['request_tokens']:.0f} | {row['response_tokens']:.0f} | {row['tool_ms']:.0f} | "
            f"{row['provider_input']:.0f} | {row['provider_output']:.0f} |"
        )
    return "\n".join(lines)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--batch", required=True)
    parser.add_argument("--db", default=DEFAULT_DB)
    parser.add_argument("--markdown", action="store_true")
    args = parser.parse_args()
    log = os.path.join(HERE, "runs", f"{args.batch}.tsv")
    with open(log, encoding="utf-8") as handle:
        rows = list(csv.DictReader(handle, delimiter="\t"))
    connection = sqlite3.connect(f"file:{args.db}?mode=ro", uri=True)
    tokenizer = Tokenizer()
    try:
        runs = [score_run(connection, tokenizer, row) for row in rows]
    finally:
        tokenizer.close()
    missing = [run["title"] for run in runs if not run["found"]]
    summary = summarise(runs)
    out = os.path.join(HERE, "runs", f"{args.batch}-scores.json")
    with open(out, "w", encoding="utf-8") as handle:
        json.dump({"batch": args.batch, "runs": runs, "summary": summary, "missing": missing}, handle, indent=1)
    print(f"scored {len(runs) - len(missing)} of {len(runs)} runs; wrote {out}", file=sys.stderr)
    if missing:
        print(f"threads not found yet: {len(missing)}", file=sys.stderr)
    if args.markdown:
        print(markdown(summary))


if __name__ == "__main__":
    main()
