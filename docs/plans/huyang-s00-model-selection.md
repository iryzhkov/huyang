# Huyang S00 model-selection exercise

Method: deterministic contract review against the frozen v1alpha1 catalog. Each scenario was
presented as an intent with the 17 tool names and one-sentence responsibilities, then checked
for the intended first call, the nearest tempting alternative, required continuation, and
coverage error. This is a pre-implementation selection exercise, not a claim that Huyang
tools already execute.

| Workflow | Intended first call | Tempting wrong call | Recorded call sequence | Wrong choice / coverage mistake |
| --- | --- | --- | --- | --- |
| Repository overview | `workspace_open` | `workspace_inspect` | `workspace_open` → optional `workspace_inspect(view=map)` | None; opening pays for the first overview. |
| Production text search | `search` | `symbol_find` | `search(mode=literal, tests=exclude, kinds=definition/call)` | None; unclassified/skipped files remain in coverage. |
| Symbol lookup | `symbol_find` | `search` | `symbol_find(include_source=true)` | None; ranked handle ambiguity remains explicit. |
| One-shot guarded edit | `edit_apply` | `change_plan` | `edit_apply(operation=replace_symbol)` | None; one operation does not create plan ceremony. |
| Known multi-file edit | `change_plan` | repeated `edit_apply` | `change_plan(action=prepare, inline=...)` → `apply` | None; prepared and plan revisions are both required. |
| Diagnostic repair | `code_actions` | `diagnostics` or immediate edit | `code_actions` → `edit_apply(apply_code_action)` | None; listing is read-only and action stays revision-bound. |
| Debugger fault localization | `debug_session` | `debug_inspect` | `debug_session(start, initial_breakpoints)` → `debug_control`; deeper `debug_inspect` only if needed | None; evaluate remains blocked without enforceable read-only policy or explicit side-effect permission. |
| Stale-revision recovery | `revision_diff` | automatic retry/rebase | `revision_diff` → reread/repreview → new guarded mutation | None; no silent rebase or handle retargeting. |

## Acceptance observations

- First-tool selection is unique in all eight cases.
- No scenario requires undocumented connection state.
- The shared operation union prevents `edit_apply` and `change_plan` from diverging.
- The likely coverage mistakes—treating targeted tests as full, silence as clean, a capped
  search as complete, or a prepared plan as canonical—are explicitly prohibited in result
  shapes and golden fixtures.
- Search refinement stays monotonic and typed; historical result sets cannot become editable.
- Compact results retain revision, coverage, primary error, and a bounded continuation.
- Fixed launch profiles do not affect permissions or correctness.
- No semantic amendment to `huyang-tools-v1alpha1.md` was required.

Executable paired-model trials, retries, actual result tokens, and task-quality comparisons
remain an S07/S20 gate because no modern server or generated schemas exist in S00. S00 freezes
the cases, expected choices, and failure rubric so those later trials cannot redefine success.
