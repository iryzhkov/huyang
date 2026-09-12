# Huyang model-facing tool contract

Status: S00-reviewed v1alpha1 surface, amended by S20b and re-frozen (see the amendment at the end)
Prepared: 2026-09-10
Amended: 2026-09-11
Upstream inspected: `iryzhkov/agent99@947f5b1`
Implementation status: implemented in `internal/bridge/modern_mcp.go`; the catalog test
`TestModernRegistryMatchesFrozenProfiles` holds the code to `fixtures/huyang-v1alpha1/contract-schema.json`

## Decision

Huyang keeps agent99's useful capabilities but does not expose its accumulated one-tool-
per-mechanism roster as the modern default. The modern API has a compact set of intent-
named tools, a common workspace/revision/result envelope, and one transaction operation
schema shared by one-shot and multi-step edits. No legacy surface ships: the agent99 names
are not advertised by any profile, endpoint, or environment switch, and the compatibility
window the S20 release candidate described was voided by the repository split and removed
outright in S20b.

The inspected standalone agent99 roster advertises 38 ordinary tools by default and 51
when debugging is enabled. Several differ only by navigation relation or edit target shape;
their individual schemas are useful compatibility contracts but too much simultaneous
choice for the new default.

The complete modern coding catalog is 19 tools: 13 shared orientation/editing tools, two
language-server administration tools added after the S00 freeze, and four debugging tools.
Its portable default is `full`, so an agent can move through orientation, editing and
debugging without a harness-specific capability. Optional fixed `orient`, `edit` and
`debug` launch profiles reduce context when an orchestrator already knows the session's
job. Profiles filter presentation only; they are not permissions or separate implementations.
A large-change plan is part of editing, not a fourth mode.

## Design rules

1. One user intent has one obvious first tool.
2. Read tools never mutate. Discovery of an LSP/parser/formatter is reported, not installed.
3. Every stateful call names a workspace ID; every mutation carries a revision or handle
   precondition.
4. A one-operation edit is one call. Transactions are available for coherent multi-operation
   work, not mandatory ceremony for changing one function.
5. One operation schema is used by `edit_apply` and `change_plan`; the two paths cannot
   drift semantically.
6. Structured content is authoritative. The text rendering is compact, deterministic and
   sufficient for clients that lose structured content.
7. Empty, partial, capped, stale and provisional results are distinct states.
8. Results say what may happen next through machine-readable `next` entries.
9. Detailed diffs, diagnostics and command output are addressable as evidence instead of
   repeated in every response.
10. Profile catalogs are stable. Workspace-specific capability loss is reported in results
    and coverage rather than silently removing tools or schema branches.

## Modern roster foundation

### Workspace and discovery

| Tool | Responsibility |
| --- | --- |
| `workspace_open` | Open a project or document allowlist and return ID, revision, capabilities, compact overview and bounded recent local commits when Git exists. |
| `workspace_inspect` | Current revision, dirty/external state, provider health, languages, semantic coverage, configured formatter/check/test stages, variants and limits. Optional `view=overview|map`. |
| `search` | Literal/regex/structural search over current source or bounded read-only Git history, with refinable result sets, semantic classification, honest coverage and cursors. |
| `symbol_find` | Find declarations by name/path and return ranked, inspectable semantic handles; optionally include source. |
| `navigate` | LSP/semantic relationships selected by `relation=definition|type_definition|implementation|references|incoming_calls|outgoing_calls|hover`. |
| `read` | Read canonical/prepared source, outline or read-only history by file/range/handle, including commit handles. |
| `diagnostics` | Inspect normalized parser/LSP diagnostics and their confidence/provenance for a revision or transaction. |
| `code_actions` | List revision-bound quick fixes/refactors. It never applies one. |

### Mutation and verification

| Tool | Responsibility |
| --- | --- |
| `edit_apply` | Resolve, prepare, verify and apply one small operation through an internal transaction; optional preview-only mode. |
| `change_plan` | Create, edit, preview, prepare, inspect, apply or discard one multi-operation editing plan through an explicit action union. |
| `verify_run` | Run named formatter gate, parser, diagnostics, check or test stages against a canonical revision or prepared transaction. |
| `revision_diff` | Explain canonical changes between two revisions or why a mutation precondition is stale. |
| `evidence_get` | Page detailed diffs, diagnostics, command output or provenance by evidence ID. |

This is 13 tools. `workspace_inspect` replaces separate tree/map/status/capabilities tools;
`read(view=outline)` replaces `skim` and `document_symbols`; `navigate` replaces eight
nearly identical position tools; `change_plan` owns one plan state machine rather than
advertising seven lifecycle tools. These are bounded enums over one conceptual operation,
not unrelated “mega-tools.”

Workspace lease release is client lifecycle behavior, not a reasoning task, and is omitted
from the model-facing roster. The adapter releases its lease on clean disconnect or expiry.

### Workflow profiles

- `orient`: `workspace_open`, `workspace_inspect`, `search`, `symbol_find`, `navigate`,
  `read`, `diagnostics`, `evidence_get`.
- `edit`: the orientation foundation plus `code_actions`, `edit_apply`, `change_plan`,
  `verify_run` and `revision_diff`.
- `debug`: the orientation foundation plus the four-tool debug roster described
  below. Once the cause is understood, a new edit-profile connection may reuse the same
  service workspace ID and revision-bound handles.
- `full` (portable default): all 19 tools, allowing one session to move among all three
  workflows. It is the only profile that carries `language_server_status` and
  `language_server_setup`.

The profile is selected by a fixed server startup argument or HTTP endpoint, not a custom MCP
negotiation. Switching profile is a client/session boundary, never a hidden server-side mode
toggle. The long-lived service retains workspace state across adapters. The three acceptance
budgets are measured independently: first useful orientation, guarded edit through evidence,
and fault localization from a reproducible stop.

Profiles must not be required for correctness. Harnesses with deferred tool discovery may
defer non-current tools, but Huyang never assumes that facility exists. Machine
administration beyond the two language-server tools (trust changes, recovery, service
maintenance) stays outside the catalog because it is configuration, not missing modern
coding capability.

## Optional rosters

- **Language servers in the `full` profile:** `language_server_status` probes the owned
  Neovim provider and reports, per language in the workspace, the parser and language-server
  attachment state and the install options available; `language_server_setup` installs or
  restarts a language server or parser through the provider with an explicit `action`,
  `language`, optional `server` and `parser`. Both were added after the S00 freeze (see the
  amendment). They are the one administration surface in the catalog, because an agent that
  finds semantic coverage missing needs a way to repair it that is visible, keyed by
  idempotency key and never triggered by an ordinary read or edit.
- **Debug in the `full` and `debug` profiles:** four cohesive tools: `debug_session` for
  start/attach/restart/stop,
  `debug_breakpoints` for breakpoint state, `debug_control` for continue/pause/step/run-to,
  and `debug_inspect` for threads/stacks/scopes/variables/evaluate. Each uses a discriminated
  action union. Debug session state is orthogonal to workspace edit plans.
- **Administration:** debugger-adapter installation, trust changes, recovery and service
  maintenance. These mutate machine configuration and are never ordinary coding tools; they
  are CLI and configuration-file operations, not MCP tools.

## Shared operation schema

`edit_apply.operation` and every operation supplied to `change_plan` use the same
discriminated union:

- `replace_symbol`
- `delete_symbol`
- `insert_before`
- `insert_after`
- `replace_range`
- `create_file`
- `move_file`
- `delete_file`
- `rename_symbol`
- `move_symbols`
- `replace_matches`
- `apply_code_action`

Every operation carries:

- `op_id`, unique within the transaction;
- target handle or human-readable locator;
- document/base revision precondition;
- operation-specific content;
- optional declared write set when an external action may touch several files.

`replace_range` supports an exact expected-text hash plus bounded before/after anchors.
Line numbers are display information, not the sole mutation precondition. `insert_before`
and `insert_after` accept symbol or range handles, so the same verbs cover declarations,
headings, configuration keys and parserless anchored ranges.

### Homogeneous find/replace

`replace_matches` is one atomic operation over one frozen set of text matches, not a generic
macro. It has two source branches:

- `result_set_handle`: consume the revision-bound complete match set returned by `search`;
- `query`: resolve literal or explicit-regex query, scope and classification filter inside
  the edit/plan, with required expected match/file counts and a write budget.

`search` returns a compact `result_set_handle` for its complete internal hit set even when
displayed groups are paged. An incomplete/capped search cannot produce an all-matches-capable
handle. Applying revalidates the workspace/document revisions, exact original matches,
coverage and non-overlap, then changes all or none. It visits the frozen original set once;
replacement text that also matches the query is not processed recursively.

Replacement bytes default to literal. Regex capture expansion is an explicit template branch
with one documented syntax; zero-width/overlapping matches and encoding-changing replacements
are rejected unless a future operation defines exact semantics. The result reports original
match count, changed file count, per-classification counts and a paged diff.

Use `replace_matches` for one uniform textual rule, `rename_symbol` for semantic identifier
rename, and `apply_code_action` for provider-defined fixes/refactors. A provider-native
fix-all action remains one revision-bound code action. Multiple unrelated individual actions
are separate visible operations in `change_plan`; Huyang does not disguise them as text
replacement.

A search hit returns a revision-bound `match_handle` in addition to its enclosing symbol
handle. When the match corresponds unambiguously to one syntax node (an if statement,
argument, key/value pair, import or declaration), the locator records that node and its
fingerprint. Otherwise it is an exact range handle with content and bounded-anchor hashes.
This makes the normal precise-edit path two calls:

1. `search` scoped to the file and, when known, the enclosing symbol.
2. `edit_apply` with `replace_range` targeting the selected `match_handle`.

No reread or repeated old text is required. The edit still fails safely when the document
revision changed, the syntax node became ambiguous, or the exact range no longer matches.

`edit_apply` accepts exactly one operation. Multiple related operations use `change_plan`;
it must not grow an operations array and become a second plan API.

## Change-plan mode

“Plan mode” is the editing workflow for a multi-operation change. `change_plan` has one
required action discriminator: `create|edit|preview|prepare|inspect|apply|discard`. Each
action has its own closed schema branch; irrelevant fields are rejected rather than ignored.

```text
begin
  → edit plan operations
  → preview
  → prepare in sandbox
  → inspect diff + diagnostics + checks/tests
  → edit the plan again
  → prepare the new revision
  → explicitly apply to the workspace, or discard
```

Every edit creates a monotonically increasing `plan_revision`. Operations have stable
`op_id` values, so `change_plan(action="edit")` supports:

- `add`: append new operations;
- `update`: change the target, content, precondition or policy of named operations;
- `remove`: delete named operations;
- `reorder`: change user-visible intent order, subject to safe dependency ordering;
- `replace_all`: replace the complete operation list for generated or UI-driven plans.

Editing a READY/PROVISIONAL plan returns it to OPEN, discards its sandbox as an
applicable result, and marks its evidence superseded rather than deleting it. The next
`change_plan(action="prepare")` creates a fresh sandbox for the new plan revision. Preparation is
deterministic for the same base revision, plan revision, configuration and environment
allowlist.

`change_plan(action="inspect")` can compare two plan revisions. For a large change it defaults to:

- files and symbols added/changed/moved/deleted;
- public API and dependency impact;
- new/resolved/unchanged/provisional diagnostics;
- formatter, check and test deltas;
- coverage gaps, variants not run and undeclared writes;
- compact per-file summaries with cursors to detailed diffs/evidence.

Preparing a plan never changes canonical workspace bytes. Only
`change_plan(action="apply", plan_id, plan_revision, prepared_revision)` does, after
revalidating canonical preconditions. Requiring both exact revisions prevents a model or UI
from committing revision 3 after the user has inspected revision 4.

When operations are already known, `change_plan(action="prepare")` accepts an inline base
revision, policy and operation list, atomically creating and preparing the plan. Its response
includes the normal inspect summary and compact prepared diff. The common large-change path
is therefore two calls—prepare, then apply—not create/add/prepare/inspect/apply. Explicit
create/edit actions remain for genuinely interactive planning.

For a 30-file change, results are bounded by summary/evidence budgets rather than by dropping
files. Every affected file appears in the summary index; detailed diffs, diagnostics and test
output are paged. Primary failures and uncovered verification dimensions are never hidden.

### Diff views

`change_plan(action="preview")` and `change_plan(action="inspect", view="diff")` expose:

- `predicted`: base revision versus the declared plan operations, before tools run;
- `prepared`: base revision versus the exact sandbox bytes after imports, formatting,
  code actions and declared generators;
- `tool_delta`: predicted versus prepared, isolating changes made by those tools;
- `revision_delta`: any difference between two editable plan revisions.

The default view is a semantic index grouped by file and symbol: operation IDs, add/change/
move/delete classification, public API impact, line counts and whether a tool altered the
declared result. `detail="patch"` returns structured unified hunks plus a deterministic
text patch fallback. Filters accept file, symbol/handle, operation ID and change kind.

Large diffs page only at file or hunk boundaries. Every affected file remains in the summary
index, binary/mode/symlink changes are explicit, and the response states whether all hunks
were returned. A diff can be exported as evidence without applying it.

After viewing the diff, the caller uses `change_plan(action="edit")` to update/remove/reorder the
responsible operations and prepares the new plan revision. The old diff and evidence remain
addressable for comparison but cannot be applied once superseded.

## Affected-test verification

`verify_run(stages=["tests"], scope="affected")` selects and runs likely affected tests
against the exact prepared sandbox bytes. Selection combines:

- changed symbol handles and public-signature deltas;
- LSP references, implementations and call hierarchy where reliable;
- language-specific import/package/module dependency edges;
- tests colocated with affected packages/modules;
- prior test failures/durations and optional coverage associations at compatible revisions;
- explicit project variant and test-suite configuration.

Each selected test or suite carries reasons such as `direct_reference`,
`affected_package`, `importer_of_changed_module`, `coverage_association` or
`configured_required_suite`. The result separately reports:

- selected and executed tests;
- affected graph nodes with no associated test;
- capped/unavailable graph edges;
- dynamic/reflection/generated/configuration risks;
- project variants included and omitted;
- whether a broader package, integration or full gate is recommended.

LSP availability improves precision but is neither required nor sufficient. A language
adapter may prove a safe package-level boundary; otherwise `scope=affected` is advisory.
Its success is named `affected_tests_passed`, never `full_tests_passed`.

Preparation policy may require affected tests before a plan reaches READY. A user/model can
then revise the plan and rerun only the invalidated affected tests. `scope=full` remains a
separate explicit gate, normally run before applying high-risk or release-bound changes.

