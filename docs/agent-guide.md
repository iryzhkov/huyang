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
  cap) or a `symbol_locator` (Go and Python resolve without a language server). Several
  files: `targets`. `numbered: true` prefixes each line with its number when you need to
  cite or window lines afterwards.
- Need locations: `search` (literal by default; multi-line queries must match whitespace
  exactly). `paths: ["*.go", "internal/"]` scopes the hits and `context_lines: 2` adds the
  numbered lines around each hit, so one search replaces `grep -rn -C2` and the read that
  usually follows it. Hits carry handles that `edit_apply replace_range` accepts, for the
  rare edit whose target you cannot name by content.
- New file: `create_file`. Several files that must change atomically or not at all:
  `change_plan` (prepare, then apply), which also runs the pipeline in a sandbox first.
- Go files are gofmt-formatted after every edit; the response says so under `format`.
  Pass `format: false` to keep bytes exactly.
- Build and test: `verify_run` with `revision_or_transaction: current`. `test_scope:
  affected` runs only the tests whose `covers` patterns match the edited files.
- `verbose: true` on `edit_apply` restores the full change record and handle resolution;
  nothing else needs it.
- A whole-file rewrite through `edit_apply` is bounded (the patch in the response is cut at
  4 KiB) but still costs the content twice in the request; prefer literal edits.
