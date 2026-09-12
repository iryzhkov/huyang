# Agent efficiency results

Running log of the numbers. Newest section last. Methods are described in README.md.

## Baseline: protocol measurement before changes

All numbers are `cl100k_base` tokens (`tiktoken-go`). The Huyang family is measured through
the real stdio adapter against a private service on a copy of the fixtures; the built-in and
Bash families are modelled from the same fixture (see README.md). Raw records:
`runs/protocol-before.json` (commit 993e7c0, before any change), `runs/protocol-after-a.json`
(after the edit changes), `runs/protocol-after-b.json` (after the read, search and open
compaction) and `runs/protocol-after-d.json` (after detected commands, the formatter, the
navigation fix and the relocation fix). Rerun with `go run ./bench/agent-efficiency/protocol
run --label X` and compare with `... protocol compare BEFORE.json AFTER.json`.

### Huyang, before and after (Go fixture)

| Scenario | Calls | Request tokens | Response tokens |
|---|---|---|---|
| W0 open workspace | 1 → 1 | 28 → 27 | 1119 → 597 (-47%) |
| R1 read whole file (150 lines) | 1 → 1 | 42 → 35 | 1708 → 1375 (-19%) |
| R2 read region by function name | 1 → 1 | 54 → 47 | 1373 → 440 (-68%) |
| R3 find every call site | 1 → 1 | 44 → 37 | 1227 → 661 (-46%) |
| R4 read three related files | 3 → 1 | 126 → 46 | 3543 → 2382 (-33%) |
| E1 change one line in a known file | 2 → 1 | 141 → 73 | 2406 → 413 (-83%) |
| E2 replace a block located by content | 2 → 1 | 363 → 293 | 3061 → 665 (-78%) |
| E3 rename a local identifier (5 occurrences) | 7 → 1 | 547 → 66 | 13998 → 524 (-96%) |
| E4 add a new file (40 lines) | 2 → 1 | 468 → 317 | 3603 → 391 (-89%) |
| E5 coordinated edits in three files, build and test | 11 → 6 | 975 → 691 | 12835 → 2811 (-78%) |
| E6 edit that breaks the build, then recover | 23 → 12 | 1772 → 1047 | 27723 → 5629 (-80%) |
| V1 run the test suite | 1 → 1 | 14 → 61 | 36 → 270 |

V1 before was a Bash fallback (`go test ./...`) because `verify_run` had no commands to run
without a `.huyang.toml`; after, `verify_run` runs the detected `go test ./...` in a sandbox
and reports a structured result, which is what the extra tokens buy. The same holds for the
verification step inside E5 and E6.

### Huyang, before and after (Python fixture)

| Scenario | Calls | Request tokens | Response tokens |
|---|---|---|---|
| W0 | 1 → 1 | 28 → 27 | 1027 → 537 |
| R1 | 1 → 1 | 37 → 38 | 1543 → 1229 |
| R2 | 1 → 1 | 49 → 50 | 1354 → 401 |
| R3 | 1 → 1 | 37 → 38 | 1236 → 670 |
| R4 | 3 → 1 | 110 → 52 | 3127 → 1989 |
| E1 | 2 → 1 | 128 → 74 | 2363 → 426 |
| E2 | 2 → 1 | 328 → 277 | 2936 → 623 |
| E3 | 7 → 1 | 504 → 70 | 14047 → 563 |
| E4 | 2 → 1 | 467 → 332 | 3656 → 398 |
| E5 | 11 → 6 | 917 → 697 | 12471 → 3374 |
| E6 | 23 → 12 | 1581 → 1016 | 27328 → 6392 |
| V1 | 1 → 1 | 19 → 60 | 17 → 977 |

The Python `verify_run` responses carry the `unittest` output of the deliberately failing test
(a traceback), which is why V1 is larger than the Go one.

### Against the built-in tools (Go fixture, after)