## Search contract

### Input

The common path is deliberately short:

```json
{
  "workspace_id": "ws_...",
  "query": "resolveSession",
  "mode": "literal",
  "scope": {"path": "bridge", "tests": "exclude"},
  "within": {"file": "bridge/routing.go", "symbol": "resolveSession"},
  "filter": {"kinds": ["definition", "call"]},
  "detail": "compact",
  "limit": 40
}
```

- `mode` defaults to `literal`; regex metacharacters do not unexpectedly change an
  identifier search. `mode=regex` is explicit.
- `source` defaults to the current workspace revision. For refinement it is
  `{"result_set_handle":"set_..."}`; the service intersects further constraints with that
  frozen set rather than rerunning or widening the original search.
- The third source branch is bounded `git_history` with ref/range/limit and explicit
  `fields=message|path|diff`. It searches local objects only and returns typed historical
  result sets that cannot be consumed by `replace_matches`.
- `scope` accepts one path, glob or file allowlist through a single mutually exclusive
  object rather than three competing top-level fields.
- `filter.kinds` accepts definition, call, code, comment, string and unknown. Unknown
  classification is never silently discarded.
- `detail=compact|context`; additional source is fetched through `read`. Search is not a
  disguised multi-file reader.
- Ordering defaults to relevance by file, then source order within a file. `order=path`
  provides strict lexical/source order when required.

### Result-set refinement

A broad search is narrowed by calling `search` again with its `result_set_handle`. Existing
`scope`, `within` and `filter` constraints are monotonic intersections. A small `refine`
object additionally supports AND-only predicates over the original hits:

- `path|language|test|kind|has_diagnostic` metadata;
- `matched_text` literal or explicit regex;
- `line_contains` literal or explicit regex;
- `enclosing_contains` literal or explicit regex within the hit's enclosing symbol/section.

The parent query is inherited. The top-level `query` is omitted on a refinement call;
`refine.matched_text` expresses an additional text constraint. This prevents “refinement”
from being ambiguously interpreted as a fresh search inside the parent files.

There is no nested Boolean query language in v1alpha1. Multiple refinements are chained;
union, negation and widening start a new workspace search. A refined result returns a new
handle with `parent`, normalized constraints and counts retained/eliminated by reason. It
does not resend parent hits or source unless requested.

Result sets are typed as current-source or historical. Constraints not meaningful for the
set kind are rejected rather than ignored. Refining history never turns it into editable
current-source handles.

Completeness is inherited and can only decrease: a complete parent plus completely evaluated
predicates can yield a complete child, while an incomplete/capped parent can never become a
complete all-matches set merely by refinement. Pagination of displayed hits is not execution
incompleteness. `replace_matches` may consume a refined handle only when its full lineage is
revision-current, complete and non-overlapping.

### Structured result

```json
{
  "summary": "7 matches in 3 files; 6 semantically classified",
  "query": {"text": "resolveSession", "mode": "literal"},
  "coverage": {
    "complete": true,
    "files_considered": 18,
    "files_searched": 18,
    "files_skipped": [],
    "classified": 6,
    "unclassified": 1,
    "truncated": false
  },
  "result_set": {
    "handle": "set_...",
    "parent": null,
    "revision": "wsrev_...",
    "matches": 7,
    "files": 3,
    "complete": true
  },
  "groups": [
    {
      "file": "bridge/routing.go",
      "matches": 4,
      "hits": [
        {
          "line": 231,
          "column": 6,
          "excerpt": "func resolveSession(...)",
          "spans": [[5, 19]],
          "kind": "definition",
          "test": false,
          "match_handle": {
            "handle": "rng_...",
            "display": "bridge/routing.go:231 exact match in resolveSession"
          },
          "enclosing": {
            "handle": "sym_...",
            "display": "bridge/routing.go:231 resolveSession function"
          },
          "diagnostic": null
        }
      ]
    }
  ],
  "next_cursor": null
}
```

### Improvements over agent99 grep

- Structured fields replace dense bracket tags that models must parse from prose.
- Literal is the safe default; regex remains fully available.
- One excerpt is returned per hit without repeating signatures/doc comments already shared
  by the enclosing handle.
- File grouping and match spans make follow-up reads deterministic.
- Each hit has a directly editable match handle, so a known fragment can normally be
  searched and replaced in one search plus one guarded edit.
- A complete result has one frozen result-set handle, so all-match replacement does not
  require returning or resubmitting every hit.
- Ranking does not hide source order inside a file.
- Coverage reports skipped/binary/inaccessible files, classification gaps and truncation.
- Optional `include.history=last_change|recent` adds deduplicated commit handles and provenance
  for current-source hits. `include.commit_paths=true` returns a bounded path index once per
  distinct commit, not repeated on every hit.
- Pagination stops at group/hit boundaries; it never byte-truncates the primary error or
  silently cuts a file halfway through.
- Empty results distinguish `no_match`, `invalid_scope`, `unsupported_classification`
  and `incomplete_search`.

Structural Tree-sitter queries were designed as a `search.mode=structure` closed
discriminated branch with `language`, query and capture fields; literal/regex schemas would
not expose those fields, and there is no separate `syntax_query` tool in v1alpha1. That
branch is not implemented: the shipped `search` schema accepts `mode=literal|regex` only.
The design is deferred, not withdrawn; adding the branch is a compatible v1alpha1 change
because it introduces a new enum value and fields that are absent today.

### Compact text fallback

Clients that lose `structuredContent` receive exactly this deterministic shape:

```text
7 matches in 3 files [complete] set=set_... rev=wsrev_...
bridge/routing.go (4)
  231:6 definition rng_... | func resolveSession(...)
  248:9 call       rng_... | session := resolveSession(...)
more cursor=cur_...
```

The first line carries outcome, coverage, result-set handle and revision. Each file path is
printed once; each hit is one physical line containing display coordinate, kind, editable
handle and escaped/truncated excerpt. A final line appears only for a cursor or coverage gap.
No signatures, enclosing-symbol descriptions, diagnostics or arguments are duplicated from
structured fields; requested context/detail is retrieved through `read` or `evidence_get`.

## Common model recipes

| Intent | Expected first call | Normal continuation |
| --- | --- | --- |
| Understand an unfamiliar repository | `workspace_open` | `workspace_inspect(view=map)`, then `symbol_find` or `search` |
| Find text/call sites | `search` | `read` or `navigate(references)` |
| Explain when/why a line changed | `read(target, view=history)` | `read(commit_handle, view=changes)` if more detail is needed |
| Search recent changes | `search(source=git_history)` | refine result set or read commit changes |
| Read one function/config section | `symbol_find(include_source=true)` | none or `read(handle)` |
| Change one function | `edit_apply(replace_symbol)` | inspect returned evidence |
| Add one adjacent declaration/key | `edit_apply(insert_after)` | inspect returned evidence |
| Replace every inspected text match | `search` | `edit_apply(replace_matches, result_set_handle)` |
| Apply a known bounded text rule | `edit_apply(replace_matches, query + expected counts)` | inspect returned evidence |
| Multi-file refactor, operations known | `change_plan(action=prepare, inline=...)` | inspect details if needed → apply |
| Interactive multi-file plan | `change_plan(action=create)` | edit → preview/prepare → inspect/revise → apply |
| Fix a diagnostic | `code_actions` | add `apply_code_action` to a transaction or use `edit_apply` |
| Explain a stale refusal | `revision_diff` | reread/repreview; no automatic rebase |
| Run tests/checks without editing | `verify_run` | `evidence_get` on failures |

