# Token-efficiency session — 2026-09-13

The question: the debugger, execution-trace and language-recovery stages added since
2026-09-12 are a large amount of new surface, and the session asked whether they had cost
efficiency or usability. Every number below is in `bench/agent-efficiency/RESULTS.md`
under "Efficiency session, 2026-09-13"; this file records what was measured, where, and
what is still open.

## What was measured, and where

| Measurement | Where it ran | What it says |
|---|---|---|
| Protocol benchmark, commit 79ab180 (`runs/protocol-x30.json`) | homelab, private service on copied fixtures | Per-call cost had not regressed since S20c: every scenario within two percent except `workspace_open`, +17 percent |
| Frozen catalog token counts | homelab | The full profile had grown from 8,526 to 9,306 cl100k tokens, paid once per session |
| Live probes against this repository through the stdio adapter | homelab | What the fixtures cannot show: a symbol read that scans 830 files, a coverage block of unrelated parser gaps, `root` refused by twelve tools |
| Fleet friction spool, 4 days, 14,414 agent calls in 144 sessions | normandy | Which replies agents actually pay for, and which refusals they actually meet |
| Agent benchmark batch `x30`, Claude Haiku 4.5, 135 runs | normandy, through the t3-steward backlog | The behaviour of a light model against the pre-fix service |

The batch could not run on an idle machine. homelab and omarchy-pc are bootstrapped as
backlog workers and both have `backlog_v2: mode: disabled` in their
`~/.config/t3-steward/persistent-worker.yaml`, and the coordinator's worker inventory binds
the huyang project to `workers: [normandy]`; omarchy-pc also had no `huyang development` T3
project until this session created one. Enabling a second live worker is a fleet change and
was left alone. normandy is also the host of the 2026-09-12 batch, so the agent numbers stay
comparable; its load average during the batch was above four, so wall-clock figures are not.

## The benchmark was measuring an impossible task

`V1` and `E5` both tell the agent that one test fails on purpose and must be named, and the
fixture generator documents the formatting gap that would cause it. The committed fixtures
handled the sign correctly and every test passed. Both fixtures now carry the documented
gap. `V1` and `E5` numbers from the `after` and `haiku` batches measured the earlier,
unsatisfiable task.

Two scenarios were added for the new surface: `D1` (find the line responsible for the
failing test) and `D2` (report a runtime value from inside a function).

## What is still open

- The agent batch against the fixed service, and the codex and Muse batches, are the
  measurement of whether the fixes change what a light model does, rather than what a reply
  weighs.
- `debug_session` answered `unavailable` in 20 of its 70 fleet calls because no DAP adapter
  is installed on the host; that is an environment gap, not a Huyang one, and it is what
  `D2` will price.
- The four debugger tools cost 1,654 catalog tokens per session and were called in 27 of
  144 sessions, almost all of them `debug_session` alone. Whether they belong in the default
  profile is a question for the batch, not for this file.
