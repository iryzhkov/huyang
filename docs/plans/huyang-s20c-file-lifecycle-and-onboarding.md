# S20c — File lifecycle in `edit_apply`, honest Python verdicts, zero-config onboarding

Status: approved 2026-09-12; waves 0 to 4 implemented on `feature/huyang` (see the
implementation notes at the end); the live proof on the deployed service and the E7/E8
bench measurement remain
Prepared: 2026-09-12
Predecessor: second friction pass at `f78e444` (`bench/agent-efficiency/RESULTS.md`)
Source: field report from a Claude session that implemented about 1,500 lines across
`t3-steward`, `omarchy-setup` and `dev-fleet` through Huyang on 2026-09-12 (about 30 calls)

## Why this stage exists

The session confirmed the direction of S20b and the two friction passes: multi-target
`read`, the `operations` list, the outline view and per-edit diagnostics were each named as
the reason Huyang beat the shell. It also named the one operation that still pushed the
agent back to Bash, and four places where Huyang was honest but unhelpful. Ranked as the
report ranked them:

1. Copying an existing file has no Huyang path. `move_file` and `delete_file` exist only
   inside `change_plan`, and there is no copy at all, so a 15 KB copy would have
   round-tripped through the agent's context to `create_file`. The agent used `cp`.
2. `create_file` on an existing path: the schema does not say whether it overwrites or
   refuses (it refuses with `create_target_exists`; the agent never found out).
3. A 53 KB `read` was dumped whole and the harness truncated it to a saved file whose
   preview showed only the first target. There is no per-target cap and no size index
   ahead of the bodies.
4. Python edits came back `provisional` with `lsp_not_configured` or
   `lsp_attach_deadline_exceeded`; pyright attached once, late, and then reported a
   spurious unresolved `pytest` import because it did not see the project's uv venv.
5. Detected commands for `dev-fleet` were `python3 -m unittest` plus an ad hoc syntax loop,
   while the repository runs pytest and ruff through uv (`Makefile` `gate`, `pyproject`).
   `t3-steward` was untrusted, so `go test` ran in the shell.
6. Result-set handles were returned but never used; `replace_literal` covered every edit.
7. Memory files under `~/.claude/projects/` went through the harness `Write` tool because
   the instructions do not say which tool owns files outside a repository.

The user's framing adds one question this stage must answer rather than dodge: what does
"a move that preserves Git history" mean for a tool that, by contract, never writes the
Git index?

## Boundaries this stage keeps

Everything below is additive to the amended v1alpha1 surface and stays inside the rules
recorded by S20b and the tool contract:

- **Git boundary** (`docs/plans/huyang-tools-v1alpha1.md`, "Git boundary"): Huyang reads
  repository metadata through the sanitized read-only subprocess and never writes the
  index or a ref. A move or copy changes the working tree only. Staging is the agent's
  explicit Git CLI step, and Huyang tells it exactly which one.
- **Content is the revision**: a moved or copied document keeps the source's bytes
  exactly, so its content-derived revision at the destination equals the source's
  revision. No formatter runs on a moved or copied file in the same call (today
  `formatEdited` would gofmt any `.go` file with `AfterExists`; the move and copy kinds
  must be excluded from it, or a rename with drift becomes a rename plus a content
  change in one step).
- **Single durable write path**: the new kinds go through `Workspace.ApplyFile` and
  `writeTempAndRename`, the same path `create_file` uses, with `RENAME_NOREPLACE` for a
  destination that must not exist. No new write primitive.
- **Shared operation union**: `copy_file` is added to the discriminated union that
  `edit_apply.operation` and `change_plan` share, so a plan may copy a file with the
  same preconditions. `move_file` and `delete_file` are not new kinds; they become
  reachable from `edit_apply` and the `operations` list.
- **Every store is bounded**: a copy or move whose source exceeds `MaxPlanContentBytes`
  (4 MiB) is refused with a coded error rather than journaled; the receipt keeps the
  same trimmed payload as every other mutation.
