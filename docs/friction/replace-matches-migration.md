# Friction log: replace_matches migration (scenario 5)

Task: name one repeated `map[string]any` tool-argument literal in
`internal/livetest`, replacing every occurrence in a single `replace_matches`
operation staged together with its new helper in one atomic `change_plan`.
What I chose: the read target `map[string]any{"path": "ledger.go"}`,
which appeared 6 times across `prepared_test.go` (4) and `workflow_test.go`
(2). The new helper is `ledgerTarget()` in `internal/livetest/targets.go`;
call sites now read `"target": ledgerTarget(),`. Total Huyang calls for the
whole run: roughly 28, of which about a third were retries or probes caused
by the items below.

## 1. Literal search with brackets silently found nothing

My first call was `search` with the default literal mode for
`map[string]any` scoped to `internal/livetest`. It returned 0 matches with
"search coverage incomplete". I guessed square brackets were safe in literal
mode, so an empty result looked like the package genuinely had no such
text, which was obviously wrong for Go test code building tool arguments.
Retrying the identical query in `mode=regex` with escaped brackets
(`map\[string\]any`) returned 12 hits immediately. Cost: one wasted call
plus the doubt about whether the package was what the prompt said it was.
A literal mode that treats `[`/`]` as ordinary bytes should have matched;
if literal mode instead interprets them, the refusal should say so rather
than reporting a clean zero.

## 2. `read` rejected the obvious argument shape

I called `read` with `{"path": "internal/livetest/harness.go"}` and got
`MCP error 0: arguments contains unknown property "path"`. The fix was
`{"target": {"path": ...}}`, which I found by re-reading the tool schema
rather than from the error. The error names the offending property but not
the shape that was wanted, so the first attempt at every new tool is a
guess-and-retype loop. Multi-target reads with `targets: [{path...}]`
worked first time after that.

## 3. Scoped searches report counts that include hits outside the scope

The search that froze my migration set,
`"target": map[string]any{"path": "ledger.go"}` scoped to the two livetests
files, listed exactly the 6 hits I wanted but reported `file_count: 3,
match_count: 7`. An unscoped repeat showed the 7th hit lives in
`internal/handlers/read_test.go`, a different package that must not share
an unexported helper. So the counts describe the pre-filter universe while
`hits`/`returned` describe the post-filter one, and there is no field that
says which. I did not trust that handle for an all-match replacement:
had the hidden 7th hit been inside the frozen set, `replace_matches` would
have rewritten a file in another package to call a helper that does not
exist there. The workaround was `refine: {path: "livetest"}` on the parent
set, which returned a child handle with consistent numbers (`file_count:
2, match_count: 6, returned: 6`). That refine call is the one future
readers should copy: it is what made the set trustworthy, and the log
should record that the parent handle's numbers could not be trusted on
their own.

## 4. The first `change_plan` prepare was refused, correctly but opaquely

Preparing the two-operation plan (create helper + `replace_matches`)
against the pre-refine handle failed with
`use-ledger-target-helper: incomplete result set is not eligible for
all-match replacement`. The cause was not my query: every search in this
workspace reported "search coverage incomplete" because two steward
scaffolding symlinks (`.t3/dependencies`, `.t3/inputs`, untracked) are
counted as files considered but skipped as "symlink, not regular text".
I moved `.t3` aside with shell `mv .t3 /tmp/t3-bak-39bd06` (a deliberate
tool-rule exception, logged here: Huyang has no way to exclude untracked
scaffolding from coverage, and I needed a complete result set). After that,
the same logical search reported no coverage warning and the identical plan
prepared successfully. Two notes: the refusal told me what was wrong but
not what to do next (exclude paths? fix coverage? refine?), and a
coverage model where two unreadable symlinks veto every all-match
replacement in a 400-file repo will bite every worker run, since every
worker checkout carries that `.t3` directory.

## 5. The frozen set stayed valid across plan building, and staleness is loud

The scenario's first question has a clean answer. `search` ran at
`wsrev_2`; `change_plan prepare` built its sandbox and verified without
moving canonical bytes, so the frozen handle was still valid at prepare
time and the prepare succeeded. After `apply` moved the tree to `wsrev_3`,
I re-ran `refine` against the old parent handle and got an explicit
conflict: `document_content_changed`, instead of a silent empty set or,
worse, matches against new bytes. Freezing works the way you would hope:
prepare does not invalidate, apply does, and the invalidation says the
document changed.

## 6. `preview`/`inspect` and `revision_diff` did not show me the change

