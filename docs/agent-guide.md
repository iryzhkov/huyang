# Agent guide: the cheapest correct Huyang call

Huyang is the editor an agent should reach for by default, including for a one-line change
in a file it already knows. This page names the call to make for each common operation and
what it costs, so no experimenting is needed. Costs are `cl100k_base` tokens measured by
`bench/agent-efficiency` on the Go fixture after the usability changes of 2026-09-12; the
built-in column models the harness's Read/Edit/Write/Grep tools on the same fixture. The
full tables, the Python fixture and the pre-change numbers are in
[bench/agent-efficiency/RESULTS.md](../bench/agent-efficiency/RESULTS.md).

Every tool below accepts `root` (the repository root) in place of `workspace_id`: the
workspace is opened on the first call and reused afterwards, so a task in a known
repository needs no `workspace_open` at all. Call `workspace_open {kind: project, root}`
only when you want its answer: the top-level overview, the verification commands and
whether `verify_run` may execute them (about 500 tokens). `idempotency_key` is optional on
every mutating call; pass one only when you intend to retry the same call.

The first reply a session gets for a workspace carries a `guide` array: the handful of
rules from this page that decide what a session costs. It is sent once, on whatever call
first reached the workspace, and never repeated for it.

| Operation | Call | Calls | Request tokens | Response tokens | Built-in (modelled) |
|---|---|---|---|---|---|
| Read a whole file (150 lines) | `read {target:{path}}` | 1 | 35 | 1375 | Read: 1 call, 13 / 1584 |
| Read a region by function name in a 900-line file | `read {target:{symbol_locator:{path, name_path}}}` | 1 | 47 | 440 | Grep + Read: 2 calls, 53 / 284 |
| Find every call site of a function | `search {query, mode: literal}` | 1 | 37 | 661 | Grep: 1 call, 24 / 358 |
| Read three related files | `read {targets:[{path},{path},{path}]}` | 1 | 46 | 2382 | Read x3: 3 calls, 39 / 2632 |
| Change one line in a known file | `edit_apply {operation:{kind: replace_literal, path, old, new}}` | 1 | 73 | 360 | Edit: 1 call, 35 / 133 |
| Replace a block located only by content | `edit_apply replace_literal` with the block as `old` | 1 | 293 | 375 | Edit: 1 call, 255 / 292 |
| Rename a local identifier (5 occurrences) | `edit_apply replace_literal {path, old, new, expected_count: 5}` | 1 | 66 | 400 | Edit replace_all: 1 call, 28 / 127 |
| Add a new file (40 lines) | `edit_apply {operation:{kind: create_file, path, content}}` | 1 | 317 | 354 | Write: 1 call, 279 / 7 |
| Coordinated edits in three files, then build and test | 5 x `replace_literal` + `verify_run {stages:[check, tests], revision_or_transaction: current}` | 6 | 691 | 2248 | Edit x5 + Bash: 6 calls, 454 / 928 |
| Edit that breaks the build, then recover | `replace_literal` (diagnostics arrive in the response), `search` for callers, `replace_literal` per caller, `verify_run check` | 12 | 1047 | 4774 | Edit + Bash + Grep + Edit x9 + Bash: 14 calls, 658 / 2577 |
| Run the test suite | `verify_run {stages:[tests], revision_or_transaction: current}` | 1 | 61 | 258 | Bash: 1 call, 14 / 13 (passing) |
| Copy a file, then build (E7) | `edit_apply {operation:{kind: copy_file, from, to}}` + `verify_run check` | 2 | 126 | 582 | Read + Write + Bash: 3 calls, 414 / 453 (the content crosses the context twice); Bash `cp`: 2 calls, 43 / 0 |
| Move a file, then test (E8) | `edit_apply {operation:{kind: move_file, from, to}}` + `verify_run tests`; the reply names the `git add` | 2 | 125 | 677 | Read + Write + Bash `rm` + Bash: 4 calls, 649 / 699; Bash `git mv`: 2 calls, 30 / 13 |
| Delete a file you have read | `edit_apply {operation:{kind: delete_file, path, revision_id}}` | 1 | about 50 | about 300 | Bash `rm`: 1 call |
| Overwrite a file you have read | `edit_apply {operation:{kind: create_file, path, content, replace: true, revision_id}}` | 1 | content + 40 | about 350 | Write: 1 call |
| Read a big file without a blind dump | `read {target:{path}, max_lines: 200}` then `view: outline` or a window | 1 or 2 | 40 | capped | Read: 1 call, whole file |