## Result and recovery ergonomics

Every result carries the common Huyang envelope plus:

- a one-sentence summary;
- explicit coverage/confidence;
- primary failures and conflicts inline;
- `next` entries containing valid tool names and minimally sufficient arguments;
- evidence IDs for detail;
- no causal attribution unless the service has evidence for it.

A model should not memorize the plan state machine. For example, READY offers apply,
inspect and discard; CONFLICTED offers inspect, revision diff and discard; PROVISIONAL
offers the missing verification stage and discard. Invalid transitions return current
state and valid next actions rather than a generic error.

## Code actions before and after edits

`code_actions` is a read-only default tool and is available before any Huyang edit. It
accepts a file position, semantic/range handle, diagnostic ID or prepared transaction
location. This covers both diagnostic quick fixes and source/refactor actions that do not
require an existing diagnostic.

Each returned action is revision-bound and includes its title, kind, originating diagnostic
when any, action handle, and the write set known from LSP resolve/preview. Listing an action
never mutates. Apply it through `edit_apply(operation=apply_code_action)` for one small
action or add it to a plan. Actions with an unknown write set prepare only in a sandbox; the
resulting complete diff must become declared plan operations before apply.

A provider-advertised source/fix-all action is one action only when resolve binds its exact
scope and workspace edit to the revision. Huyang does not manufacture a synthetic fix-all by
blindly replaying individual quick fixes: potentially interacting actions are separate plan
operations, resolved and conflict-checked together. Diagnostics may return compact action
counts/handles, but full action titles and edits are fetched only when requested.

## Delayed diagnostics

Modern Huyang does not attach a full delayed verdict as unexplained prose to the next
unrelated tool reply. It does guarantee delivery through a durable per-workspace diagnostic
inbox:

- `edit_apply` and `change_plan(action="prepare")` wait for the configured authoritative barrier
  within their deadline.
- If evidence is incomplete at the deadline, the result is `PROVISIONAL`, names the
  missing coverage, returns the evidence/transaction ID, and offers `diagnostics` or
  plan inspection in `next`.
- Later publications are stored against their producer, document revision, first-seen
  workspace state sequence and candidate transactions.
- Every later result for that workspace, until acknowledgement, carries a compact
  `diagnostic_updates` notification with diagnostic IDs, severity, location and attribution
  summary. Full messages/evidence remain behind its cursor and IDs.
- `diagnostics(since=cursor)` or `change_plan(action="inspect")` retrieves the structured update.
  Clients supporting notifications/subscriptions receive the same signal immediately; the
  response inbox remains the reliable fallback.

### Culprit attribution

Attribution is evidence-ranked, not inferred from arrival time alone:

- `exact`: the diagnostic came from a transaction sandbox/provider and producer document
  version belonging only to that transaction.
- `strong`: the producer version equals a committed postimage and no other transaction or
  external change touched the relevant document/dependency path before first sight.
- `likely`: the transaction changed the containing/referenced symbol or an impact-graph
  predecessor, and the diagnostic first appeared after that change.
- `ambiguous`: several transactions or an external write are plausible; return every
  candidate and the evidence for/against each.
- `unattributed`: there is not enough evidence. Never manufacture a culprit.

Each update carries the diagnostic's first-seen producer/version/state sequence and ranked
transaction candidates with reasons such as `postimage_version_match`,
`changed_referenced_symbol`, `direct_importer` and `first_seen_after_commit`. A later
transaction that resolves it is recorded separately as `resolved_by`; resolution does not
prove which earlier transaction caused it.

Culprit scores are presentation aids, never commit-policy thresholds. Clean/commit decisions
use the diagnostic and coverage evidence itself, not the attribution guess.

## Compatibility mapping

For readers who know the agent99 roster, its names map as follows. No adapter for those
names ships; this table is a reading aid, not a promise.

- `workspace_tree`, `workspace_map` → `workspace_inspect`
- `skim`, `document_symbols`, `buffer_lines`, `read_file` → `read`
- `grep` → `search`
- `find_symbol`, `workspace_symbols` → `symbol_find`
- definition/type/implementation/references/hover/call tools → `navigate`
- existing edit/file/refactor tools → `edit_apply` or a one-operation transaction
- `run_tests`, `check_project` → `verify_run`
- `undo_edit` → discard before apply, or a compensating plan after apply

The mapping is behavioral: modern schemas do not repeat every historical parameter.

## Pre-implementation usability requirements

These are contract work, not post-implementation polish:

1. **One target shape.** Tools that address code accept the same `target` union:
   handle, file/range, or human symbol locator. Do not rename the same fields between search,
   read, navigation, code actions and edits.
2. **Bounded batching.** `read`, `symbol_find`, `navigate` and `diagnostics` accept a
   small target array with per-item results. This removes repetitive round trips without
   turning one failure into an all-or-nothing read. Mutations remain explicitly atomic.
3. **Safe retry semantics.** Every stateful request accepts an idempotency key and returns
   whether it was created, resumed or replayed. Transport reconnects cannot duplicate an edit
   or commit.
4. **Compact recovery errors.** Conflicts return stable code, current state, affected target,
   evidence and executable `next` suggestions. They do not require the model to interpret a
   paragraph and invent a recovery call.
5. **Schema and description budgets.** Measure tool-list tokens as a first-class budget.
   Descriptions state when to use the tool and its important safety rule; detailed tutorials
   live in documentation/examples, not every tool declaration.
6. **Profile-filtered catalogs.** Fixed lean profiles omit unrelated tool families. A tool
   whose workspace backend is broken remains visible and returns actionable degradation
   evidence; catalogs do not churn with provider health. Ordering is deterministic.
7. **Preview parity.** Preview and apply use the same resolver and operation object. A preview
   cannot describe a change that apply would reinterpret.
8. **Consistent pagination.** Search hits, reads, diffs, diagnostics and evidence use one
   cursor convention and never truncate inside the primary failure or selected target.
9. **Workspace opening pays for itself.** `workspace_open` returns the compact overview,
   capabilities, current revision, bounded recent local commits and likely next discovery
   calls, avoiding an obligatory second status/history call.
10. **No hidden state prerequisites.** Handles and transactions improve later calls, but
    every refusal remains understandable from its human-readable locator, revision and
    conflict evidence.
11. **Acknowledged diagnostic inbox.** Pending late updates persist across adapters and
    reconnects until acknowledged by cursor, so a model cannot accidentally lose them by
    making an unrelated call.
12. **Golden recipes and negative tests.** Test both the intended tool and tempting wrong
   tools for each common task. A roster is not simple merely because the correct call works;
   it is simple when nearby alternatives clearly do not claim the same job.
13. **Hot-path completeness.** `edit_apply` returns the compact patch, canonical revision,
   formatting/tool delta, diagnostic delta, verification coverage and durable receipt.
   `change_plan(action="prepare")` returns the equivalent prepared summary. Neither requires
   an inspect/status call merely to discover whether it worked.
14. **Inline preparation.** Already-known operations go directly to
   `change_plan(action="prepare")`; create/edit calls are ceremony only when the plan itself
   is being iterated.
