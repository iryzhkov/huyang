#!/usr/bin/env python3
"""Compare two scored agent batches scenario by scenario.

Usage: compare-scores.py BEFORE AFTER [--family huyang] [--markdown]

Both names are batches that score.py has already written to
runs/<batch>-scores.json. The table is per scenario and family: calls,
request tokens and response tokens, before and after, with the change.
Scenarios or families missing from either batch are listed, not averaged
away: a run that produced no thread is not a run that used no tokens.
"""
from __future__ import annotations

import argparse
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))


def load(batch: str) -> dict:
    path = os.path.join(HERE, "runs", f"{batch}-scores.json")
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def cells(scores: dict) -> dict:
    """Mean calls and tokens per scenario, language and family."""
    totals: dict[tuple[str, str, str], list[float]] = {}
    for run in scores.get("runs", []):
        if not run.get("found"):
            continue
        key = (run["scenario"], run.get("language", "go"), run["family"])
        calls = run.get("calls") or []
        request = sum(call.get("request_tokens", 0) for call in calls)
        response = sum(call.get("response_tokens", 0) for call in calls)
        entry = totals.setdefault(key, [0.0, 0.0, 0.0, 0.0])
        entry[0] += len(calls)
        entry[1] += request
        entry[2] += response
        entry[3] += 1
    return {key: [value[0] / value[3], value[1] / value[3], value[2] / value[3], value[3]]
            for key, value in totals.items() if value[3]}


def change(before: float, after: float) -> str:
    if before == 0:
        return "n/a"
    return f"{(after - before) / before * 100:+.0f}%"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("before")
    parser.add_argument("after")
    parser.add_argument("--family", default="")
    args = parser.parse_args()

    first, second = cells(load(args.before)), cells(load(args.after))
    keys = sorted(set(first) | set(second))
    print(f"| Scenario | Lang | Family | Runs {args.before}/{args.after} | Calls | Req tokens | Resp tokens |")
    print("|---|---|---|---|---|---|---|")
    for key in keys:
        if args.family and key[2] != args.family:
            continue
        left, right = first.get(key), second.get(key)
        if left is None or right is None:
            present = args.before if left else args.after
            print(f"| {key[0]} | {key[1]} | {key[2]} | only in {present} | | | |")
            continue
        print(f"| {key[0]} | {key[1]} | {key[2]} | {left[3]:.0f}/{right[3]:.0f} "
              f"| {left[0]:.1f} -> {right[0]:.1f} ({change(left[0], right[0])}) "
              f"| {left[1]:.0f} -> {right[1]:.0f} ({change(left[1], right[1])}) "
              f"| {left[2]:.0f} -> {right[2]:.0f} ({change(left[2], right[2])}) |")


if __name__ == "__main__":
    main()