E7 and E8 are measured (`runs/protocol-s20c.json`, 2026-09-12); the delete and overwrite
rows are estimates from the same reply shapes, which carry no content.

What the table says:

- Reads cost the same as the built-in tools or less: a whole file is cheaper than Read
  (no line-number prefixes), a region by name is one call instead of two, and several
  files are one call.
- Edits are one call each, the same count as Edit. The response carries the locations
  that changed, the content hashes, the new revision and the diagnostics the edit caused,
  and nothing you sent: no patch unless `verbose`. The diagnostics are the reason to
  prefer Huyang: a compile error shows up in the edit response, before any build. Before
  these changes the same edits cost 2 to 23 calls and 10 to 100 times the tokens.
- Reads and searches never carry diagnostic notices; only edits, `verify_run` and
  `workspace_inspect` do, collapsed to one line per finding and capped at five. An edit to
  a file that no language server covers (Markdown, config, prose) is plainly `ok`.
- `verify_run` replaces the shell for builds and tests when the workspace has commands
  (declared in `.huyang.toml` or detected from `go.mod`, `pyproject.toml`, `package.json`)
  and the root is trusted. Its response is bigger than raw command output because it is
  structured per stage; when the tests fail the output is the same text a shell shows.

## Files outside a repository

A config file, a shell script, a note or a lone source file needs no `workspace_open`:
`read {target:{path}}` and `edit_apply` (`replace_literal` or `create_file`) with an
absolute `path` and no `workspace_id` open an exact one-document workspace on the spot,
run the same guards and diagnostics, and return the workspace id for further edits of that
file. `create_file` creates missing parent directories. Only `replace_range` needs an
existing workspace, because its handle comes from one.

## Rules of thumb

- Know the text you are changing: `replace_literal`. Do not search first. If the text
  occurs more than once, pass `expected_count` or add surrounding lines to `old`; the tool
  refuses any other count and lists the locations, changing nothing.
- Indentation mismatch is forgiven when it is uniform (the whole block one level off); a
  tabs-versus-spaces mismatch is refused with the exact document text in `data.actual`, so
  the retry is one call.
- Need to see code: `read` with a path, a line window (`start_line`, `end_line`, no size
  cap) or a `symbol_locator` (Go, Python, Markdown headings and TOML tables resolve
  without a language server, so a section of a long document is one call:
  `{path: "docs/plan.md", name_path: "Waves/Wave 2"}`). Several
  files: `targets`. `numbered: true` prefixes each line with its number when you need to
  cite or window lines afterwards.
- A file over about 500 lines: `view: outline` first, then windows, or `max_lines` on
  the read (per call or per target). A capped reply says `truncated`, carries the total
  line count and the line to continue from, and a multi-target reply lists every
  target's lines and bytes under `entries` ahead of the bodies. Every option of a
  single-target read works per target as well, so one call can outline one file and
  window another (`targets: [{path, view: outline}, {path, start_line, end_line}]`).
- `view: outline` works in every language, not only the two the native parser reads:
  Go, Python, Markdown (by heading, nested: `Rules of thumb/Indentation`) and TOML (by
  table: `tool/ruff`) are sectioned natively, without a language server, and any other
  file is outlined from the language server's declarations. The reply is one entry per declaration with its name, kind,
  line range and a handle, which `read {target:{handle}}` and `edit_apply replace_range`
  accept; a declaration can also be read by `symbol_locator` with the name the outline
  gave. A file neither can section answers a handle to the whole document and says
  `text_only`.