15. **No boolean soup.** Mutually exclusive behaviors use discriminated unions. Defaults are
   few, uniform and printed by `workspace_open`; unknown or irrelevant fields are rejected.
16. **Quiet steady state.** Results do not echo arguments, repeat unchanged capabilities,
   duplicate source in prose and structured data, or replay acknowledged lengthy warnings.
   The default is one sentence plus structured deltas; `next` contains at most the two most
   useful continuations, and details stay behind evidence IDs.
17. **Piggyback freshness.** Successful stateful calls return the new revision and acknowledge
   the diagnostic cursor they consumed. Models do not spend calls refreshing status or
   acknowledging notifications when the current response already proves freshness.

## Graceful degradation and environment repair

`workspace_open` must succeed for an accessible text tree even when every optional provider
is absent or broken. The minimum service is its own bounded filesystem walker, literal/regex
search, exact byte reads, hash-guarded range/file edits, diff production, revision tracking
and recovery journal. It must not depend on Git, Neovim, Tree-sitter, ripgrep, an LSP, a
formatter or a working project command merely to be useful.

Capability loss narrows evidence; it does not corrupt semantics:

| Missing or broken layer | Still available | Honest limitation |
| --- | --- | --- |
| LSP | text search/read/edit/diff; parser symbols when available | references, rename, code actions and type diagnostics are unavailable or partial |
| Parser | LSP semantics when healthy; exact range handles otherwise | no structural classification or `syntax_anchor` indentation |
| LSP and parser | raw text orientation and hash/anchor-guarded edits | no semantic claims; high-risk edits remain provisional unless policy accepts text-only evidence |
| Formatter/import organizer | byte-preserving edits and format check when separately available | no transform claim; failure never triggers a guessed cleanup |
| Test/build environment | source work, parser/LSP evidence and captured command failure | no passing-test claim; environment failure is distinct from test assertion failure |
| Git | the complete Huyang workspace/edit model | no Git annotations; Git is never a correctness dependency |
| Provider crash mid-call | canonical bytes and journal recovery | affected semantic evidence is provisional until restart/resync |

`workspace_inspect` doubles as the environment-repair report: failed provider/command,
resolved executable and arguments, working directory, sanitized exit/stderr evidence,
configuration source, last healthy time, restart attempts and the smallest safe suggested
repair. It never installs or rewrites configuration from an ordinary call. Project config,
toolchain files and scripts remain readable and editable through exact text operations, so an
agent can repair the environment that prevents stronger semantic service.

Fallback results use the same shapes as healthy results. They mark semantic fields
`unavailable` or `unknown`, include precise coverage, and never synthesize fake symbol handles
from uncertain text. Recovery is tested by disabling each layer independently and all layers
together; the all-disabled fixture must still orient, make a guarded text edit, show its exact
diff and undo/recover it.

## Cross-cutting contract consolidation

The following concepts each have one schema definition used everywhere:

- **Target:** `handle | file_range | symbol_locator`. Search matches, symbols, diagnostics,
  code actions, edit operations, breakpoints and debugger stack locations return or consume
  this target without renaming its fields. Handles are opaque, revision-bound and always have
  a compact human display fallback. Exact byte offsets are authoritative; displayed lines
  and columns are one-based Unicode-scalar coordinates. Provider-specific UTF-16/UTF-8
  position conversion is internal and never leaks into mutation preconditions.
- **Outcome:** `ok | partial | provisional | conflict | unavailable | failed`. Transport or
  protocol failure is not used for an expected workspace outcome. Every non-`ok` outcome has
  a stable code, affected scope, coverage and at most two valid continuations.
- **Coverage:** the same complete/searched/skipped/capped/unavailable structure is used for
  search, semantics, diagnostics, formatting, checks, affected tests and debugging source
  mapping. “No findings” is meaningful only beside coverage.
- **Verification stage:** selection, execution, result, exact input revision, environment,
  duration, writes and evidence share one record for parser/LSP/format/check/test stages.
  `selected`, `running`, `passed`, `failed`, `unavailable`, `cancelled` and `timed_out` are
  distinct; selected tests are never reported as executed.
- **Durable work:** a slow formatter, diagnostic barrier, test or provider restart returns a
  stable work/evidence ID before the transport deadline. `evidence_get` reads pending or final
  state; completion also enters the normal workspace inbox. No generic polling/status tool is
  added, and an idempotent retry cannot start duplicate work.
- **Filesystem object:** regular text, binary, symlink, directory and missing path are explicit
  kinds. Encoding/BOM, newline convention, final newline, executable bit and symlink target
  participate in preconditions and diffs. Modern edits never follow a symlink for mutation or
  overwrite a special file implicitly.

Debugging reuses normal source handles and evidence. `debug_session(start|attach)` may accept
initial breakpoints, and every stop/control response includes the stop reason, top source
location, compact top-frame locals and changes since the previous stop. `debug_inspect` is
needed only for deeper stacks/scopes/variables/evaluation. Missing adapters, stale source maps,
optimized-away variables and evaluation side-effect risk are explicit coverage states rather
than generic debugger errors.

Arbitrary debugger evaluation is considered potentially side-effecting; expression syntax is
not proof of purity. `debug_inspect(action="evaluate")` defaults to `policy="read_only"` and
executes only when the adapter/runtime can enforce that guarantee. Otherwise it returns
`approval_required` without evaluating. `policy="allow_side_effects"` is explicit, never
inherited by later/watch evaluations, and the result records that debuggee state may have
changed. Ordinary scope/variable reads do not use evaluate as a shortcut.

These consolidations are deliberately internal schema reuse, not additional model-facing
tools. Generate JSON Schema, SDK types, compact rendering and conformance fixtures from the
same definitions so adapters cannot drift.

## Semantic-provider selection

Several LSPs or analyzers may be valid for the languages in one workspace. Huyang exposes
that choice without making the model route every request manually:

- `.huyang.toml` and trusted user configuration define named `analysis_profile` values such
  as `fast`, `default` and `full`. `workspace_open` selects one profile; absent a request, the
  checked-in/default deterministic policy applies.
- `workspace_inspect` lists each provider ID, language/file selector, capabilities, role,
  health, version/config provenance and whether it is selected. A provider may be primary
  for navigation/refactors while another contributes diagnostics or code actions.
- Navigation, rename and a single code-action application route to one primary provider per
  capability. Diagnostics and verification may aggregate an explicitly configured set. Every
  item and coverage claim records its provider ID; conflicting results remain separate.
- `navigate`, `diagnostics`, `code_actions` and `verify_run` accept an optional provider or
  analysis-profile override when meaningful. The normal call omits it. Overrides name
  discovered/configured provider IDs, never arbitrary executable commands.
- Fallback order is part of the visible profile. Huyang may follow that declared order after
  failure and reports that it did so; it never silently substitutes a semantically different
  server or merge two rename edits.

Provider processes are started lazily by selected capability where practical. A broken
provider degrades only its evidence dimensions, and its launch/configuration failure remains
available through the environment-repair report.

`navigate(relation="hover")` remains in v1alpha1. Although hover is inspection rather than
movement, it shares the same source target, provider routing, staleness and bounded result
contract as the other semantic relations. A separate `symbol_inspect` would overlap
`symbol_find(include_source)`, `read` and `navigate` for too little benefit.