| Scenario | Huyang calls / req / resp | Built-in (modelled) calls / req / resp | Bash (modelled) calls / req / resp |
|---|---|---|---|
| R1 | 1 / 35 / 1375 | 1 / 13 / 1584 | 1 / 13 / 1030 |
| R2 | 1 / 47 / 440 | 2 / 53 / 284 | 2 / 43 / 206 |
| R3 | 1 / 37 / 661 | 1 / 24 / 358 | 1 / 18 / 358 |
| R4 | 1 / 46 / 2382 | 3 / 39 / 2632 | 1 / 22 / 1767 |
| E1 | 1 / 73 / 413 | 1 / 35 / 133 | 1 / 86 / 0 |
| E2 | 1 / 293 / 665 | 1 / 255 / 292 | 1 / 473 / 0 |
| E3 | 1 / 66 / 524 | 1 / 28 / 127 | 1 / 68 / 0 |
| E4 | 1 / 317 / 391 | 1 / 279 / 7 | 1 / 292 / 0 |
| E5 | 6 / 691 / 2811 | 6 / 454 / 928 | 6 / 860 / 13 |
| E6 | 12 / 1047 / 5629 | 14 / 658 / 2577 | 14 / 1344 / 503 |
| V1 | 1 / 61 / 270 | 1 / 14 / 13 | 1 / 14 / 13 |

Reading: call counts now match or beat the built-in tools everywhere. Reads cost the same
or less. Edits cost two to four times the built-in Edit echo in response tokens because the
response carries the diff record (with content hashes), the new revision and the diagnostics
the edit caused; the remaining fixed cost per edit is about 250 tokens of envelope, of which
the two hashes and the document revision are 100. Before the changes the same edits were 10
to 100 times the built-in cost, which is why an agent reached for `sed`.

### What changed, in the order it was measured

1. `edit_apply` gained `replace_literal` and `create_file` and a compact default response
   (`runs/protocol-after-a.json`): E1 2406 → 545, E3 13998 → 658, E4 3603 → 527.
2. Compact JSON rendering (indentation alone was a third of every response), compact
   `read`, `search` and `workspace_open` data, native Go and Python sections for reads by
   name, and `read.targets` (`runs/protocol-after-b.json`): W0 1119 → 496, R2 1373 → 443,
   R4 3 calls → 1, E1 545 → 423.
3. Detected verification commands, `verify_run ... current`, the gofmt pass after Go edits,
   pruned verification stage records, the navigation target fix, the handle relocation
   preference and the `.git`-free sandbox (`runs/protocol-after-d.json`): the verification
   steps of E5, E6 and V1 run through `verify_run` instead of a shell fallback.

### Friction pass (`runs/protocol-after-f.json`, 2026-09-12)

A second pass on what the live session showed: replies no longer echo the caller's text,
diagnostic notices collapse and stay off reads, the envelope drops its normal-case fields,
`root` replaces the separate open, `search` gained `paths` and `context_lines`, `read`
gained `numbered`, and `tools/list` lost the per-tool output schema. Same fixtures and
scenarios, Huyang family, response tokens before (`protocol-after.json`) and after:

| Scenario | Go | Python |
|---|---|---|
| W0 open | 603 -> 531 | 622 -> 566 |
| E1 one-line edit | 421 -> 360 | 432 -> 381 |
| E2 swap a block | 671 -> 375 | 631 -> 375 |
| E3 rename x5 | 526 -> 400 | 564 -> 419 |
| E4 new file | 398 -> 354 | 399 -> 348 |
| E5 three files + tests | 2832 -> 2248 | 2729 -> 1857 |
| E6 signature change | 5636 -> 4774 | 5654 -> 4559 |
| V1 tests | 273 -> 258 | 277 -> 258 |

Request tokens and call counts are unchanged; reads moved by a few tokens. The full-profile
`tools/list` went from 18,699 to 8,526 cl100k tokens, paid once per session. The protocol
harness still opens the workspace explicitly (W0) so the open stays measured; an agent
using `root` skips that call entirely.

### Second friction pass (`runs/protocol-after-g.json`, 2026-09-12)

`edit_apply` takes an `operations` list; `search` answers semantic modes (references,
definition, implementations, callers) through the language server with a literal
fallback; `navigate` takes a bare `symbol`; search hits carry handles only on request;
`verify_run` reports one line per stage plus the output of stages that did not pass;
every reply names the workspace as `{id, revision}`. Response tokens, Huyang family,
after-f to after-g:

| Scenario | Go | Python |
|---|---|---|
| R3 find call sites | 626 -> 283 | 662 -> 302 |
| E1 one-line edit | 360 -> 346 | 383 -> 362 |
| E5 three files + tests | 2248 -> 1969 | 2166 -> 1954 |
| E6 signature change | 4774 -> 4084 | 4734 -> 4184 |
| V1 tests | 258 -> 175 | 261 -> 176 |