- Need locations: `search` (literal by default; multi-line queries must match whitespace
  exactly). `paths: ["*.go", "internal/"]` scopes the hits and `context_lines: 2` adds the
  numbered lines around each hit, so one search replaces `grep -rn -C2` and the read that
  usually follows it. Hits carry handles that `edit_apply replace_range` accepts, for the
  rare edit whose target you cannot name by content.
- Several edits at once: `edit_apply` with `operations: [{kind: replace_literal, ...},
  {kind: create_file, ...}]`. They apply in order, each located against the bytes the
  previous ones left, with one formatter pass, one receipt and one diagnostics refresh. A
  refusal stops the list; the reply names the failed operation and how many were applied.
  An `operations` list outside any repository needs no workspace either: give absolute
  paths and one document workspace is opened over all of them.
- Find references, the definition, implementations or callers of a symbol: `search
  {query: Name, mode: references}` or `navigate {relation, symbol: Name}`. The name is
  resolved to its declaration first (Go and Python natively, other languages through the
  language server), so no path is needed; an ambiguous name lists the declarations. When
  no language server answers, `search` falls back to literal matches and says so.
- Search hits are path, line and text. Add `include_handles: true` only when you will edit
  a hit with `replace_range`.
- New file: `create_file`; it refuses an existing path and names that file's
  `revision_id`, and `replace: true` with that revision overwrites the file in one call.
  Several files that must change atomically or not at all: `change_plan` (prepare, then
  apply), which also runs the pipeline in a sandbox first. The `operations` list is a
  sequence, not a transaction: a refusal stops it and the earlier operations stay.
- Move, copy or delete a file: `move_file {from, to}`, `copy_file {from, to}` (`from` may
  be an absolute path outside the workspace; the reply records its hash and size) and
  `delete_file {path, revision_id}` (or `expected_sha256`; without either the reply
  hands both back so the retry is one call). The bytes never pass through you and are
  never reformatted, so a moved file keeps the exact content Git needs to see a rename.
  Huyang does not write the Git index: the reply's `git` block gives each path's tracked
  state and, when the source was tracked, `next` names the one command to run
  (`git add -A -- old new`); after it `git diff --cached -M` reports the move as a
  rename. Symlinks and directories go through `change_plan`.
- After preparing a plan, the staged code is somewhere you can look: pass `revision:
  "prep_..."` (or `plan_id`) to `read`, `navigate`, `diagnostics` or `code_actions` on the
  experimental profile, and the answer comes from the sandbox and the language server that
  read it, in the paths you know. Once the plan is discarded, applied or edited, the same
  call is refused rather than answered about the canonical bytes.
- Asking a prepared revision for `diagnostics` also answers what the proposal changed:
  `data.delta` carries what is new against the canonical report, what it resolved, and how
  many findings were already there and stayed. Findings are matched by what they say, not
  by the line they sit on, so an edit that moves a warning down seven lines is not two
  findings. Each new one names the operation that caused it, ranked by the evidence:
  `exact` when it landed in bytes that operation wrote, `strong` when it is in a file the
  operation targeted, `likely` when the snapshot connects its file to one, `ambiguous`
  when several are equally plausible, `unattributed` when nothing supports a claim. A
  finding the formatter caused is attributed to the formatter. `baseline_complete: false`
  means the comparison could not be made at all: the workspace held no current evidence
  about those files, so nothing in the report can be called new.