## Formatting, indentation and whitespace contract

Huyang is byte-preserving by default. Applying an edit does not implicitly run an import
organizer, LSP formatter, repository formatter, indentation heuristic, whitespace normalizer
or trailing-space cleanup.

### Separate checking from transformation

Formatting has three explicit modes:

- `off`: preserve the declared edit bytes; still report syntax/parser evidence.
- `check`: run the configured non-mutating repository format gate and report what would
  fail. This is the default verification policy when a format gate is configured.
- `transform`: run the named formatter in the transaction sandbox and expose every byte it
  changes in the prepared diff and `tool_delta`.

`transform` is enabled only by an explicit transaction/edit policy or a trusted checked-in
workspace policy whose activation is visible in `workspace_inspect` and the preview. Merely
discovering a formatter or LSP capability never authorizes it to rewrite text.

### Exact scope and approval

- A range-capable formatter may edit only the requested range plus a declared bounded
  syntactic envelope. The result names that envelope.
- A whole-file formatter is honestly labeled whole-file. It runs only in the sandbox and
  cannot be described as range formatting.
- Changes outside declared target ranges appear separately in `tool_delta`, grouped as
  indentation, line endings, trailing whitespace, wrapping, import ordering and other text.
- A formatter touching undeclared files, binary data, generated outputs or more than the
  configured line/file budget makes preparation `PROVISIONAL` or `FAILED`; it cannot be
  silently absorbed.
- The caller may accept formatter changes by preparing/applying that exact
  `prepared_revision`, reject them by changing the plan policy, or turn them into explicit
  operations. There is no hidden “accept all formatting.”

### Indentation

Indentation behavior belongs to an explicit `indentation` policy:

- `exact`: write the supplied bytes exactly; default for text/range/file content.
- `syntax_anchor`: preserve the existing indentation outside a syntax node and interpret
  replacement lines relative to that node; permitted only when Tree-sitter identifies one
  unambiguous node.
- `formatter`: delegate indentation to the explicitly selected transform formatter.

No mode guesses indentation width from nearby lines and then shifts arbitrary text.
`syntax_anchor` refuses mixed or ambiguous leading whitespace instead of “fixing” it.
Symbol and search-hit operations should target node byte columns so indentation preceding the
node remains untouched wherever possible.

### Byte invariants

Unless an explicit operation or transform says otherwise, preserve:

- tabs versus spaces;
- line-ending convention;
- final-newline presence;
- BOM/encoding markers;
- trailing whitespace outside the target;
- blank-line count outside the target;
- text sharing the target's first or last physical line.

Preview and apply compare content hashes over exact bytes, not normalized lines. The parser is
run after the declared edit and again after any transform. A transform that introduces a parse
error, changes protected string/comment literal bytes, or violates its declared scope is
discarded and reported with evidence.

### Required regression fixtures

Freeze the agent99 formatting failures as adversarial tests: import organization reindent,
whole-file formatter answering a range request, Python nested declaration dedent, mixed tabs/
spaces, JSON separator placement, declarations sharing a line, line-ending/final-newline
changes, string-literal rewrites, unrelated trailing-space cleanup, formatter nondeterminism
and exact rollback of formatter output.

## Simplicity acceptance gate

Before freezing v1alpha1, run the same short tasks against the modern and legacy rosters:

1. Orient in an unfamiliar repository and identify the implementation of a named feature.
2. Find production callers without prose/test false positives.
3. Make and verify one guarded symbol edit.
4. Prepare but do not commit a two-file signature change.
5. Diagnose a staged error and apply an offered code action.
6. Explain and recover from a stale revision conflict.

Record first-tool selection, invalid calls, retries, schema tokens, result tokens and whether
the model falsely inferred complete coverage. The modern roster passes when models choose
the intended first tool consistently, require no undocumented state knowledge, and do not
regress task quality. This evaluation belongs to baseline stage S00; it changes the contract
before implementation only when the evidence shows a real ambiguity.

## Deliberate omissions from v1alpha1

- No automatic transaction rebase or silent handle retargeting.
- No generic shell execution tool.
- No Git mutation tools for status/diff/commit/branch/stash/rebase/push; agents use the Git
  CLI and the user's normal authorization boundaries.
- No separate tree/map/skim aliases in the modern roster.
- No automatic language/tool installation from an ordinary read or edit.
- No monolithic “do anything” tool combining search, read, edit and verification.
- No exposure of the legacy one-tool-per-debug-action roster in the modern `full` profile.
- No generic project-wide edit macro language. Repeated transformations use visible ordinary
  plan operations, a revision-bound LSP code action, or a configured codemod in the sandbox.
  A future named/versioned edit-recipe extension may be added only if it deterministically
  expands to reviewable plan operations with declared writes and no extra apply semantics.

## Git boundary

Huyang is Git-aware but not a Git control client. In an initialized project it provides
bounded, read-only source history through existing `read` and `search` tools:

- `workspace_open` includes `recent_commits`, defaulting to the last three first-parent
  commits (configurable from zero to a small fixed cap). Each compact entry has commit handle,
  abbreviated object ID, subject, author display name, authored/committed time and changed-path
  count. Email, bodies and path lists are not returned automatically.
- `read(target=<line|range|symbol|file>, view="history", limit=N)` returns blame/provenance
  spans, recent commits touching the target and typed commit handles.
- `read(target=<commit_handle>, view="changes")` returns the commit summary and paged
  changed paths/hunks, answering which other files changed with a line.
- `search(source={"git_history":{...}})` searches bounded local commit messages, paths and
  patch text. Current-source search can request deduplicated last-change/commit-path context.
- File age is never a bare number. Results name the ref, traversal and rename policy and
  report `first_parent_commits_since_last_change`, last-change dates, and optionally the
  bounded/uncertain introduction commit. “Created” is provisional when history is shallow or
  rename following is ambiguous.

Canonical dirty lines with no committed origin are `uncommitted`; prepared-only lines are
`derived_from_plan`. Huyang may map unchanged portions back to committed spans but never
assigns new text fake blame. Shallow clones, missing objects, replace/graft history, submodules
and rename-detection caps are explicit coverage gaps.

History access is local and inert: no fetch/network, ref update, checkout, index/worktree
write, hook, pager, external diff, textconv or repository-configured executable. Implementation
uses safe plumbing/library operations or a sanitized read-only Git subprocess and is covered
by adversarial repository-config tests.

Huyang may also read repository metadata to:

- identify the project root, HEAD, branch/detached state and dirty/untracked baseline;
- include Git state in workspace revisions, evidence and recovery reports;
- ensure a sandbox represents the actual working tree rather than only HEAD;
- detect Git operations that changed files underneath an open workspace;
- annotate changed public files and compare prepared bytes with the canonical tree.

It does not expose commit, branch, checkout, merge, rebase, stash, reset, push, pull or
remote-management tools. Those remain explicit Git CLI operations outside Huyang
transactions. Huyang observes and resynchronizes after them like any other external
filesystem change.

The public `change_plan(action="apply")` verb deliberately avoids `commit`: it applies a
verified plan to workspace files through the recovery journal but creates no Git object.
Internal implementation and documentation may still use “commit journal/window” for the
durability protocol when the context cannot be confused with Git.

## Contract-freeze decisions

1. Structural queries are the closed `search.mode="structure"` branch; there is no
   `syntax_query` tool.
