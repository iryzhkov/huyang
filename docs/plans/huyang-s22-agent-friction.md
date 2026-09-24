# S22 — Iron out the friction agents hit in the field

Status: approved 2026-09-24; in progress on `fix/huyang-kinks`
Prepared: 2026-09-24
Base: `dd7dafc` (GitHub `main`, the deployed build `0.4.0/dd7dafc8f287`)

## Sources

- Tool-friction spool, 2026-09-17 to 2026-09-24: 13,035 agent calls in 54 sessions on
  homelab, normandy and omarchy-pc, 5.3% not ok (`toolfeedback report --source huyang`).
- Jocasta `huyang/feedback/steward-runtime-session-2026-09-21.md`
  (`jocasta:79f677d6698a4ffab20e945c4094f63e@1`, findings H1 to H5), its qualification
  `jocasta:1be710f2a7009a8302bc2632a77bcae5@1`, and the ranking suggestion
  `jocasta:f1efb8e6869fae2839d4d1705dbff2c7@1`.
- GitHub issues #1 to #8, filed from the 2026-09-14 weekly digest and all still open.

Usage over the same week shows where the value is: `read`, `search` and `edit_apply` are
93% of calls, while `navigate`, `verify_run` and `diagnostics` together are under 1%.
Agents use Huyang as a revision-guarded replacement for grep, cat and patch. This stage
therefore favours making those three tools, and the failures around them, dependable.

## What the field data says

The clusters that leave agents stuck, most frequent first:

| Cluster | Calls | Cause |
|---|---|---|
| a9f941, 7a9624, b48f40 | 205 | A read of a path that does not exist, mostly a guessed path. The reply claims repository-wide absence even when the listing was capped, and loses the candidates in multi-target reads. |
| 285f8b | 103 | `context_lines` above 20. The bound is in the schema, so some clients do not show it to the model. |
| 86f43e, 8fd87b, a1d9e8 | 41 | Unknown properties (`path`, `symbol`, `max_results`, `path_prefix`). Search's `path` is pointed at `refine.path`, which is wrong. |
| 44c607, 3ea186, ccd461 | 77 | `replace_literal` text not found or not unique. These guardrails work; recovery is 90%. Out of scope. |
| a1c47a | 10 | "must match exactly one allowed shape" with no hint of which shape was meant. |
| c92f17 | 11 | Semantic search with an ambiguous name tells the agent to use `target.symbol_locator`, which search does not accept, and ignores `paths`. |
| revision_diff | 15/19 | Gaps from external writes are returned as partial with no diff; `current`, `HEAD` and reversed ranges are refused. |
| change_plan | 32/119 | An omitted `plan_revision` means "current" only for inspect (#1, #8); prepare's conflict message hides the conflicts; the 8-operation limit's hint is attached to every tool's arrays. |
| diagnostics | 6/22 | An empty evidence store is `unavailable`, never an answer (#4). |
| symbol_find | 5/93 | Any skipped file, even a binary, makes a name that exists nowhere "could not establish" (#2). |

H1, the scoped search that finds nothing in a workspace over 2,000 files, is rare in the
spool but it is the one that yields a wrong answer rather than a refusal, so it ranks high.

Schema validation failures bypass the Huyang envelope entirely: `registerModernTool`
returns a Go error, the SDK turns it into bare text, and the agent gets no code, no
`next` and no request ID (H3).

## Principles

- A refusal must say what to send instead. Every argument error names the property to use.
- An answer beats a refusal whenever the answer can be honest: degrade with a warning and
  exact coverage rather than fail.
- Never claim more coverage than was inspected, and never substitute a different path in
  an edit.
- Unambiguous aliases may be accepted, with a warning that names the canonical spelling,
  because the warning teaches the agent at no extra call.

## Waves

### W1 — Arguments and the error envelope

1. Validation failures return a normal Huyang envelope: outcome `failed`, code
   `invalid_arguments`, the offending argument path in `data`, and a `next` that restates
   the correct shape. One helper in `internal/service/server.go` replaces the four
   `return nil, err` sites.
2. "Did you mean": `validateObject` suggests sibling properties by alias table and near
   miss before nested ones (search `path` goes to `paths`, not `refine.path`).
3. Accepted aliases, each with a warning: `read` top-level `path`, `start_line`,
   `end_line` and `symbol_locator` move into `target`; search `path`/`path_prefix` become
   `paths`, `max_results` becomes `limit`; revision_diff `to_revision` becomes
   `to_revision_or_current`.
4. `oneOf` failures name the closest shape and its specific error, or list the shapes.
5. The operations-limit hint is specific to the tool; change_plan's per-request limit
   rises from 8 to 32.
6. `workspace_open` defaults `kind` to `project` when `root` is given and to `documents`
   when only `files` is.
7. `context_lines` above 20 is reduced to 20 with a warning that says so and points at
   `read` with a line window, instead of refusing the call.

### W2 — Scoped search that is actually complete (H1, c92f17)

1. `collectFiles` applies the `paths` scope before the `MaxFiles` cap, so a scoped search
   lists only its scope; `Capped` is set only when an in-scope file was dropped. A scope
   that names an existing file skips the listing. Result-set revalidation lists the same
   way.
2. Semantic modes pass `paths` to symbol resolution; an ambiguous name returns the
   candidates with `next` entries that repeat the search scoped to each candidate file.

### W3 — Missing-path recovery (H2, #6)

1. `workspace.Read` returns a coded `document_not_found` for a missing file, with the
   relative path; every read view routes it through one recovery helper.
2. The helper lists files without statting them, offers exact-basename candidates first
   and then near matches (same suffix, small edit distance), and words absence within
   the listing's coverage ("none among the 2,000 files listed").
3. A symbol read of a missing file reports the missing file, not "No parser covers".
4. Multi-target reads keep each failed target's candidates and hoist the first `next`.

### W4 — Tools that fail instead of degrading

1. change_plan: an omitted or zero `plan_revision` resolves to the current revision for
   every action (#1, #8); an edit of a prepared plan whose sandbox is gone falls back to
   OPEN; prepare's conflict message quotes the first conflict; parser verification
   names the failing path and line.
2. revision_diff: `current` is accepted on both sides, reversed ranges are swapped,
   `wsrev_0` clamps to 1; a gap that reaches the current revision is filled by diffing the
   last receipt's bytes against disk (marked inferred), and the reply is `ok` when every
   gap was filled (#5).
3. diagnostics: an empty store asks the provider once, then answers `ok` with no findings
   and incomplete coverage instead of `unavailable` (#4).
4. verify_run `test_scope=affected`: when receipts do not cover the range, the affected
   files come from Git (changed and untracked), with a warning; full scope only if Git
   fails.
5. symbol_find: only skipped files the sectioner could have parsed reduce coverage; zero
   hits with a clean literal fallback is `ok`, "0 symbols" (#2). Provider incompleteness
   folds into coverage.

### W5 — Language-server readiness (H5, first step)

`lsp_starting` and `lsp_attach_deadline_exceeded` become `unavailable`, retryable, with a
`next` that retries or checks `language_server_status`, rather than bare `failed`. The
cold and warm Rust measurement stays open; it needs the large Codex checkout.

## Deferred

- Query-time ranking of search hits (the 2026-09-23 suggestion). The display-only step in
  `searchSource` is cheap, but it deserves its own evaluation set; S23.
- `replace_literal` near misses: they work as guardrails today.
- Telemetry: `partial` and `unavailable` are counted as errors by the friction log. After
  W4 fewer of them exist; splitting the columns belongs in toolfeedback.

## Verification

- Each item lands with a test that fails on `dd7dafc`: the refusal pinned today becomes
  the new behaviour, and the frozen catalog fixtures are regenerated deliberately.
- `go test ./...`, `go vet ./...` and the Lua unit tests pass.
- After deployment, the next weekly `toolfeedback report --source huyang` is compared
  with the table above; the target is that no listed cluster keeps a stuck rate above 20%.