- **Typed errors, compact replies, scheduler class declared**: new refusals are
  `CodedError` values; replies carry paths, revisions and a one-line Git status, and the
  `edit_apply` descriptor stays `ClassCanonicalWrite`.
- **Detection never executes**: the command detector may read `pyproject.toml`, a
  `Makefile`, `uv.lock` and `.venv/bin`, but execution still needs the root in the user
  trust policy. The stage adds a CLI to write that policy, not a tool.
- **Contract amendment, not redesign**: the v1alpha1 document says `edit_apply` "must
  not grow an operations array and become a second plan API". The second friction pass
  already shipped the array, and this stage adds kinds to it. What keeps it from being a
  second plan API is stated in amendment 2 and enforced in code: the list is a
  sequence, not a transaction. Operations apply in order against canonical bytes, a
  refusal stops the list and leaves the earlier operations applied, nothing is
  prepared in a sandbox and no pipeline stage runs. Anything that must change
  atomically, or be verified before it lands, is a `change_plan`. The descriptor says
  so in one sentence.

## Framework check

The stage was checked against the rules it must not break. Two conflicts exist in the
record, not in the code, and both need a decision from the roadmap owner:

1. **The freeze.** S20b decision 11 re-froze v1alpha1 at the end of S20b, and the
   semantic-evidence plan's governing constraints say "do not change the frozen
   v1alpha1 input schemas; incubate new input shapes in an experimental v1alpha2
   catalog". The two friction passes after S20b already added inputs to v1alpha1
   (`root`, `operations`, `paths`, `context_lines`, `numbered`, `search.mode`
   semantic values, `navigate.symbol`, `verify_run` `current`), so the freeze as
   written is already breached. This stage proposes to treat all of it the way
   amendment 1 treated the S20b additions: optional inputs and compacted outputs,
   recorded in amendment 2, with the freeze moved to the end of S20c. That requires
   editing S20b decision 11 and the foundation paragraph of the semantic-evidence
   plan in `~/Work/plans`. The alternative is to leave v1alpha1 as amended at S20b and
   ship every post-S20b input under a v1alpha2 catalog; that would mean two catalogs
   for one surface with no behavioural difference, which the contract's own
   "profiles are fixed catalogs, not negotiation" rule argues against.
2. **Arrival time is never causality.** The late-attach recording in wave 2b must key
   on the document version the server publishes for, not on when the batch arrives.
   The kernel already compares `version == expected` in the fresh-publish check; the
   late batch is attributed to a transaction only when its version equals the buffer
   version that transaction left, and otherwise to the current revision as ordinary
   push evidence. Wave 2b is written to that rule below.

Everything else lines up: the new kinds reuse the single write path and the shared
operation union; every new store or payload has an existing bound; errors are coded;
replies stay compact; `edit_apply` keeps its scheduler class; detection still never
executes; the trust CLI is administration and not a tool; the Git boundary is
unchanged; sandbox paths are not exposed; unavailable evidence is never promoted to
clean; `make lint`'s 80-line rule and `make budget`'s descriptor-bytes bound apply to
every wave.

## What "preserving Git history" means here

Git stores no per-path history. `git log --follow` and `git diff -M` reconstruct renames
at read time by content similarity between a path that disappeared and one that
appeared in the same commit (the default threshold is 50 percent). A working-tree move
therefore preserves history when two things hold:

1. the destination's bytes are the source's bytes, or close to them, in the commit that
   records the move; and
2. both the removal and the addition are staged in that commit.

Huyang guarantees the first by construction (exact bytes, no formatter, no line-ending
change) and cannot do the second without crossing the Git boundary. The reply therefore
carries what the agent needs to do the second step in one shell call: the tracked state
of both paths from `git ls-files` and a `next` entry naming the exact command
(`git add -A -- <from> <to>`). `git mv` does nothing more than that.

Two things Huyang must not do, because they silently break the similarity test:
reformat the moved file in the same call, and normalise line endings on the way. Both
are covered by tests in wave 0.

## Decisions taken (2026-09-12)

