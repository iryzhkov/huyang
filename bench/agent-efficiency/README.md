# Agent efficiency benchmark

This harness measures what an agent pays to do ordinary source-code work through
Huyang compared with a harness's built-in file tools (Read/Edit/Write/Grep) and with
plain Bash. Per operation it records the number of tool calls, the input tokens the
agent has to emit (request payloads), the output tokens it has to read (tool results),
and wall time.

Two measurements feed `RESULTS.md`:

1. **Protocol measurement** (`protocol/`): a Go program that starts a private Huyang
   service on the fixture, replays the cheapest correct call sequence for every scenario
   through the real stdio MCP adapter, and records exact request and response sizes.
   Token counts use the `cl100k_base` tokenizer through `tiktoken-go`; the BPE file is
   cached under `.cache/`. The built-in and Bash columns in this measurement are
   *modelled*: the request is the arguments the tool would need, the response is the text
   the tool would print for the same fixture (a `cat -n` style listing for Read, the
   snippet Edit echoes back, `path:line:text` lines for Grep, raw bytes for `cat`).
   This measurement is deterministic and reruns in seconds, so it is what every code
   change is checked against.
2. **Agent measurement** (`submit.sh`, `score.py`): the Muse free model, driven through
   the t3-steward backlog, performs each scenario with one tool family at a time. The
   thread transcript in the T3 database is the evidence: tool calls with their exact
   arguments and results, provider token counts per turn, and the agent's own report of
   why it chose each call. Model variance is smoothed by running every scenario at least
   three times per family.

## Layout

- `fixtures/generate.py` writes `fixtures/go` and `fixtures/python`, two small
  repositories with the same shape: a ~150-line core module, a ~900-line report module,
  two helper modules, and a test suite with one deliberately failing test. The output is
  committed; regenerate only by running the script.
- `scenarios/scenarios.json` defines the operations R1-R4, E1-E6 and V1 for both
  languages, with the task text, the done criterion and the verification command.
- `scenarios/prompt.py` renders the prompt for one scenario, language and tool family.
- `submit.sh` queues one backlog task per scenario, family and repetition.
- `score.py` reads the finished threads out of the T3 state database and prints the
  per-scenario table for `RESULTS.md`.
- `protocol/` holds the protocol measurement program.
- `runs/` keeps one TSV per submitted batch (the titles to look up) and the scored JSON.

## Rerun the protocol measurement

```sh
make build
go run ./bench/agent-efficiency/protocol run --label after > /tmp/protocol.json
go run ./bench/agent-efficiency/protocol table /tmp/protocol.json
```

`run` copies both fixtures to a temporary directory, starts `bin/huyang serve` with a
private socket, state directory and a config that trusts the copy, connects through
`bin/huyang mcp --profile full`, and executes every scenario. It prints one JSON document
with a record per call. `table` renders the per-scenario summary as Markdown.

## Rerun the agent measurement

```sh
bench/agent-efficiency/submit.sh baseline --reps 3
# wait for the steward to run the tasks (t3-steward backlog list --project huyang)
bench/agent-efficiency/score.py --batch baseline --markdown
```

`submit.sh` writes a task through `t3-backlog` for every scenario, language, family and
repetition, using `instance: opencode` and `model:
opencode/muse-spark-1.3-contributor-free`, and records the titles in
`runs/<batch>.tsv`. The tasks run in an isolated clone of this repository at the branch
head, so commit the fixtures and prompts before submitting. `score.py` finds the threads
by title in `~/.t3/userdata/state.sqlite`, extracts every tool call and the provider
token counts, and writes `runs/<batch>-scores.json` plus a Markdown table.

Token counting for the agent measurement uses the same `cl100k_base` tokenizer as the
protocol measurement (`go run ./bench/agent-efficiency/protocol count`), applied to the
tool arguments and results stored in the transcript. The provider's own
`inputTokens`/`outputTokens` per turn are recorded next to them; they include the system
prompt and conversation, so they are reported as totals per run, not per call.
