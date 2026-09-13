# Huyang debugging/usability workshop — baseline-muse

Assignment: baseline-muse. Harness/provider: opencode; model: opencode/muse-spark-1.3-contributor-free; existing provider defaults, no other agents or paid fallbacks.

- Exact source/service baseline: 2c22cbf24c4242eca17533866e86a9da1827e8c6 (clone HEAD verified `git rev-parse HEAD` = same; detached HEAD, no shared checkout altered).
- Binary provenance: /home/igor/.local/share/huyang/bin/huyang → `go version -m` reports module github.com/iryzhkov/huyang v0.0.0-20260913104659-2c22cbf24c42, go1.27.0-X:nodwarf5. Matches baseline commit.
- Endpoint: shared service socket /run/user/1000/huyang/control.sock (srw, present). MCP transport: stdio JSON-RPC client to `/home/igor/.local/share/huyang/bin/huyang mcp --profile experimental --socket /run/user/1000/huyang/control.sock` (allowed Huyang transport). No global harness config changed; no daemon restarted.
- Fixture workspace: workshop-baseline-muse/ copied through Huyang copy_file from read-only /home/igor/Work/huyang/docs/friction/2026-09-13-debug-usability/fixtures/delivery/{go.mod,main.go,main_test.go}; SHA-256 verified: go.mod b1bc4f2b…, main.go 39d143cf…, main_test.go aa405f1d… (all match expected). Opened as separate Huyang workspace ws_4cd96c290abb1b04265402ebc3da40ac.
- Experimental catalog: tools/list → 21 tools (change_plan, code_actions, debug_breakpoints, debug_control, debug_inspect, debug_session, diagnostics, edit_apply, evidence_get, execution_graph, language_server_setup, language_server_status, navigate, path_explain, read, revision_diff, search, symbol_find, verify_run, workspace_inspect, workspace_open). Full exchanges in baseline-muse-evidence/.
- Language/provider state: gopls attached (go + gomod), delve built-in found at /home/igor/.local/share/nvim/mason/bin/dlv.

## Setup checkpoint

HEAD == baseline confirmed before any edits. Fixture copies made only inside clone. Baseline runs: `go run .` → `sent:1` exit 0; `go run . fail` → `rejected` exit 1; `go test ./...` → FAIL `credits=0: got "rejected", want "sent:0"`.

## First-diagnosis checkpoint

Strongest conclusion (high confidence): `dispatch` in workshop-baseline-muse/main.go:9 guards with `if credits <= 0`, so zero credits take the early `return "rejected"` at line 10 and the `deliver(credits)` call at line 12 is never reached. The `fail` invocation sets credits=0 (main.go:27), hence `deliver` is absent from the failing run. Spec requires zero and positive credits accepted, only negatives rejected — the guard must be `< 0`. Test agrees: `go test` fails only on `credits=0: got "rejected", want "sent:0"`; the -1 case already passes.

Evidence, per execution:

- Passing (`go run .`, credits=1), live delve via Huyang debug_session/start+step_over+step_into: breakpoint dispatch main.go:8 hit with `credits: int = 1`; steps landed 8→9 (`if credits <= 0`)→12 (`return deliver(credits)`); step_into entered `main.deliver main.go:15` with caller `#1 main.dispatch main.go:12`. deliver present.
- Failing (`go run . fail`, credits=0), same session flow: breakpoint hit with `credits: int = 0`; steps landed 8→9→10 (`return "rejected"`); governed evaluate `credits <= 0` → `true` (bool); continue → output `rejected`, `exit_code: 1`, state `exited`. deliver frame never appeared.
- Static path_explain dispatch→deliver (raw JSON in baseline-muse-evidence/): exactly one bounded candidate path `function:main.go:8:6 → cfg …:9:5:condition → cfg …:12:9:call_site → function:main.go:15:6`, code `execution_coverage_incomplete` with gaps (intraprocedural CFG, conservative call resolution, syntax-only, unresolved targets). It names the guard and the call site but does not evaluate the condition — the debugger supplied the decisive per-run values.
- execution_graph snapshot (raw JSON in baseline-muse-evidence/): built revision-keyed graph, also `execution_coverage_incomplete`; corroborates structure only, not the runtime branch taken.

What was and was not captured: per-run locals (`credits = 1` vs `0`), stepped line sequence, deliver-frame presence/absence, program output and exit code were captured. Not captured under read_only policy: expression evaluation was refused (`approval_required`/`enforceable_read_only_evaluation` unavailable for delve); retry with `allow_side_effects` succeeded — friction event, see below. The static tools captured candidate structure but explicitly not the taken branch.

