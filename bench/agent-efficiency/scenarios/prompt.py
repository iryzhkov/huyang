#!/usr/bin/env python3
"""Render the prompt for one benchmark scenario, language and tool family.

Usage: prompt.py --scenario E1 --language go --family huyang

The prompt is written for an unattended agent: it names the working directory, the
rules of the tool family under test, what done looks like, and the report format that
score.py parses out of the final message.
"""
from __future__ import annotations

import argparse
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))

REPORT_FORMAT = """\
When you are finished, end your final message with exactly this block, filled in:

BENCH REPORT
scenario: {scenario}
family: {family}
calls:
1. <tool name> - <why you chose it, one line>
2. ...
reasoning: <two to four sentences on why these tools and not others, and any friction>
BACKLOG STATUS: done

List every tool call you made, in order, including calls that failed. If a rule of the
tool family forced you to do something the long way, say so in the reasoning line.
"""


def load() -> dict:
    with open(os.path.join(HERE, "scenarios.json"), encoding="utf-8") as handle:
        return json.load(handle)


def render(spec: dict, scenario_id: str, language: str, family: str) -> str:
    scenario = next(s for s in spec["scenarios"] if s["id"] == scenario_id)
    lang = spec["languages"][language]
    rules = spec["families"][family]
    verify = scenario.get("verify")
    command = lang[verify] if verify else None
    parts = [
        "You are running one scenario of the Huyang agent-efficiency benchmark. The",
        "repository root is your working directory; the code under test is the fixture",
        f"directory {lang['dir']} (a self-contained {language} project). Work only inside",
        "that directory. Do not read the repository's README, docs or other directories,",
        "do not run git, and do not look for prior benchmark runs: the point is to",
        "measure the cost of the task itself.",
        "",
        f"Tool family under test: {family}. {rules}",
    ]
    if family == "huyang":
        parts += [
            "Start with workspace_open on the absolute path of the fixture directory and",
            "pass the returned workspace_id to every later huyang call.",
        ]
    parts += ["", f"Task ({scenario['id']}: {scenario['title']}):", scenario["task"][language], ""]
    if command:
        parts += [
            f"Verification command (run from {lang['dir']}): {command}",
            "Run it once at the end and include its outcome in your final message.",
            "",
        ]
    parts += [f"Done means: {scenario['done']}", "", REPORT_FORMAT.format(scenario=scenario_id, family=family)]
    return "\n".join(parts)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", required=True)
    parser.add_argument("--language", default="go")
    parser.add_argument("--family", required=True)
    args = parser.parse_args()
    print(render(load(), args.scenario, args.language, args.family))


if __name__ == "__main__":
    main()