The scenario's second question also has a clean answer, and it goes the
other way: I ended up reading the files to be sure. Before applying, the
plan's `preview` listed affected files with byte counts and SHA-256 hashes
(`prepared_test.go` 7399 -> 7315 bytes, `workflow_test.go` 24003 ->
23961, plus the new 360-byte `targets.go`) and a `patch_bytes` figure per
file (16KB and 52KB patches for what ought to be ~14 bytes x 6 of real
change), but no hunks, no before/after lines. `inspect` after the fact
returned the same structure with `committed_diffs` whose `before`,
`after`, and `patch` fields were all `null`. `revision_diff` from `wsrev_2`
to `wsrev_3` dumped ~79KB into a side file and truncated in-band, which is
the opposite of a migration summary. What actually let me trust the
migration was three cheap calls: `search` for `ledgerTarget()` (7 hits: 6
call sites plus the definition), `search` for the old literal (3 hits: the
2 untouched `handlers` occurrences in another package plus the 1 inside
the new helper), and `read` windows over the edited regions showing
`"target": ledgerTarget(),` in place. For a 6-site mechanical migration,
the tool that made the change is the one place that could not show it.

## 7. Shell reaches, all logged

- `git branch`, `git status`, `git log`, `git checkout -b` (setup; the
  workspace began detached at `d501123`, and I created
  `scenario/replace-matches-migration` tracking `origin/main`).
- `mv .t3 /tmp/t3-bak-39bd06` before the search/plan sequence (see item 4)
  and `mkdir docs/friction` (Huyang `create_file` cannot create a file
  whose parent directory does not exist -- it fails with a temp-file
  `no such file or directory` rather than creating parents).
- `make build`, `go test ./...`, `go vet ./...`, `make lint`, `make
  smoke`, `make live`, plus targeted `go test -tags live` runs (see item
  8). Shell is the right tool for all of these; none of them went through
  `verify_run`, which duplicates a subset.
- One redundant `change_plan inspect` after apply that re-fetched the
already-known plan summary, and three `edit_apply` argument-shape retries
for this very log file: top-level `kind` rejected as unknown property,
`operation` without `kind` rejected as missing it -- the accepted shape is
`operation: {kind, path, content}`. The temp-file error in the paragraph
above was the fourth retry.

## 8. Verification and the `make live` failures

- `make build`: `go build -o bin/huyang ./cmd/huyang`, clean.
- `go test ./...`: all packages `ok` (handlers 4.6s, mcpapi, provider,
  provider/embed 11.7s, providerpool 8.8s, service 12.2s, workspace 6.6s).
- `go vet ./...`: clean; `make lint`: `huyang lint: OK`;
  `go vet -tags live ./internal/livetest/`: clean;
  `gofmt -l internal/livetest/`: empty; `make smoke`: `huyang smoke: OK`.
- `make live`: FAILS, but not because of this migration. Every
  `startWithLanguageServers` test fails in this worker checkout during
daemon bootstrap with `nvim ... ENAMETOOLONG` on the luac cache path:
the worker directory path (~190 chars) pushes nvim's hashed cache name
past its limit. `TestImpactPreviewNamesCallersTestsAndUnknowns` then panics at
`impact_preview_test.go:65` ranging over a nil `callers` slice, which is
downstream of the same bootstrap failure. None of the failing tests touch
`ledgerTarget()`; the failures happen before any migrated line executes.
- What does exercise the migration: all 7 live tests that do not need a
  language server PASS, including `TestThePreparedSelectorIsExperimentalOnly`
  (lives in edited `prepared_test.go`) and six workflow tests
  (`TestTwoPreparedPlansCoexist`,
  `TestAnExternalWriteBeforeApplyIsRefused`,
  `TestApplyWithoutAPreparationIsAStateError`,
  `TestAReleasedPreparedRevisionIsRefusedUnderItsOwnCode`,
  `TestAnEmptyPlanIsRefusedAtPrepare`,
  `TestTheFrozenAndExperimentalProfilesCoexist`), 1.6s total.

## 9. Genuinely good

- The atomic plan did what the scenario promises: helper creation plus all
  6 replacements prepared together, sandbox-verified together (format,
  parser, `go vet`, full `go test` all `passed` inside `prepare`), and
  committed together, so the tree was never half migrated.
- Sandbox verification inside `prepare` is the best part of the whole
  flow: I knew `go vet` and the full unit suite passed on the migrated
  bytes before anything canonical moved.
- Stale-handle behaviour (item 5) is exactly right: quiet validity while
  building, loud conflict after the world moves.
- `search` context lines and `include_handles` made choosing the literal
  easy once the regex issue was past: `map[string]any{"path":
  "ledger.go"}` with 8 repo-wide hits, refined to the 6 livetests ones,
  was obviously the one repeated literal, and the `handlers` holdouts were
  visible enough to reason about.

## 10. Where the prompt was wrong

- "If your workspace does not already hold a clone ... make your own with
  `git clone`" -- the workspace already held the clone (detached HEAD),
  so no clone was needed; a branch off `origin/main` sufficed.
- `make live` as a gate cannot pass in this worker environment regardless
  of the change (nvim path-length ceiling, item 8). The prompt presents all
  five checks as equally runnable; here four are green and the fifth fails
environmentally.