- State what the result must satisfy and let the prepare check it:
  `change_plan {action: prepare, operations, invariants: [{id: "clean", kind:
  "no_new_diagnostics"}]}` on the experimental profile. The kinds are
  `no_new_diagnostics`, `tests_pass`, `api_compatible`, `no_references`,
  `symbol_exists`, `symbol_absent` and `path_unreachable`; the ones about a
  declaration name it in `scope.symbol`. `api_compatible` reads each affected
  file's exported surface before and after (Go through the compiler's parser,
  TypeScript through its export forms) and calls a removal, a rename, a changed
  signature, a changed member set or a moved barrel export breaking.
  `path_unreachable` can refute reachability from one remaining reference but
  cannot yet prove it, so it answers violated or unknown. Each one is answered against the prepared revision and is
  `proven`, `violated` or `unknown`, and unknown is not a pass: a required
  invariant that nobody could evaluate keeps the plan out of READY exactly as a
  violated one does. A required invariant has no accept flag, so a plan that
  fails one is not applied and the reply offers inspect and discard rather than
  apply; declare `enforcement: "advisory"` for an assertion that should be
  reported and acknowledged rather than enforced. Editing the plan drops every
  answer, because they were answers about other bytes.
- What a change means rather than which lines moved: `change_plan {action:
  inspect, plan_id, view: "semantic"}` on the experimental profile, for a plan
  that is previewed, prepared or already applied. It answers with an index of
  every affected file, the declarations added, removed, renamed, moved or
  changed in shape, the exported-surface breaks, the imports gained and lost,
  the prepared diagnostic delta, what the tests did, the plan's invariants, the
  gaps and what to do about them. It says "no semantic change" only when the
  coverage behind that claim is complete. `revision_diff {view: "semantic"}`
  answers the same way for a canonical revision range, from the plans applied
  inside it; a step made by a direct edit is a gap, because its receipt keeps
  hashes rather than content.
- Before changing a declaration, ask what it reaches: `change_plan {action: preview,
  plan_id, plan_revision, view: "impact"}` answers who calls it today, whether it is
  exported, which tests are associated with the files, what generated or configuration
  files are involved, and what no contributor could see. It needs the experimental profile
  (`huyang mcp --profile experimental`), because the frozen schemas do not advertise
  `view`. It writes nothing.
- Refactors the language server owns are `change_plan` operations: `rename_symbol`
  (`content` is the new name), `apply_code_action` (`content` is the title `code_actions`
  listed), `inline_symbol`, and `safe_delete_symbol`, which refuses and names the call
  sites when anything outside the declaration still refers to it. Each one asks the server
  what it would change and stages that answer as exact ranges, so the plan previews,
  verifies and rolls back like any other; the operations it produced carry `derived_from`.
  Target `rename_symbol` and `safe_delete_symbol` at the declaration (a `symbol_locator`).
  Target `inline_symbol` and `apply_code_action` at the exact text the action applies to,
  which for an inline is the call and not the statement around it: `search {query:
  "Total()", include_handles: true}` and pass the handle of the call you mean. A target
  the server offers nothing for is refused with the list of actions it does offer there,
  so one retry is enough.
- Go files are gofmt-formatted after every edit; the response says so under `format`.
  Pass `format: false` to keep bytes exactly.
- Build and test: `verify_run` with `revision_or_transaction: current`. `test_scope:
  affected` runs only the tests whose `covers` patterns match the edited files. The reply
  is one line per stage (verdict, exit, duration, the files covered, counts) plus the
  output of any stage that did not pass; a stage that passed carries the last few lines
  of its output under `output_tail`, which is where a command says what it did, so there
  is never a reason to run it again in a shell. `verbose: true` restores the full record.
  Without a `.huyang.toml` the commands come from the project: a Makefile's `test`,
  `lint` and `check` targets, `go.mod`, or a Python project's own pytest and ruff run
  through its `.venv`, `uv run` or `poetry run`. An untrusted root runs nothing and the
  reply says so; `huyang trust <root>` on the machine grants it.
- A provisional edit verdict names what is missing: no server configured, the server
  not installed, still starting (its findings arrive with the next reply, attributed to
  this edit), or none attached within the wait. Servers start in the background when a
  project is opened, so this is rare after the first call.
- `verbose: true` on `edit_apply` restores the full change record and handle resolution;
  nothing else needs it.
- A whole-file rewrite through `edit_apply` is bounded (the patch in the response is cut at
  4 KiB) but still costs the content twice in the request; prefer literal edits.