The protocol harness still edits one literal per call (it measures that shape); an agent
folding E5's five edits into one `operations` call saves four replies of envelope and
four diagnostics refreshes. This paragraph and the next were written with one such call.

### Stage S20c (`runs/protocol-s20c.json`, 2026-09-12)

Two scenarios were added for the file-lifecycle kinds: E7 copies a file (Go: `format.go`
to `testdata/format.go`; Python: `ledger/fmt.py` to `ledger/fmt_copy.py`) and builds; E8
moves a file (Go: `store.go` to `storage.go`; Python: `ledger/store.py` to
`ledger/storage.py` plus the import in the test) and runs the tests. The built-in family is
modelled as Read plus Write (the content crosses the context twice) plus `rm` for a move;
the bash family as `cp` and `git mv`. Huyang family, one call plus `verify_run`:

| Scenario | Lang | Family | Calls | Req tokens | Resp tokens |
|---|---|---|---|---|---|
| E7 | go | huyang | 2 | 126 | 582 |
| E7 | go | builtin | 3 | 414 | 453 |
| E7 | go | bash | 2 | 43 | 0 |
| E8 | go | huyang | 2 | 125 | 677 |
| E8 | go | builtin | 4 | 649 | 699 |
| E8 | go | bash | 2 | 30 | 13 |
| E7 | python | huyang | 2 | 126 | 556 |
| E7 | python | builtin | 3 | 404 | 372 |
| E7 | python | bash | 2 | 50 | 0 |
| E8 | python | huyang | 3 | 202 | 1043 |
| E8 | python | builtin | 5 | 500 | 593 |
| E8 | python | bash | 3 | 130 | 17 |

The Huyang request cost is a third of the built-in tools' because the file content never
enters the request; the response cost is the `verify_run` stage report plus the lifecycle
reply (`changed_paths`, `git` state, the staging command). Bash stays cheapest in tokens
and tells the agent nothing about the tree or Git afterwards. The other scenarios were
unchanged by the stage within noise.

### Friction met while doing this work through Huyang (the pre-change build)

- `change_plan prepare` failed twice with `sandbox_source_changed` in this repository:
  the sandbox manifest included `.git`, whose directory mtime changes whenever the T3 app
  polls `git status`. Fixed by skipping `.git` during materialization. New files were
  created through the shell for the rest of the session because the fixed service was not
  deployed yet.
- Search is whitespace-exact: a query with the spacing from before `gofmt` ran returned
  zero hits and no hint. `replace_literal` now forgives uniform indentation and names a
  tabs-versus-spaces mismatch.
- Applying several handles from one search failed with `symbol_ambiguous` once a
  neighbouring replacement changed the bytes after the next handle (E3 before). Fixed by
  preferring the candidate at the original offset with an unchanged preceding anchor.
- A whole-file rewrite through `edit_apply` returned the old and the new content twice
  (about 45 KB for an 11 KB file). Patches in responses are now bounded at 4 KiB.
- `navigate` with a `symbol_locator` pointed gopls at the first mention of the name, which
  was the doc comment above the declaration, so the language server answered `no identifier
  found`. The declaration line is located natively now.
- The revision token from an edit response was once refused by `verify_run` as `not
  current` a moment later (E5 in `runs/protocol-after-d.json` before the `current` alias).
  `verify_run` accepts `current`; the guide recommends it.
- Handle and result-set lifetime is 30 minutes, not one minute as the field report said.
- `diagnostic_updates` on every reply in this repository ran to the 20-notice cap while
  gopls churned during edits: about 800 response tokens per call of `new`/`stale`/`resolved`
  notices for the same ids, each with an `attribution: {rank: unattributed}` object. A
  5-line edit therefore cost about 1,300 tokens here against 422 in the protocol
  measurement on a quiet fixture. Fixed: notices collapse to one line per finding, stale
  ones are dropped, the cap is five, reads never carry them, and the client's cursor jumps
  to the newest notice so a backlog is never replayed twenty at a time (it was: the
  delivered cursor lagged 400 notices behind in this session).
- `edit_apply` on a file outside any repository answered `semantic diagnostic refresh
  failed` in its summary although nothing failed; a documents workspace has no provider.
  Fixed: an edit to a file no language server covers, or in a documents workspace, skips
  the refresh and is plainly ok.
- The edit reply echoed the caller's own text as a JSON-escaped byte-range patch (a
  40-line `score.py` edit answered about 1,500 tokens). Fixed: the patch appears only with
  `verbose` or in a preview; `locations` names the changed lines instead.