## Repair and verification

Minimal repair (one line, workshop-baseline-muse/main.go:9, via Huyang edit_apply replace_literal, no new diagnostics): `if credits <= 0 {` → `if credits < 0 {`. No acceptance test changed; exit codes not suppressed — `main` still exits 1 on `"rejected"`.

Post-repair acceptance (shell, fixture dir): `go test ./...` → ok exit 0 (all three credit cases: 1→sent:1, 0→sent:0, -1→rejected); `go run .` → `sent:1` exit 0; `go run . fail` → `sent:0` exit 0; `gofmt -l .` clean; `go vet ./...` clean. Post-repair delve run of the failing invocation: breakpoint dispatch hit with `credits: int = 0`, steps 9→12 (`return deliver(credits)`), program output `sent:0`, `exit_code: 0`. deliver present. Sessions stopped, owned breakpoints verified cleared (`breakpoints: [], count: 0`).

## Friction events

1. edit_apply create_file shape: first attempt passed `content`/`path` top-level and got `MCP error 0: arguments contains unknown property "content"; content belongs under operation.content or operations.content`. Expected the same flat shape as copy operations. Recovery: resent nested under `operation`. Cost: 1 call, negligible time, no effect on answer. Suggestive: the wrapper accepts both `operation` (single) and `operations` (list) — the error message already says the fix, which is good.
2. debug_inspect evaluate under `read_only`: refused with `code approval_required`, `executed: false`, `unavailable: [enforceable_read_only_evaluation]` — delve adapter cannot enforce read-only expressions. Expected pure comparison `credits <= 0` to be evaluable read-only. Recovery: followed the response's own `next` and retried with `allow_side_effects` → `result: true`, type bool. Cost: 1 call. Effect: none on answer, but note the evaluated flag warns `debuggee_state_may_have_changed: true` even for a pure expression — worth knowing when chaining steps after an eval.
3. path_explain/execution_graph have no native wrapper in this harness — reachable only via the stdio JSON-RPC transport to the experimental profile. Worked first try; documented here as routing, not failure. Both returned `execution_coverage_incomplete` with explicit gap lists — honest boundedness, not opacity.
4. No Huyang implementation or execution-contract docs consulted before/during diagnosis; everything above came from tool descriptions plus observed responses. No assisted lookup to log.

## Observations (vs speculation)

- Observed: exact stepped line sequences, locals, eval result, outputs, exit codes, static path ids, gap lists. Speculation (marked): the `<=` looks like an off-by-one against the stated credit policy; I did not investigate who wrote it or why.
- Confusing names: none blocking. `initial_breakpoints` + symbol_locator worked without needing line numbers; `line_offset` semantics unexplored.
- Opaque refusals: none — both refusals carried machine-readable codes and actionable `next`.
- Missing next steps: debug_control continue-after-exit suggests continue/variables again on an exited session; harmless.
- Excessive output: debug start echoes full absolute program path twice (output + output_new) and large revision-bound locators per step; tolerable. Raw path_explain/execution_graph payloads (~235KB/~201KB) were kept out of context and stored as evidence files.
- Worked well: workspace_open overviews with verify commands; copy_file with outside-workspace `from` + SHA in response (hash match confirmed without reading originals into context); delve start/step/locals/exit_code round-trips; breakpoint auto-clear on session end.
- Minimal reproduction: in workshop-baseline-muse, `go run . fail` (credits=0) prints `rejected`, exit 1 pre-fix; `sent:0`, exit 0 post-fix. One-character class of fix (`<=` → `<`).

## Tallies

- Duration: ~15 min active work window; this session stayed within it. Huyang calls: ~25 (2 workspace_open, 1 multi-read, 2 inspect/status, 5 edit_apply, 3 debug_session starts + 3 stops, 8 debug_control, 2 debug_inspect, 2 breakpoint lists) plus 4 stdio transport sessions (tools/list, schemas, 2 tool calls). Retries: 2 (events 1–2 above), both recovered in one retry. Output bytes: evidence files 234,503 + 201,449 bytes stored on disk, never in context. Tokens/cost: not measurable from this harness — unavailable.
- Status: completed. No hints/rescues received; no user questions asked; no daemon restarted; no shared checkout or service touched.

BACKLOG STATUS: done