1. **Staging a rename.** Huyang never writes the index; the reply names the `git add`
   command. The rejected alternative was an explicit `stage: true` argument running
   `git add -A -- from to` through the sanitized subprocess: one fewer agent call per
   move, but the first index write in the codebase, with its own adversarial
   repository-config surface (`core.fsmonitor`, `filter` drivers, `index.version`).
2. **Copy source outside the workspace root.** An absolute `from` outside the root is
   allowed as a read-only source, because the reported case (the installed
   `~/.claude/CLAUDE.md` into the `omarchy-setup` checkout) is exactly that. The source
   is read once, its SHA-256 and size are recorded in the receipt and echoed in the
   reply, an optional `expected_sha256` guards it, and the destination stays confined to
   the root by the existing component-walked check.
3. **Guard on `delete_file` in `edit_apply`.** `revision_id` or `expected_sha256` is
   required; a delete with neither is refused with the current revision and hash in
   `data`, so the retry is one call. Deleting a file the agent has not read is the case
   the guard exists for; `read` already returns `revision_id`.

## Wave 0 — File lifecycle through `edit_apply`

### Operation kinds

`edit_apply.operation.kind` and every item of `operations` accept, in addition to
`replace_literal`, `create_file` and (single only) `replace_range`:

- `move_file {from, to, revision_id?, destination_revision_id?}`: `to` must be missing;
  `from` must exist. A missing `revision_id` binds to the current source revision exactly
  as `normalizeMoveFile` does for plans. Bytes, mode and symlink target are preserved.
- `copy_file {from, to, expected_sha256?, destination_revision_id?}`: same destination
  rule; `from` may be absolute and outside the root (decision 2). The reply carries
  `source: {sha256, bytes}`.
- `delete_file {path, revision_id? | expected_sha256?}`: decision 3. The preimage is
  kept in the receipt for the `PayloadWindow` (15 minutes) like every other mutation,
  which is what a `revision_diff` after the fact reads.
- `create_file {path, content, replace?: true, revision_id}`: a guarded whole-file
  overwrite. Without `replace` the current refusal `create_target_exists` stands and the
  descriptor says so. With `replace` the `revision_id` of the existing file is required
  and checked, so an agent that read the file can rewrite it in one call without the
  old content in the request. This is the only change to an existing kind.

`move_file`, `copy_file` and `delete_file` are file-lifecycle kinds: `formatEdited` skips
them, and the diagnostics refresh treats a moved or copied source file as a new
document at the destination and a closed one at the source (the Lua kernel's `didOpen`
and `didClose` path that `create_file` and plan moves already use).

### Reply shape

The compact edit reply gains, for these kinds only:

- `changed_paths` lists both ends of a move or copy;
- `git`: `{from: tracked|untracked|ignored|not_a_repository, to: ...}` from one
  sanitized `git ls-files` call, omitted outside a repository;
- `next`: for a move whose source is tracked, one entry with the staging command; for a
  delete of a tracked file, `git rm --cached -- <path>` (the file is already gone from
  the tree, so `git add -A -- <path>` is the equivalent and simpler form; pick one and
  use it everywhere).

### Refusals

All coded, all changing nothing: `move_source_missing`, `move_target_exists`
(`create_target_exists` is reused for copy), `copy_source_missing`,
`copy_source_too_large`, `copy_source_changed` (hash mismatch), `delete_guard_required`,
`delete_target_changed`, `replace_revision_required`, `replace_target_changed`. Symlink
escape refusal and component-walked confinement apply to every destination as today.

### Implicit document workspace

`editImplicitDocument` currently accepts `replace_literal` and `create_file` by absolute
`path`. It extends to `move_file`, `copy_file` and `delete_file` by opening a documents
workspace over the named ends (`to`, or `path`), so a copy into a directory that is not
a repository is one call too.

### Code

- `internal/workspace/plan.go`: `OperationCopyFile`, `normalizeCopyFile`, `applyCopy` in
  the preview builder, ordering edges for copy (a copy of a path that a later edit
  touches, and an edit of a path a later copy reads).