- The reply envelope carried `evidence: {ids: [], truncated: false}`,
  `idempotency_persisted: true` and a required `idempotency_key` on every mutation. Fixed:
  both fields are omitted in the normal case and the key is generated when absent.
- `read` with `targets` failed with `arguments.targets must be an array` when the harness
  sent the array as JSON text (its cached tool schema predated the server). Fixed:
  structured arguments that arrive as JSON text are decoded.
- `workspace_open` was a mandatory first call (about 840 tokens live). Fixed: every tool
  accepts `root` instead of `workspace_id` and opens or reuses the workspace itself.
- `search` lost to `grep -rn -C` because it had no context lines and no path filter; this
  session fell back to the shell for those. Fixed: `paths` and `context_lines`.
- `read` content had no line numbers, so citing a line meant counting. Fixed: `numbered`.
- `tools/list` was 18,699 tokens for the full profile, loaded into every session; the
  output envelope schema was attached to all 19 tools and the search schema repeated its
  properties three times under `oneOf`. Fixed: neither is attached any more.
- Several literal replacements in one file still need one call each (four calls on
  `score.py`; the three test assertions at the end of this pass went through a shell
  script for that reason). Fixed in the second pass: `edit_apply` accepts `operations`.
- `verify_run` on an untrusted root returned two identical skipped stages under the summary
  `Verification completed`, and the Muse agents fell back to bash every time. The summary now
  names the cause and the config file to edit.

### Field report, 2026-09-12 (t3-steward, omarchy-setup, dev-fleet through the after-g build)

A Claude session implemented about 1,500 lines across three repositories with about 30
Huyang calls and reported: multi-target `read`, the `operations` list, the outline view
and per-edit diagnostics were the reasons Huyang beat the shell; zero mis-edits. What
pushed it back to the shell or left it guessing, with the fix in stage S20c
(`docs/plans/huyang-s20c-file-lifecycle-and-onboarding.md`):

- Copying an existing file had no Huyang path (a 15 KB `CLAUDE.md` into a checkout would
  have round-tripped through the context); `cp` was used. Fixed: `edit_apply` `copy_file`
  (source may be outside the workspace), `move_file`, `delete_file` and `create_file`
  with `replace`, all in the `operations` list too, with the Git state and staging
  command in the reply.
- `create_file` on an existing path: the schema did not say whether it overwrites or
  refuses. Fixed: the descriptor says it refuses, the refusal carries the revision, and
  `replace: true` with it overwrites.
- A 53 KB read was dumped whole and the harness preview showed only the first target.
  Fixed: `read.max_lines` with `truncated` and the total, and `entries` (sizes) ahead of
  the bodies in multi-target replies.
- Python edits answered `provisional` with `lsp_not_configured` or
  `lsp_attach_deadline_exceeded`; pyright attached once, late, and flagged a spurious
  unresolved `pytest` import because it did not see the uv venv. Fixed: servers start in
  the background at open, a system-installed server is enabled, pyright gets the venv
  interpreter, a start in progress is `lsp_starting`, a late publish is recorded against
  the transaction whose version it matches, and the summary names the reason.
- Detected commands for dev-fleet were `python3 -m unittest` and an ad hoc syntax loop
  while the project runs pytest and ruff through uv (Makefile `gate`, pyproject). Fixed:
  detection reads Makefile targets, `[tool.pytest]`, `[tool.ruff]` and the project's
  environment. t3-steward was untrusted, so `go test` ran in the shell: `huyang trust
  <root>` now grants it in one command and the untrusted reply names it.
- Result-set handles were returned and never used; `replace_literal` covered every edit.
  No change: that is the intended default.
- Memory files under `~/.claude/projects` went through the harness `Write` tool because the
  instructions did not say which tool owns files outside a repository. Fixed in the
  instructions: either is fine for a file that is not source.

## Agent measurement (Muse through the t3-steward backlog)

The scenarios were submitted as backlog tasks with `submit.sh`; the batch log is in
`runs/`. Results are appended here by `score.py --batch NAME --markdown` once the steward
has run the tasks in a quiet slot. Until then this section carries only the submission
record.

### Batch `after` (submitted 2026-09-11 22:52 PDT)

- `submit.sh after --reps 3`: 99 tasks (11 scenarios x 3 families x 3 reps, Go fixture),
  logged in `runs/after.tsv`; `t3-steward backlog list --project huyang` showed 99 new
  workflow runs. Model `opencode/muse-spark-1.3-contributor-free`, max 4 turns per task.