2. Hover remains `navigate(relation="hover")`; no overlapping `symbol_inspect` is added.
3. Search's compact fallback is the fixed file-grouped rendering specified above.
4. Portable stdio launch is `huyang mcp --profile full|orient|edit|debug`, defaulting to
   `full`. Streamable HTTP routes are `/mcp`, `/mcp/orient`, `/mcp/edit` and `/mcp/debug`,
   with `/mcp` equal to `full`. These are fixed catalogs, not protocol negotiation. There is
   no legacy endpoint; administration beyond the two language-server tools remains CLI and
   configuration.
5. Debugger evaluate is potentially side-effecting unless the adapter/runtime enforces
   read-only execution. Lack of that proof requires explicit per-call side-effect policy.

S00 conformance/model-selection review found no measurable ambiguity or portability failure.
These decisions are frozen for v1alpha1; later implementation stages do not casually redesign
the surface. The amendment below records the one stage that did change it, and re-freezes.

## v1alpha1 amendment (S20b, 2026-09-11)

The S20b structural consolidation (`docs/plans/huyang-s20b-structural-consolidation.md`)
changed the shipped v1alpha1 surface in the ways listed here. Every change is additive or
compacting: no tool was renamed, no field changed meaning, no mutation precondition was
weakened and no non-executing call became executing. Everything below is taken from
`internal/bridge/modern_mcp.go`, `compaction.go`, `receipts.go`, `provider_call.go` and
`internal/workspace/errors.go` at the merge commit named at the end.

### Tools added after the S00 freeze

`language_server_status` (read-only, idempotent, scheduler class provider read) and
`language_server_setup` (destructive, requires `idempotency_key`, scheduler class canonical
write) were added during the post-rollout hardening rounds and are in the `full` profile
only. The catalog is therefore 19 tools in `full`, 8 in `orient`, 13 in `edit` and 12 in
`debug`; `validateModernRegistry` refuses to start a service whose catalog differs from those
counts, and every descriptor must declare a scheduler class.

### Input additions

- `change_plan.accept_provisional` (boolean): with `action=apply`, commit a `PROVISIONAL`
  plan by explicitly accepting its incomplete diagnostic evidence. Without it, applying a
  `PROVISIONAL` plan is refused with `provisional_not_accepted`. Only `READY` commits by
  default.
- `diagnostics.full` (boolean): return the complete report including finding bodies and
  per-dimension evidence IDs. The default report is the compact shape described below.
- `search.include_ranges` (boolean): return `byte_start`, `byte_end` and `range` on every
  hit. The default hit carries only `path`, `line`, `column`, `match` and the editable
  `handle`.
- `workspace_open.overview` (`compact|full`): `compact` (the default) summarises the tree by
  top-level entry; `full` returns the complete entry listing.

### Output changes

- Envelope `diagnostic_updates` is a per-client delta: it carries only the diagnostic
  notices this MCP session has not seen, at most 20 (`maxDiagnosticUpdates`), and is omitted
  when there are none. `diagnostic_updates_truncated: true` marks a reply whose delta was
  cut at that cap. Acknowledgement through `diagnostics(since=cursor)` prunes notices for
  every client.
- Envelope `next` holds at most two entries (`maxNextEntries`); the finaliser keeps the
  earliest ones. The advertised output schema declares `maxItems: 2`.
- `diagnostics` data is compact by default: `confidence`, per-dimension `coverage` with
  `state`, `confidence` and `reasons`, `cursor`, `new_count`, `resolved_count`,
  `resolved_ids`, `preexisting_count`, `provisional_reasons`, and `new` items with `id`,
  `document`, `producer`, `severity`, `range`, `message`, `attribution` and, when present,
  `code`, `source`, `document_revision` and `transaction_id`. Resolved findings collapse to
  their IDs. `full=true` returns the raw report.
- `search` hits are the compact shape above; anchors appear only with `include_ranges`.
- `workspace_open` returns the compact overview (`entry_count`, `top_level` with per-entry
  `path`, `kind`, `bytes` and directory `files`, `top_level_count`,
  `top_level_truncated`) and compact `recent_commits` (`handle`, `abbreviated_id`,
  `subject`) unless `overview=full`.
- `language_server_status` replaces each language's inline `install_options` list with an
  `install_options_key` naming the entry under the report's single `install_options` map.
- `change_plan(action=apply)` data carries `provisional_accepted` (the dimensions the caller
  accepted) and `missing_coverage` (the verification gaps) when a `PROVISIONAL` plan was
  committed; a refused apply returns `missing_coverage` and offers `accept_provisional` in
  `next`.
- `transaction.state` may now be `CONFLICTED` or `EXPIRED`.

### Codes and warnings added

- `provisional_not_accepted`: apply of a `PROVISIONAL` plan without `accept_provisional`.
- `plan_state_invalid`: an action that the plan state machine does not permit from the
  plan's current state, including an edit of a plan that retention compacted.
- `idempotency_receipt_evicted`: the receipt for this idempotency key was evicted by the
  retention caps; the call is neither replayed nor re-executed.
- `request_cancelled`: the request's context was cancelled, including by the client.
- `workspace_busy` (outcome `conflict`): the kernel refused the request because the
  workspace's provider is occupied.
- Warning `The replayed receipt was trimmed to its provenance fields by retention; evidence
  detail is available through evidence_get`, with `data.receipt_trimmed: true`, on a replay
  older than the receipt payload window.

The workspace lifecycle codes `commit_precondition_changed`, `commit_recovery_required`,
`workspace_epoch_changed`, `prepared_revision_changed`, `plan_revision_changed`,
`plan_validation_conflicts` and `provider_unavailable` are now typed (`CodedError` in
`internal/workspace/errors.go`) and classified by the bridge with `errors.As`, never by
substring; their text keeps the `code: detail` shape.

### Not implemented

`search.mode=structure` is not implemented and is deferred, as stated in the search
contract above.

### Fixtures

`fixtures/huyang-v1alpha1/contract-schema.json` is exercised by
`TestModernRegistryMatchesFrozenProfiles` in `internal/bridge/modern_mcp_test.go`, which
holds the four profile catalogs to the fixture's `catalogs` object and checks that catalog
generation is deterministic; its `tools` object was extended with the two language-server
tools in this amendment. `fixtures/huyang-v1alpha1/golden-results.json` and
`fixtures/huyang-v1alpha1/multi-provider.json` are S00 design artifacts: no test reads them
and their shapes (grouped search results with `excerpt` and `spans`, named analysis
profiles) describe the intended contract, not the compact output the code emits today.

### Re-freeze

The amended v1alpha1 contract is frozen from the S20b merge commit
`fe143fe97dc7ac0060489c0d0840086a6c93122d` on `feature/huyang`. A later compatible change
may add optional inputs, optional result fields or new stable codes; anything else is a new
API version with a separately named catalog.

## v1alpha1 amendment (agent usability, 2026-09-12)

The agent usability workshop (`bench/agent-efficiency`, `docs/agent-guide.md`) changed the
surface additively; every change below is an optional input, an optional result field, a
compaction of a default result, or a new stable code. No mutation precondition was weakened.

### Input additions

- `edit_apply.operation.kind` gains `replace_literal` (fields `old`, `new`, optional `path`,
  optional `expected_count`, default 1) and `create_file` (`path`, `content`).
  `replace_range` is unchanged. `target` is required only for `replace_range`.