- `internal/workspace/commit.go`: the copy case in the stage request builder, mirroring
  `OperationMoveFile` without clearing the source. In a plan, `normalizeCopyFile` binds
  the source hash at normalization (a source outside the root has no revision), and
  apply rechecks it per path like every other precondition, so a source that changed
  between prepare and apply is `CONFLICTED`, never a silent stale copy.
- `internal/workspace/workspace.go` or `text.go`: `ApplyFile` gains `FileMove`,
  `FileCopy`, `FileDelete` and `FileReplace` alongside `FileCreate`, each a single
  guarded write set advancing the state sequence once.
- `internal/workspace/git.go`: `TrackedState(paths) map[string]string` over one
  `ls-files -z --cached --others --exclude-standard --ignored` call, bounded and
  sanitized like the existing listing.
- `internal/handlers/edit.go`, `edit_literal.go`, `edit_batch.go`, `edit_implicit.go`,
  `edit_format.go`: decoding, application, list support, implicit-workspace support,
  formatter exclusion.
- `internal/mcpapi/catalog.go`: descriptor text and schema for the new kinds and the
  `replace` flag; the `operations` item schema lists every accepted kind.

### Tests

- Unit: each kind applies, each refusal fires with nothing changed, the source's
  content-derived revision equals the destination's after a move and after a copy,
  a moved `.go` file with gofmt drift keeps its drift, CRLF content survives a move
  byte-for-byte, a symlink moves as a symlink, a copy of 4 MiB + 1 byte is refused.
- Git: in a fixture repository, move a tracked file through `edit_apply`, run the
  `next` command, and assert `git status --porcelain` shows `R` for the pair and
  `git diff --cached -M --name-status` reports a 100 percent rename.
- Contract: catalog counts unchanged (19/8/13/12), descriptor budget within
  `docs/plans/budget/baseline.json`, every new error code present in
  `internal/workspace/errors.go`.
- Bench: two scenarios added to `bench/agent-efficiency` (E7 copy a 15 KB file, E8
  move a file and stage the rename), with the modelled built-in cost being Bash `cp`
  and `git mv`.

## Wave 1 — Reads that know their size

- `read` accepts `max_lines` (per call, and per item in `targets`). A target over the
  cap returns its first `max_lines` lines, `truncated: true`, the total `lines`, and a
  `next` entry pointing at `view: outline` and a line window. Without `max_lines` the
  behaviour is unchanged (no cap), as the agent guide promises.
- Multi-target replies put an `index` before `files`: one `{path, lines, bytes}` per
  target, so a harness preview that shows only the head of the reply still shows every
  size. Go's JSON encoder orders map keys, so the field is named to sort ahead of
  `files`.
- `create_file` descriptor states the refusal and the `replace` flag (wave 0 delivers
  the flag; this wave only fixes the wording if wave 0 is deferred).
- `docs/agent-guide.md` gains a row for the multi-target read with `max_lines` and a
  rule of thumb: outline first for a file over about 500 lines, then windows.

## Wave 2 — Language servers: attach early, see the project, say what is missing

The session saw three distinct language-server failures on Python, and each has a
different cause in the kernel. The wave fixes all three and the reporting around them.

### 2a. The server is installed but the config is not enabled (`lsp_not_configured`)

`core.lua` `get_client` and `edit.lua`'s diagnostics path raise `lsp_not_configured`
when `enabled_lsp_configs_for(filetype)` is empty, even when the server binary is on
`PATH`. `install.lua` already holds the default server per filetype (`python =
"pyright"`, `go = "gopls"`, `typescript = "ts_ls"`, and so on). At provider start for a
project workspace, the kernel enables that default config for every filetype present in
the root whose executable `vim.fn.executable` finds, and records the decision in the
handshake health detail (`enabled: pyright (found /usr/bin/pyright-langserver)`). A
user's own `HUYANG_HEADLESS_INIT` configuration keeps precedence: a config the init
file enabled or disabled is not touched. `lsp_not_configured` is then reserved for a
filetype with no known default, and its message names `language_server_setup install`
as today.

### 2b. The server attaches after the deadline (`lsp_attach_deadline_exceeded`)