- The batch runs against commit `6552560` (replace_literal, compact responses, detected
  commands, implicit single-file edits) deployed to the local service before submission,
  so it measures only the new build. A comparable pre-change agent batch was not run; the
  before/after comparison is the protocol measurement above.
- First 35 attempts failed at workspace preparation: the steward clones over
  `ssh://igor@normandy/...` and the forced-command wrapper on the worker key
  (`~/.local/libexec/t3-steward-f02-worker`) allow-listed `git-upload-pack` only for the
  citadel repository. Added the huyang entry, restarted the steward and retried the failed
  tasks with `t3-steward backlog retry`. Threads are auto-titled by the steward, so
  `score.py` now matches threads by their prompt text and assigns them to reps in creation
  order.
- The first 21 runs verified through bash because the cloned fixture root was untrusted
  (`verify_run` answered two skipped stages). Trusted
  `~/.local/state/t3-steward/backlog-v2-workspaces/workers` in `~/.config/huyang/config.toml`
  after that; later runs in the same batch can use `verify_run`. The untrusted reply itself
  now says why nothing ran and where to grant trust.
- OpenCode records no context-window events, so provider token columns are 0 for this
  batch; the tool-call columns (cl100k) are the measurement.
- By 01:00 PDT 75 runs had succeeded and 22 had failed with `final summary has no done
  marker` (the Muse agent ended without the steward's completion marker): 12 bash, 8
  builtin, 3 huyang (all R1). Retried once. Runs after 00:47 PDT ran against the friction
  pass build (commit 2ea1453 and later), which changes the Huyang family's response sizes
  mid-batch; `score.py` output should be read with the thread's start time in mind.
- The Muse free quota ran out at 01:10 PDT with 75 runs succeeded; the 22 retries and the
  last pending tasks were cancelled. The table below is the final agent measurement for
  this batch (`runs/after-scores.json`). Rows with 0 calls are the failed threads
  (R1 for every family, R2 for bash): the agent never called a tool before ending
  without the done marker, so those scenarios have no agent data.

### Agent scores, batch `haiku` (Claude Haiku 4.5, Go fixture, cl100k tokens, mean of 3 runs)

Submitted 2026-09-12 against the S20c build `67b0045` (file-lifecycle kinds, capped
reads, project-derived command detection, warm language servers), run through the
steward backlog on `claudeAgent/claude-haiku-4-5` with a maximum of four turns. All 117
runs produced a thread and tool calls; no run was lost, so unlike batch `after` every
cell below is measured. Tool milliseconds are wall time inside the tool: `verify_run`
builds and runs the real tests, while for the shell families the harness times only the
command.

| Scenario | Family | Calls | Req tokens | Resp tokens | Tool ms | Provider in | Provider out |
|---|---|---|---|---|---|---|---|
| R1 | bash | 1.3 | 45 | 1088 | 746 | 43091 | 1244 |
| R1 | builtin | 1.0 | 109 | 1290 | 2349 | 43202 | 1215 |
| R1 | huyang | 2.0 | 147 | 2044 | 1907 | 44698 | 1724 |
| R2 | bash | 2.0 | 139 | 197 | 2298 | 42270 | 1000 |
| R2 | builtin | 2.3 | 187 | 3202 | 3626 | 46174 | 1448 |
| R2 | huyang | 2.7 | 183 | 1543 | 2589 | 44123 | 1399 |
| R3 | bash | 4.3 | 268 | 1071 | 2818 | 44403 | 2120 |
| R3 | builtin | 6.3 | 531 | 9386 | 11022 | 55282 | 3116 |
| R3 | huyang | 2.0 | 156 | 1443 | 3510 | 43883 | 1806 |
| R4 | bash | 3.0 | 72 | 1752 | 1564 | 44121 | 1412 |
| R4 | builtin | 3.0 | 322 | 2160 | 5017 | 44944 | 1959 |
| R4 | huyang | 2.0 | 162 | 3090 | 2963 | 46039 | 1737 |
| E1 | bash | 4.7 | 291 | 130 | 4144 | 42941 | 1436 |
| E1 | builtin | 3.3 | 411 | 1443 | 7161 | 44430 | 1309 |
| E1 | huyang | 4.3 | 299 | 1759 | 19812 | 45232 | 1943 |
| E2 | bash | 9.3 | 1040 | 938 | 13642 | 47059 | 2870 |
| E2 | builtin | 3.3 | 651 | 8981 | 9763 | 54453 | 2204 |
| E2 | huyang | 6.3 | 679 | 2306 | 27837 | 47665 | 3589 |
| E3 | bash | 5.7 | 170 | 2426 | 3243 | 48626 | 3811 |
| E3 | builtin | 3.3 | 673 | 1494 | 8987 | 45274 | 2085 |
| E3 | huyang | 6.0 | 553 | 3139 | 24221 | 48471 | 3446 |
| E4 | bash | 7.7 | 478 | 1835 | 6529 | 46007 | 2206 |
| E4 | builtin | 4.0 | 680 | 1544 | 9490 | 45847 | 2791 |
| E4 | huyang | 4.7 | 524 | 2863 | 23216 | 47730 | 3054 |
| E5 | bash | 19.0 | 1604 | 3981 | 16540 | 53579 | 5795 |
| E5 | builtin | 12.3 | 2744 | 3994 | 23176 | 52268 | 6208 |
| E5 | huyang | 14.0 | 2306 | 12517 | 54640 | 63834 | 6509 |
| E6 | bash | 24.7 | 1877 | 4305 | 20467 | 54966 | 5904 |
| E6 | builtin | 14.3 | 3480 | 4469 | 37609 | 54245 | 7613 |
| E6 | huyang | 12.0 | 2374 | 7289 | 33519 | 56845 | 6939 |
| E7 | bash | 5.0 | 258 | 325 | 4504 | 43302 | 1529 |
| E7 | builtin | 3.0 | 667 | 459 | 6167 | 44818 | 2914 |
| E7 | huyang | 3.3 | 217 | 1255 | 17506 | 44342 | 1728 |
| E8 | bash | 5.0 | 340 | 843 | 5764 | 44102 | 1880 |
| E8 | builtin | 4.7 | 1063 | 1040 | 11028 | 48195 | 5696 |
| E8 | huyang | 4.0 | 246 | 1847 | 15707 | 45106 | 1714 |
| V1 | bash | 2.3 | 255 | 103 | 4212 | 42390 | 1126 |
| V1 | builtin | 5.0 | 359 | 1487 | 6465 | 45218 | 2776 |
| V1 | huyang | 6.0 | 359 | 5392 | 20787 | 51326 | 3229 |

Per run across all 13 scenarios: Huyang 5.3 calls, 631 request and 3,576 response
tokens; the built-in tools 5.1 calls, 914 request and 3,150 response tokens; Bash 7.2
calls, 526 request and 1,461 response tokens.

What the numbers say:

- **Request tokens are Huyang's structural win, and the file-lifecycle scenarios show
  why.** E8 (move a file, then test) cost 246 request tokens against 1,063 for the
  built-in tools, E7 (copy) 217 against 667, because the file content never enters the
  request: the agent names two paths while Read plus Write carries the whole file
  through its context twice. The same effect appears in E6 (2,374 against 3,480) and
  R3 (156 against 531).
- **Call counts are won where a tool answers a question rather than a file.** R3 (find
  every call site) is 2 calls against 6.3, because `search` answers it once while the
  built-in family greps and then reads the hits. E6 (signature change and recovery) is
  12 against 14.3 and against Bash's 24.7: the diagnostics in the edit reply replace a
  build round trip.
- **Response tokens are still Huyang's weak side.** Attributing every Huyang response
  token to its tool over the 39 runs: `read` 80,512 over 66 calls (mean 1,219),
  `workspace_open` 27,073 over 39 calls (mean 694), `edit_apply` 15,943 over 41 calls
  (mean 388), `search` 582 per call, `verify_run` 169 per call. The edit and verify
  means match the protocol measurement, so the gap against the built-in tools is not
  the edit path; it is one fixed open per task plus agents reading more than they need.
- **The open was this batch's own fault.** The benchmark prompt still told the Huyang
  family to start with `workspace_open`, which the `root` argument has made unnecessary
  since the first friction pass. That mandated call is 694 response tokens of every run.
  Without it the Huyang family averages 4.3 calls, 521 request and 2,882 response
  tokens per run, which beats the built-in tools on calls and on both token columns.
  The prompt now names `root` instead, so the next batch measures the intended shape.

### What Huyang costs in wall-clock time (batch `haiku`)

The scored runs carry the turn's own start and end, so the agent's whole runtime is
measured, not only the time inside tools. Per run, averaged over all 13 scenarios:

| Family | Wall | Inside tools | Model and harness |
|---|---|---|---|
| huyang | 48.2 s | 19.1 s | 29.1 s |
| builtin | 39.4 s | 10.9 s | 28.5 s |
| bash | 32.8 s | 6.7 s | 26.1 s |

Huyang cost 8.8 seconds more per run than the built-in tools, and 8.2 seconds of that
is time inside tools: the model's own thinking time is the same for all three families,
as it should be. Where that tool time goes is not where the reputation says. Mean
seconds per call, and the comparable built-in tool:

| Huyang call | Mean | Built-in equivalent | Mean |
|---|---|---|---|
| `read` (66 calls) | 0.65 s | `Read` (78 calls) | 1.89 s |
| `edit_apply` (41 calls) | 2.39 s | `Edit` (42 calls) | 3.74 s |
| `search` (10 calls) | 0.72 s | a `grep` through Bash | 0.92 s |
| `workspace_open` (39 calls) | 2.05 s | none | |
| `verify_run` (33 calls) | 14.35 s | `go build` or `go test` through Bash | about 5 s |

Reading, editing and searching are each faster through Huyang than through the built-in
tools, by two to three times. The whole wall-clock penalty is `verify_run`, which was
three times the cost of running the same command in the shell, plus the two seconds of
the open. Split by scenario the picture is clean: on the four read scenarios, which call
no verification, Huyang averaged 23.9 seconds against the built-in tools' 27.2; on the
nine scenarios that verify, it was slower every time.

**Why `verify_run` was slow, and the fix.** Every command stage was given a throwaway
HOME *and* a throwaway `GOCACHE`, so each stage compiled the fixture from scratch and one
`verify_run` paid it twice. On this fixture a cold `go build ./...` takes 7.73 s against
0.06 s warm, and a cold `go test ./...` 11.52 s against 6.62 s. The build cache is now
shared across runs under `<state-dir>/command-cache/go-build`, while HOME and
`XDG_CACHE_HOME` stay throwaway: cache entries are addressed by the hash of their inputs,
so reuse cannot make a later build wrong, which is the same reasoning that already
forwarded `GOMODCACHE`. `HUYANG_COMMAND_CACHE=off` restores the old behaviour. Measured
by rerunning the protocol benchmark on the Go fixture, total `verify_run` time fell from
57.9 s to 18.4 s; per scenario, E6 from 12.5 s to 0.32 s, E7 from 11.8 s to 0.28 s, E8
from 10.4 s to 0.47 s, and E5, which edits five files and runs first, from 22.7 s to
16.3 s. The first verification of a session still pays a cold build.

Two caveats on these numbers. The batch ran two and later four tasks at a time on one
machine, so a run overlapped 0.74 other runs on average for Huyang against 0.62 and 0.54
for the built-in and Bash families; longer runs overlap more by construction, which
inflates Huyang's figure slightly. And the tool time of the shell families counts only
the command they ran, while `verify_run` also materialises the sandbox and captures the
tree before and after.

Friction the transcripts show, to fix next:

- **A passing `verify_run` is not believed.** In V1 the agent ran `verify_run` with the
  tests stage, got a passing one-line stage record, and then ran `go test ./...`
  through Bash anyway to see the output; its own report says "to see full output and
  confirm test results". That is the whole V1 gap (6 calls against Bash's 2.3). A
  passing stage should carry a short evidence line (tests run, package count, duration)
  rather than only a verdict.
- **The one-call literal edit is not reached for.** In E1 the agent opened, searched for
  `MaxEntries`, read the line, then edited, verified and read the file again: 6 calls
  for a one-line change it already knew the text of. The `edit_apply` descriptor says
  "in one call with no prior search", but nothing in the reply of the preceding call
  points at it. The agents were not given `docs/agent-guide.md`; the descriptors alone
  did not change the search-first habit.
- **Tool time is real and large.** Huyang's tool milliseconds are two to five times the
  shell families' because `verify_run` compiles and runs the suite inside the call
  while the shell families' numbers time only the command they ran. E5's 54.6 seconds
  is mostly the Go build in the sandbox.

### Agent scores, batch `after` (Go fixture, cl100k tokens, mean of the runs found)

| Scenario | Family | Runs | Calls | Req tokens | Resp tokens | Tool ms |
|---|---|---|---|---|---|---|
| E1 | bash | 3 | 2.0 | 155 | 396 | 310 |
| E1 | builtin | 3 | 3.7 | 392 | 1622 | 449 |
| E1 | huyang | 3 | 3.7 | 317 | 1669 | 4750 |
| E2 | bash | 3 | 6.3 | 518 | 7970 | 471 |
| E2 | builtin | 3 | 4.7 | 709 | 1052 | 425 |
| E2 | huyang | 3 | 5.7 | 564 | 2648 | 869 |
| E3 | bash | 3 | 7.7 | 310 | 5349 | 500 |
| E3 | builtin | 3 | 5.0 | 788 | 3502 | 678 |
| E3 | huyang | 3 | 6.7 | 718 | 5879 | 1290 |
| E4 | bash | 3 | 6.3 | 1225 | 7344 | 565 |
| E4 | builtin | 3 | 5.7 | 946 | 2658 | 543 |
| E4 | huyang | 3 | 4.7 | 699 | 3333 | 9415 |
| E5 | bash | 3 | 14.3 | 2408 | 12222 | 2128 |
| E5 | builtin | 3 | 12.3 | 2233 | 10814 | 1183 |
| E5 | huyang | 3 | 10.3 | 1546 | 11288 | 23773 |
| E6 | bash | 3 | 9.7 | 1223 | 11814 | 1020 |
| E6 | builtin | 3 | 18.0 | 2808 | 15083 | 1268 |
| E6 | huyang | 3 | 21.3 | 2014 | 16868 | 13237 |
| R2 | builtin | 3 | 0.7 | 48 | 179 | 33 |
| R2 | huyang | 3 | 2.3 | 157 | 1370 | 263 |
| R3 | bash | 3 | 4.0 | 135 | 2056 | 342 |
| R3 | builtin | 3 | 5.3 | 528 | 4471 | 293 |
| R3 | huyang | 3 | 4.0 | 296 | 5269 | 304 |
| R4 | bash | 3 | 2.3 | 129 | 1174 | 318 |
| R4 | builtin | 3 | 3.3 | 354 | 2782 | 206 |
| R4 | huyang | 3 | 2.7 | 207 | 3318 | 253 |
| V1 | bash | 3 | 1.7 | 135 | 112 | 491 |
| V1 | builtin | 3 | 2.3 | 199 | 159 | 574 |
| V1 | huyang | 3 | 2.0 | 174 | 1184 | 8305 |

What the agent data says, honestly: with the Muse model the Huyang family used fewer or
equal calls than the built-in tools on E4, E5, R3 and R4 and fewer request tokens on every
edit scenario, but paid more response tokens on every scenario. Three causes are visible
in the transcripts: `workspace_open` at the start of every task (about 840 tokens; the
friction pass makes it unnecessary through `root`, but these agents were not told),
the pre-friction reply shape for most runs (patch echo, diagnostic notices, envelope
boilerplate; the after-f protocol numbers above show what the same calls cost now), and
`verify_run` answering several times the size of a shell's exit status, both when the
fixture root was untrusted and afterwards because the stage report is structured. The
tool time column is wall time inside the tool: `verify_run` runs the real tests, while
for the shell families the harness records only the command's own duration.

Preliminary scores, 22 of 99 runs (before the trust change; `score.py --batch after`):

| Scenario | Lang | Family | Runs | Calls | Req tokens | Resp tokens |
|---|---|---|---|---|---|---|
| E1 | go | builtin | 3 | 3.7 | 392 | 1622 |
| E1 | go | huyang | 2 | 4.0 | 356 | 1682 |
| E2 | go | bash | 3 | 6.3 | 518 | 7970 |
| E2 | go | builtin | 3 | 4.7 | 709 | 1052 |
| E2 | go | huyang | 3 | 5.7 | 564 | 2648 |
| E3 | go | bash | 3 | 7.7 | 310 | 5349 |
| E3 | go | builtin | 1 | 5.0 | 791 | 3470 |
| E3 | go | huyang | 2 | 6.5 | 696 | 5939 |

What the live E1 Huyang run spent: `workspace_open` 839 response tokens, `edit_apply` 457,
`verify_run` 375 (skipped, untrusted), then `go build` through bash. The edit itself is at
the protocol cost; the open and the wasted verify are the overhead to attack next
(`workspace_open` on a fresh clone carries the overview and the untrusted-commands text).