- `edit_apply.verbose` (boolean): return the full change record, hashes and handle
  resolution. `edit_apply.format` (boolean, default true): run the native formatter (gofmt)
  over edited Go files after the edit and report what it changed.
- `read.targets` (array of `{path, start_line, end_line}` or `{symbol_locator}`): several
  reads in one call; `read.limit` no longer has a maximum.
- `verify_run.revision_or_transaction` accepts `current`.
- `edit_apply.workspace_id` is optional for `replace_literal` and `create_file` when
  `operation.path` is absolute: an exact one-document workspace is opened implicitly, as
  `read` already does, and the response carries `data.implicit_workspace: true` and the
  workspace identity. `replace_range` without a workspace answers `workspace_required`.
- Every workspace-scoped tool accepts `root` in place of `workspace_id`: the project
  workspace is opened, or reused when already open, inside the call, and the reply's
  `workspace` block names it. `workspace_id` is no longer a required property anywhere.
- `idempotency_key` is optional on stateful calls; a missing key is generated per call, so
  the call is not replay-protected, which is what omitting it means.
- `read.numbered` (boolean) prefixes each content line with its number and a tab.
- `search.paths` (array of substrings or globs) keeps only hits under those paths;
  `search.context_lines` (0 to 20) adds `context`, the numbered lines around each hit.
  The search schema no longer uses `oneOf`; exactly one of `query`, `result_set_handle`
  with `refine`, or `git_history` is still expected.
- Structured arguments (`target`, `targets`, `operation`, `operations`, `files`, `stages`,
  `paths`, `refine`, `git_history`, `edit`) that arrive as JSON text are decoded, for
  clients whose cached schema predates the server.

### Output changes

- Tool results are rendered as compact JSON (no indentation).
- `edit_apply` data is `changed_paths`, `canonical_changed`, `diffs` (one `{path,
  before_sha256, after_sha256, patch}` per file, the patch bounded at 4096 bytes),
  `from_revision`, `revision`, `document_revision` (or `document_revisions` for several
  files), `replacements` when not one, `relocated` when a stale handle was relocated,
  `format` when the formatter ran, `diagnostic_delta` with compact findings (`id`,
  `severity`, `message`, `path`, `line`, `code`, `producer`; resolved findings as IDs) and
  `verification`. `change`, `resolution` and `tool_delta` appear only with `verbose`.
- `read` data is `path`, `content`, `revision_id`, `lines` and the line window; symbol reads
  add `name_path`, `kind`, `handle`, `start_line`, `end_line` and `coverage`. The full
  snapshot is gone. `targets` answers `{files: [...]}` with per-target errors in place.
- `search` data drops the echoed query, mode, workspace and (when complete) coverage;
  `result_set` is the compact `{handle, match_count, file_count, expires_at}` plus
  `complete: false` or `all_matches_eligible: false` only when so, and `parent`,
  `retained`, `eliminated` only for a refinement.
- Friction pass (2026-09-12): `diffs` entries carry `patch` only with `verbose` (or a
  preview); `locations` (`path:line:column`) names each literal replacement instead.
  `diagnostic_delta` is omitted when both lists are empty and `verification` when the
  verdict is authoritative (the `ok` outcome states it). An edit whose files no parser or
  language server covers, or in a documents workspace, skips the semantic refresh and is
  `ok` with no recovery hints and no implicit-workspace warning.
- `diagnostic_updates` is attached only to mutating calls and `workspace_inspect`, never
  to reads. Its entries are `{id, kind, severity, path}` (`attribution` only when
  attributed), one per finding with its last state, newest first, findings whose last
  notice is `stale` dropped, capped at 5 with `diagnostic_updates_truncated`. The client's
  cursor advances to the newest notice examined, so a backlog is never replayed a page at
  a time.
- The envelope omits `evidence` when it has no IDs and `idempotency_persisted` when true;
  `evidence` is no longer a required output property.
- `verify_run` whose stages all skipped says why in `summary` (untrusted root and the
  config file that grants trust, or no command declared).
- `workspace_open` `capabilities` is `{semantic, not_available: {name: state}, failures}`:
  native facilities and available optional ones are not listed. `overview.top_level`
  entries drop `bytes`; `recent_commits.coverage` appears only when history is unavailable.
- `tools/list` no longer attaches the output envelope schema to every tool.
- Second friction pass (2026-09-12): `edit_apply.operations` (array of `replace_literal`
  or `create_file` operations, 1 to 64) applies them in order in one call; the reply is
  the merged edit record (`changed_paths`, `locations`, `replacements`, `diffs`) and a
  refusal carries `failed_operation`, `applied_operations` and the paths already changed.
  `navigate.symbol` names a declaration instead of `target` (resolved natively or through
  the provider; `symbol_not_found` and `symbol_ambiguous` are conflicts that list the
  choices). `search.mode` accepts `references`, `definition`, `implementation`,
  `type_definition`, `incoming_calls` and `outgoing_calls`, answered through navigate
  with `query` as the symbol; without a language server the reply is the literal search
  with a warning. `search.include_handles` (boolean) adds `handle` and `column` to hits,
  which are otherwise `{path, line, match}`; `result_set` drops `expires_at`.
  `verify_run.verbose` restores the full stage record; the default stage is `{stage,
  status, exit, duration_ms}` plus `output` when the stage did not pass (`output_bytes`
  and `output_truncated` when it passed with output), `skipped` reasons, `scope`, test
  scope and verdict, and executed/selected test counts. `navigate` data is `{navigation}`
  plus `provider` only when degraded. Every reply except `workspace_open` names the
  workspace as `{id, revision}`.
- `workspace_open` data adds `commands` (`source`, `detected_from`, `format_gate`, `check`,
  `tests`, `state`, `trusted`, and one of `run`, `enable`, `override`, `hint`) and reports
  compact `capabilities` (`native`, `optional`, `semantic`, `failures`) and
  `semantic_provider` (`backend`, `state`, `failure_code`).
- `verify_run` stage records omit empty lists, empty strings, false flags and zero counts.
- `workspace_inspect.pipeline_state.state` may be `detected_trusted` or
  `detected_untrusted` when commands were detected from the repository layout.

### Codes added

- `literal_not_found`, `literal_whitespace_mismatch` (with `data.actual`, the exact document
  text, and `data.locations`), `literal_count_mismatch` (with `expected_count`, `found`,
  `locations`) and `create_target_exists`: all `conflict` outcomes that changed nothing.

### Behaviour changes

- The native sectioner covers Go and Python, so `read` by `symbol_locator`, `symbol_find`
  and the declaration position `navigate` sends to the language server no longer depend on
  the provider for those languages. Symbol coverage is incomplete only for other source
  languages (`parser_unavailable`), not for documentation and data files.
- A range handle whose bytes and preceding anchor are unchanged at its original offset
  resolves there even when the bytes after it changed (`format_only_relocation`), so
  several handles from one search survive being applied in any order.
- Sandbox materialization skips `.git`, so an IDE polling `git status` no longer makes
  `change_plan` prepare fail with `sandbox_source_changed`.
- Without a `.huyang.toml`, commands are detected from `go.mod` (gofmt gate, go build, go
  vet, go test), a Python project file (compileall, ruff when installed, pytest when
  installed else unittest) and a `package.json` test script (npm test). Execution still
  requires the root to be trusted.