The post-edit diagnostics path shares one `wait_ms` budget across the file set of a
transaction (`edit.lua`, `attach_deadline`), and a cold pyright or gopls start can
exceed it on the first edit of a session. The report describes exactly that: pyright
"showed up once, late". Three changes:

- **Warm attach at open.** `workspace_open` on a project workspace already asks the
  provider for `workspace_support`. The provider additionally starts the enabled server
  for each detected language in the background (one scratch buffer per filetype, closed
  once a client attaches or the start fails), so the first edit finds a warm client.
  Bounded: at most the languages present in the root, never a server whose executable
  is missing, cancelled cooperatively with the request, and never awaited by the open
  itself. `language_server_status` reports per language `attach: warm|starting|failed
  (<reason>)|not_started` and the start time.
- **Late attach is recorded, not lost.** When a client attaches after a verdict was
  returned as `unavailable`, the publish handler the kernel already wraps
  (`wrap_publish_handler`) records its first batch for those documents. The batch is
  attributed to the earlier transaction only when the version the server publishes
  for equals the buffer version that transaction left (the same `version == expected`
  test the fresh-publish check uses); otherwise it is ordinary push evidence for the
  current revision. Go's diagnostic ledger then supersedes the `unavailable` evidence
  for the matched documents, and the next reply's `diagnostic_updates` carries the
  findings with `attribution: late_attach` and the transaction they belong to. The
  original reply is not rewritten; the verdict it gave was true when given.
- **The deadline is per server start, not per call.** A server that is starting keeps
  its own start deadline (`ATTACH_TIMEOUT_MS` today, made configurable per server in
  the kernel with the jdtls value as the precedent); a call whose budget expires while
  a start is in progress reports `lsp_starting` with the elapsed time rather than
  `lsp_attach_deadline_exceeded`, and the `next` entry says to retry `verify_run
  diagnostics` for the same revision rather than `language_server_setup restart`, which
  would kill the start it is waiting for.

### 2c. The server does not see the project environment (spurious `pytest` import)

Pyright resolved imports against the system interpreter because nothing told it about
the project's uv venv. The kernel already finds a project venv for debugpy (`dap.lua`,
`.venv`, `venv`, `env`). The same lookup configures pyright and basedpyright through
`vim.lsp.config(name, {settings = {python = {pythonPath = <venv>/bin/python}}})` before
the first Python buffer attaches, with `VIRTUAL_ENV` from the service environment as a
fallback and `uv.lock` or `poetry.lock` as a hint that a venv is expected when none
exists yet (reported, not created). The chosen interpreter appears in
`language_server_status` as `interpreter: <path>`. The same pattern applies to the two
other servers whose resolution depends on a per-project toolchain: `ts_ls` with
`tsserver.path` from `node_modules/typescript` when present, and `gopls` with `GOFLAGS`
and `GOWORK` from the service environment passed through `cmd_env`.

### 2d. Plain wording for an absent verdict

When the diagnostics dimension is `unavailable` for every edited document, the edit
summary says so in words the agent can act on: "no Python language server attached
(pyright starting, 1.8 s elapsed); no diagnostics for main.py yet" or "no language
server is configured for filetype toml; no diagnostics" instead of "semantic
diagnostics remain incomplete". The outcome stays `provisional`, because the evidence
model is right that the verdict is incomplete; only the sentence and the `next` entry
change, and each `unavailable` reason (`lsp_not_configured`, `lsp_not_startable`,
`lsp_starting`, `lsp_attach_deadline_exceeded`) has its own sentence.

### Tests

- Kernel: auto-enable respects an init-file decision; warm attach starts one client
  per present filetype and none for a missing executable; the late-attach batch is
  recorded against the earlier transaction's revision; a start in progress yields
  `lsp_starting` and not `lsp_attach_deadline_exceeded`.
- Evidence: the real-pyright test gains a fixture with a `.venv` holding one installed
  package and asserts the import resolves with the venv and fails without it; the
  ledger supersedes `unavailable` evidence with a late batch and the `diagnostic_updates`
  delta names the transaction.
