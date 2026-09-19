#!/usr/bin/env python3
"""Score a submitted benchmark batch from the T3 thread transcripts.

Usage: score.py --batch NAME [--db ~/.t3/userdata/state.sqlite] [--markdown]

For every row of runs/NAME.tsv the matching thread is looked up in the T3 state database
by its first user message (the rendered prompt), since the steward titles threads itself;
threads of one scenario are assigned to the rows in creation order. Its tool activities give the ordered tool calls with exact arguments and
results; the context-window events give the provider's own token counts per turn; the
final assistant message gives the agent's BENCH REPORT. The OpenCode provider records no
context-window events, so provider token columns read 0 for Muse batches; the tool-call
columns are the measurement. Token counts for arguments and
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
from datetime import datetime, timezone

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


def tool_durations(connection: sqlite3.Connection, thread_id: str) -> dict[str, float]:
    """Milliseconds between the start and the completion of each tool call.

    Claude-agent threads record no duration in the payload; the two
    activities of one call share a toolCallId and carry timestamps.
    """
    started: dict[str, datetime] = {}
    durations: dict[str, float] = {}
    rows = connection.execute(
        "select kind, payload_json, created_at from projection_thread_activities "
        "where thread_id = ? and kind in ('tool.started', 'tool.completed') order by sequence",
        (thread_id,),
    )
    for kind, payload_json, created_at in rows:
        call_id = (json.loads(payload_json) or {}).get("toolCallId")
        if not call_id or not created_at:
            continue
        try:
            at = datetime.fromisoformat(created_at.replace("Z", "+00:00"))
        except ValueError:
            continue
        if kind == "tool.started":
            started[call_id] = at
        elif call_id in started:
            durations[call_id] = (at - started.pop(call_id)).total_seconds() * 1000
    return durations


def tool_calls(connection: sqlite3.Connection, thread_id: str) -> list[dict]:
    """Return the completed tool calls of a thread in order."""
    calls = []
    durations = tool_durations(connection, thread_id)
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
        state = data.get("state") or {}
        if data.get("toolName") and isinstance(data.get("result"), dict):
            # Claude-agent threads: every item type (mcp_tool_call,
            # dynamic_tool_call, file_change, command_execution) carries
            # toolName, input and result.content, which is a string or a
            # list of content blocks.
            name = str(data["toolName"]).removeprefix("mcp__huyang__").removeprefix("huyang_")
            arguments = data.get("input")
            content = (data["result"] or {}).get("content")
            if isinstance(content, str):
                text = content
            elif isinstance(content, list):
                text = "".join(block.get("text", "") for block in content if isinstance(block, dict))
            else:
                text = "" if content is None else json.dumps(content, ensure_ascii=False)
            duration = durations.get(payload.get("toolCallId"))
        elif data.get("tool") and "input" in state:
            # OpenCode-backed threads: every tool kind carries tool, state.input,
            # state.output and state.time regardless of itemType.
            name = str(data["tool"]).removeprefix("huyang_")
            arguments = state.get("input")
            output = state.get("output") or ""
            text = output if isinstance(output, str) else json.dumps(output, ensure_ascii=False)
            time = state.get("time") or {}
            duration = (time["end"] - time["start"]) if "start" in time and "end" in time else state.get("durationMs")
        elif kind == "mcp_tool_call":
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
    """The provider's own token counts for a thread.

    Two shapes exist. A provider that reports per-turn counts
    (lastInputTokens and friends) is summed. A Claude-agent thread reports
    the context window instead: inputTokens is the whole conversation the
    turn read and therefore grows, while outputTokens is that turn's own
    output. Summing the input would count the conversation once per turn,
    so the peak is kept as input and the outputs are summed; the last event
    also carries totalProcessedTokens, the provider's own total.
    """
    totals = {"input": 0, "cached_input": 0, "output": 0, "turns": 0, "total_processed": 0, "input_is_peak": False}
    rows = connection.execute(
        "select payload_json from projection_thread_activities "
        "where thread_id = ? and kind = 'context-window.updated' order by sequence",
        (thread_id,),
    )
    for (payload_json,) in rows:
        payload = json.loads(payload_json)
        totals["turns"] += 1
        if payload.get("lastInputTokens") is not None or payload.get("lastOutputTokens") is not None:
            totals["input"] += int(payload.get("lastInputTokens") or 0)
            totals["cached_input"] += int(payload.get("lastCachedInputTokens") or 0)
            totals["output"] += int(payload.get("lastOutputTokens") or 0)
            continue
        totals["input_is_peak"] = True
        totals["input"] = max(totals["input"], int(payload.get("inputTokens") or 0))
        totals["output"] += int(payload.get("outputTokens") or 0)
        if payload.get("totalProcessedTokens"):
            totals["total_processed"] = int(payload["totalProcessedTokens"])
    return totals


def turn_windows(connection: sqlite3.Connection, thread_id: str) -> list[tuple[datetime, datetime]]:
    """The start and end of every finished turn of a thread.

    One benchmark task is one user prompt, so these windows are the agent's
    wall-clock runtime: model thinking, tool execution and the harness
    between them.
    """
    windows = []
    rows = connection.execute(
        "select started_at, completed_at from projection_turns where thread_id = ? order by requested_at",
        (thread_id,),
    )
    for started_at, completed_at in rows:
        if not started_at or not completed_at:
            continue
        try:
            start = datetime.fromisoformat(started_at.replace("Z", "+00:00"))
            end = datetime.fromisoformat(completed_at.replace("Z", "+00:00"))
        except ValueError:
            continue
        if end >= start:
            windows.append((start, end))
    return windows


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


PROMPT_PREFIX = "You are running one scenario of the Huyang agent-efficiency benchmark"


def batch_started(rows: list[dict]) -> str:
    """UTC timestamp of the earliest row in the batch.

    submit.sh writes started_at when it starts each row, and that column is the bound
    whenever it is present. There is deliberately no *default* bound: a default would
    silently widen the thread lookup to every benchmark thread ever recorded, and the
    scores would be computed against somebody else's runs without anything saying so.

    A legacy log has no started_at column, but the batches committed under runs/
    (after.tsv, haiku.tsv) carry task_file, whose -YYYYMMDD-HHMMSS.md suffix is the
    moment submit.sh wrote that row's task file. That is a real per-row measurement
    rather than a default, so it is used when started_at is absent, and the substitution
    is announced once on stderr. Without it those logs could never be scored again;
    RESULTS.md records batch `after` as scored for only 22 of its 99 runs.

    When a row yields neither, the refusal names the row.
    """
    stamps = []
    derived_from_task_file = False
    for index, row in enumerate(rows, start=1):
        value = (row.get("started_at") or "").strip()
        if value:
            try:
                moment = datetime.fromisoformat(value.replace("Z", "+00:00"))
            except ValueError:
                raise SystemExit(
                    f"row {index} of the batch log has an unreadable started_at: {value!r}"
                )
            if moment.tzinfo is None:
                moment = moment.replace(tzinfo=timezone.utc)
            stamps.append(moment.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S"))
            continue
        match = re.search(r"-(\d{8}-\d{6})\.md$", (row.get("task_file") or "").strip())
        if not match:
            raise SystemExit(
                f"row {index} of the batch log has no started_at, and no task_file whose "
                "name ends in -YYYYMMDD-HHMMSS.md, so the batch has no start time and has "
                "to be submitted again"
            )
        local = datetime.strptime(match.group(1), "%Y%m%d-%H%M%S").astimezone()
        stamps.append(local.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S"))
        derived_from_task_file = True
    if not stamps:
        raise SystemExit("the batch log has no rows, so it has no start time")
    if derived_from_task_file:
        print(
            "this batch log has no started_at, so the start time is derived from the "
            "-YYYYMMDD-HHMMSS suffix of task_file, which is when submit.sh wrote each "
            "row's task file",
            file=sys.stderr,
        )
    return min(stamps)


def thread_providers(connection: sqlite3.Connection) -> dict[str, str]:
    """The provider each thread ran on, as the session projection recorded it."""
    return {
        thread_id: (provider or "")
        for thread_id, provider in connection.execute(
            "select thread_id, provider_name from projection_thread_sessions"
        )
    }


def batch_threads(connection: sqlite3.Connection, since: str, provider: str = "") -> dict[tuple, list[str]]:
    """Map (scenario, language, family) to the benchmark threads started since `since`.

    The steward titles threads itself, so a thread is recognised by its first user
    message, which is the rendered prompt; threads of one scenario are interchangeable
    and are assigned to the batch rows in creation order.

    Two batches of the same scenarios on different models render the same prompt,
    so the prompt alone cannot tell their threads apart: scoring them without
    `provider` gave the codex and the Muse batch identical numbers, which were one
    batch's threads counted twice. `provider` keeps only the threads that ran on
    the provider the batch named.
    """
    threads: dict[tuple, list[str]] = {}
    seen: set[str] = set()
    providers = thread_providers(connection) if provider else {}
    rows = connection.execute(
        "select thread_id, text from projection_thread_messages where role = 'user' "
        "and text like ? and created_at >= ? order by created_at",
        (PROMPT_PREFIX + "%", since),
    )
    for thread_id, text in rows:
        if thread_id in seen:
            continue
        seen.add(thread_id)
        if provider and provider.lower() not in providers.get(thread_id, "").lower():
            continue
        scenario = re.search(r"Task \((\w+):", text)
        family = re.search(r"Tool family under test: (\w+)", text)
        language = re.search(r"fixtures/(\w+)", text)
        if scenario and family and language:
            key = (scenario.group(1), language.group(1), family.group(1))
            threads.setdefault(key, []).append(thread_id)
    return threads


def score_run(connection: sqlite3.Connection, tokenizer: Tokenizer, row: dict, threads: dict[tuple, list[str]]) -> dict:
    candidates = threads.get((row["scenario"], row["language"], row["family"]), [])
    index = int(row["rep"]) - 1
    thread_id = candidates[index] if index < len(candidates) else None
    scored: dict = dict(row, thread_id=thread_id, found=thread_id is not None)
    if thread_id is None:
        return scored
    calls = tool_calls(connection, thread_id)
    for call in calls:
        call["request_tokens"] = tokenizer.count(call["request"] or "")
        call["response_tokens"] = tokenizer.count(call["response"] or "")
    times = [c["created_at"] for c in calls]
    windows = turn_windows(connection, thread_id)
    tool_ms = sum(int(c["duration_ms"] or 0) for c in calls)
    wall_ms = sum((end - start).total_seconds() * 1000 for start, end in windows)
    scored.update({
        "calls": calls, "call_count": len(calls),
        "request_tokens": sum(c["request_tokens"] for c in calls),
        "response_tokens": sum(c["response_tokens"] for c in calls),
        "tool_ms": tool_ms,
        # wall_ms is the whole turn; the remainder is the model thinking and
        # the harness, which no tool choice can be blamed for directly.
        "wall_ms": wall_ms, "model_ms": max(wall_ms - tool_ms, 0), "turns": len(windows),
        "turn_windows": [[start.isoformat(), end.isoformat()] for start, end in windows],
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
            "wall_ms": mean("wall_ms"), "model_ms": mean("model_ms"),
            "provider_input": statistics.mean(i["provider_tokens"]["input"] for i in items),
            "provider_output": statistics.mean(i["provider_tokens"]["output"] for i in items),
        })
    return rows


def markdown(rows: list[dict]) -> str:
    lines = [
        "| Scenario | Lang | Family | Runs | Calls | Req tokens | Resp tokens | Wall s | Tool s | Model s | Provider in | Provider out |",
        "|---|---|---|---|---|---|---|---|---|---|---|---|",
    ]
    for row in rows:
        lines.append(
            f"| {row['scenario']} | {row['language']} | {row['family']} | {row['runs']} | {row['calls']:.1f} | "
            f"{row['request_tokens']:.0f} | {row['response_tokens']:.0f} | "
            f"{row['wall_ms'] / 1000:.1f} | {row['tool_ms'] / 1000:.1f} | {row['model_ms'] / 1000:.1f} | "
            f"{row['provider_input']:.0f} | {row['provider_output']:.0f} |"
        )
    return "\n".join(lines)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--batch", required=True)
    parser.add_argument("--db", default=DEFAULT_DB)
    parser.add_argument("--markdown", action="store_true")
    parser.add_argument("--provider", default="",
                        help="keep only threads that ran on this provider (claude, codex, opencode); "
                             "needed when two batches of the same scenarios ran on different models")
    args = parser.parse_args()
    log = os.path.join(HERE, "runs", f"{args.batch}.tsv")
    with open(log, encoding="utf-8") as handle:
        rows = list(csv.DictReader(handle, delimiter="\t"))
    connection = sqlite3.connect(f"file:{args.db}?mode=ro", uri=True)
    tokenizer = Tokenizer()
    try:
        threads = batch_threads(connection, batch_started(rows), args.provider)
        runs = [score_run(connection, tokenizer, row, threads) for row in rows]
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