- Fake-LSP permutations assert the summary sentence for each `unavailable` reason and
  that none of them is ever promoted to a clean verdict (the S17 rule).

## Wave 3 — Command detection that reads the project, and a trust CLI

`detectPipelinePolicy` currently keys everything on the service's own `PATH`
(`exec.LookPath("pytest")`) and ignores what the project declares. Replace the Python
branch with, in order:

1. A `Makefile` with `test`, `check`, `lint` or `gate` targets: `make <target>` for the
   matching stage, `test` as the tests command, the others as checks. Applies to every
   language, before the language-specific rules.
2. `pyproject.toml` `[tool.pytest.ini_options]` or `[tool.pytest]`, `pytest.ini`,
   `setup.cfg [tool:pytest]`: pytest is the test runner even when it is not on the
   service's `PATH`.
3. Runner: `uv run` when `uv.lock` exists, `poetry run` when `poetry.lock` exists,
   `.venv/bin/<tool>` when the venv has it, else the bare tool. Ruff the same way,
   enabled by `[tool.ruff]` or a `ruff.toml` as well as by presence on `PATH`.
4. The current `unittest` fallback stays for a project with none of the above.

`workspace_open` continues to report `commands.source: detected` with the files that
drove the detection (`detected: ["pyproject.toml", "uv.lock", "Makefile"]`), and the
summary line names the override (`.huyang.toml`).

**`huyang trust <root>`**: a CLI subcommand that appends the resolved root to
`[trust].roots` in `$XDG_CONFIG_HOME/huyang/config.toml`, creating the file with mode
0600 when absent, refusing a root that is not a directory, and printing the line it
wrote. `huyang trust --list` and `huyang trust --remove <root>` complete it. This is
administration, so it is a CLI and not a tool, per the contract's "Administration"
paragraph; the `verify_run` untrusted-root reply already names the file and now also
names the command.

Tests: detection fixtures for each of the four rules and their precedence; a `Makefile`
with a `test` target but no Python; the CLI round trip on a temporary config directory.

## Wave 4 — Documents and instructions

- `docs/plans/huyang-tools-v1alpha1.md`: "v1alpha1 amendment 2 (S20c, 2026-09-12)"
  recording the `operations` list (shipped in the second friction pass), the semantic
  `search` modes, `navigate.symbol`, the new operation kinds, the `replace` flag,
  `read.max_lines`, and the reasoning that each is additive. The freeze in S21 applies
  from the end of this stage.
- `docs/agent-guide.md`: rows for copy, move plus stage, delete, guarded overwrite,
  capped multi-target read; a paragraph on what a move preserves and what the agent
  must still do with Git.
- `README.md`: the trust CLI; the `edit_apply` kinds list.
- `~/.claude/omarchy-setup/CLAUDE.md` (its own repository): one sentence under "Reading
  and editing code" that files outside any repository, including the machine-local
  memory directory, may be written with either the harness `Write` tool or
  `edit_apply create_file`; both are one call, and the memory directory is not source.
- `bench/agent-efficiency/RESULTS.md`: the field report above appended to the friction
  log with the fix for each item, and the E7/E8 numbers once measured.

## Order and gates

Waves 0 and 3 are independent and can run in parallel; wave 1 is small and lands first;
wave 2 touches the Lua kernel and the diagnostic ledger and needs the live pyright and
gopls fixtures, so it runs on the canary host with the language servers installed;
wave 4 closes.

Each wave ends with `make check` (gofmt gate, vet, functions over 80 lines), `go test
./...`, `make budget` against `docs/plans/budget/baseline.json`, and the bench scenarios
it touched rerun through the protocol harness. The stage is complete when the reported
session's three shell fallbacks are reproduced through Huyang on the live service: the
`CLAUDE.md` copy into `omarchy-setup`, `verify_run` on `dev-fleet` running pytest and
ruff through uv, and `verify_run` on `t3-steward` after `huyang trust`. Deploy per the
recorded procedure only after that proof.

## Implementation notes (2026-09-12)

What landed differs from the text above in these places:

- Wave 0: `ApplyMove` writes the destination and removes the source through two
  native journals rather than one; a crash between them leaves a complete copy at both
  paths and never loses content. Symlinks and binary files: binary files move and copy
  natively (the journal comparators accept them now), symlinks and directories are
  refused with `transfer_source_unsupported` and stay with `change_plan`. The
  `delete_target_missing` code was added. Bench scenarios E7 and E8 are described in the
  agent guide but not yet added to the protocol harness.
- Wave 2b: the late-attach batch is captured in the kernel's publish handler and handed
  to Go through a new `huyang_late_evidence` operation, which the next edit and the
  `diagnostics` tool call; the reply field is `late_evidence_batches`. The kernel keeps at
  most 64 pending batches. Servers are warmed by the same `workspace_support` probe
  `language_server_status` uses, with a 250 ms attach wait, in a goroutine keyed by
  workspace and provider epoch. The per-server start deadline is not separately
  configurable; `lsp_starting` is reported whenever a client for a configured server
  exists but has not initialized.
- Wave 2c: gopls needs no change, because the provider inherits the service's
  environment (`GOFLAGS`, `GOWORK` included); the plan's `cmd_env` note was wrong.
- Wave 3: a Makefile `gate` target is not used, since it is a superset that usually
  includes `test`; `make test` covers `**`. The Python syntax check skips `.venv`, `venv`
  and `node_modules`. Detection of `[tool.ruff]` requires the tool to be reachable in the
  project's environment; a `[tool.ruff]` without ruff installed adds nothing.

## Follow-up: what using the tool for this stage showed (2026-09-12)

The stage was implemented through Huyang itself, about two hundred calls. Six things cost
calls or misled, and each was fixed in the same branch:

1. A passing `verify_run` stage reported a verdict and no evidence, so the same command
   was run again in a shell to see what it said. A passing stage now carries
   `output_tail`, the last three non-empty lines bounded at 240 bytes
   (`VerificationTailLines`, `VerificationTailBytes`), which is where a command states
   what it did.
2. A multi-target `read` rejected `view` and `numbered` per target, so outlining one file
   and windowing another took two calls. Every option of a single-target read now applies
   per target.
3. An `edit_apply` `operations` list outside any repository was refused for want of a
   workspace, which forced one call per file. It now opens one documents workspace over
   every absolute path the list names.
4. `view: outline` covered Go and Python only: a 4,600-line Lua file answered a
   whole-document handle, honestly labelled `text_only`, with `lua_ls` attached. The
   outline now falls back to the semantic provider through a new kernel operation,
   `file_symbols`, which answers the structural index of one file in the shape
   `find_symbol` already uses, so the same durable handles are registered and a
   `symbol_locator` read can name what the outline listed. The outline reply is
   compacted at the same time: it listed a full handle record per declaration, hashes
   and anchors included, which made the outline of a fifty-declaration file larger than
   the file. It is now one entry per declaration with its name, kind, line range and
   handle id.
5. The friction spool filed 998 calls under 13 anonymous ids, because the daemon reads
   the session from its own environment. The adapter now announces its session in the
   control hello and every call on that connection is spooled under it.
6. A diagnostic's identity included its range, so an edit above an untouched warning
   retired it and announced an identical one: every edit reported diagnostics it had not
   caused. Identity is now what the finding says plus its occurrence among identical ones
   in the document, and the stored range follows the latest observation.

And one thing the agent could not have known without being told: the guide to the
cheapest correct call lives in `docs/agent-guide.md`, which an agent working in another
repository never sees. The first reply for a workspace now carries a `guide` array of
five one-line rules, sent once, on whatever call opened the workspace.

## Out of scope

No index or ref writes unless decision 1 chooses the alternative. No directory move or
recursive delete: a directory is a set of files, and a plan lists them. No undo tool;
receipts and `revision_diff` remain the recovery path. No change to plan-mode semantics
beyond the `copy_file` kind. Result-set handles stay as they are; the report's note that
they went unused is evidence that `replace_literal` is the right default, not a defect.
