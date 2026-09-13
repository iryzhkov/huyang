# jsts language-expansion — worker raw report (setup checkpoint)

- Session: language-expansion-jsts
- Provider/model: opencode, opencode/muse-spark-1.3-contributor-free; no subagents/model substitution
- Coordinator code baseline: 2615bf0 (not read; no coordinator impl/reports/verdicts accessed)
- Huyang binary baseline: 5d244adf13c978599a267ee6f35b446faf7430c5
  - Verified: `go version -m /home/igor/.local/share/huyang/bin/huyang` reports
    `mod github.com/iryzhkov/huyang v0.0.0-20260913153935-5d244adf13c9`
- Coordinator thread: 92bdd669-fcfc-4a2c-90b1-3addf4dbe039
- Run ID (UNIQUE substitute): run-11afb9aa82383fe77a7c1f4848d7cee0 / attempt-b3f85f08f84984a5100556e063044400
- Working root (steward-owned isolated checkout):
  /home/igor/.local/state/t3-steward/backlog-v2-workspaces/workers/9a9842d1ddc92dee/run-11afb9aa82383fe77a7c1f4848d7cee0/task-c09bc66f485f20f529b509fed4ded590/attempt-b3f85f08f84984a5100556e063044400/workspace
  - Verified pwd is NOT /home/igor/Work/huyang
- Worker repo (own, nested git): <workspace>/work-jsts-11afb9aa
  - Huyang workspace_id: ws_2cbca74d8c9279c3e78982909456fd7b, revision wsrev_1 at open
- Target repo (own, nested git): <workspace>/target-jsts-11afb9aa
  - Absolute target path:
    /home/igor/.local/state/t3-steward/backlog-v2-workspaces/workers/9a9842d1ddc92dee/run-11afb9aa82383fe77a7c1f4848d7cee0/task-c09bc66f485f20f529b509fed4ded590/attempt-b3f85f08f84984a5100556e063044400/workspace/target-jsts-11afb9aa
  - Huyang workspace_id: ws_9155034de515f68a4f1375d5fa6ca9da, revision wsrev_6 after 5 copy_file ops
  - Target baseline commit: b8a6433a5db0dcc9ac1b7b40fb770032473e0afa (jsts initial synthetic fixture)
  - Fixture layout under jsts/: package.json, tsconfig.json, billing.ts, index.ts, main.js
  - Copies done via Huyang edit_apply operations kind=copy_file

## Fixture content (via Huyang read, wsrev_6)

- jsts/billing.ts (2 lines): export function quoteTotal(units: number): number { return units < 0 ? 0 : units * 7; } + export const diagnosticName = "quoteTotal";
- jsts/index.ts (1 line): export { quoteTotal, diagnosticName } from "./billing.ts";
- jsts/main.js (3 lines): imports { quoteTotal, diagnosticName } from "./index.ts"; asserts quoteTotal(3)==21, quoteTotal(-1)==0, diagnosticName=="quoteTotal"
- package.json: huyang-language-workshop private module
- tsconfig.json: allowJs/checkJs/noEmit, NodeNext, allowImportingTsExtensions, include *.ts/*.js

## UNIQUE placeholder

- No UNIQUE occurrence in any of the 5 fixture files. Nothing to replace. Run/session ID recorded above.

## Baseline check

- node --version => v26.7.0
- timeout 30s node main.js in target-jsts-11afb9aa/jsts => exit 0, no output (asserts pass)

## Goal 1 — declaration + references (completed)

- language_server_status (target wsrev_6): ts_ls ATTACHED for typescript (2 files) and
  javascript (1 file); json unconfigured. Provider embed healthy, capabilities include
  navigation, rename, diagnostics, code_actions. attached_server_count=1,
  attached_language_count=2. Attachment OBSERVED (not assumed).
- symbol_find quoteTotal: 1 match, function_declaration in jsts/billing.ts (lines 1-1).
  Semantic=embedded_nvim; coverage.complete=true but provider_evidence.complete=false
  (optional LSP-only symbols pending; parser-backed match available).
- search literal quoteTotal (paths jsts): 7 matches / 3 files (result_set handle
  set_e23dd93502a058b46e0adee460712806): billing.ts:1 (decl), billing.ts:2 (string
  value "quoteTotal"), index.ts:1 (re-export), main.js:2 (import), main.js:3 x3
  (2 call sites + string literal "quoteTotal").
- search mode=references quoteTotal: 5 hits, "resolved through the language server":
  billing.ts:1 decl, index.ts:1 re-export, main.js:2 import, main.js:3 two call sites.
  Excludes the 2 string occurrences (billing.ts:2 value, main.js:3 assert literal).
- Comparison: literal 7 vs semantic-references 5; delta = exactly the 2 string
  literals that must be PRESERVED (diagnosticName value + assert expectation).
- Bindings: single public exported function quoteTotal; no overloads; no local
  shadowing; barrel re-export in index.ts; JS consumer import + 2 calls.
- Evidence frames archived: jsts-evidence/g1-langstatus.json (5913 B),
  g1-symbolfind.json (4553 B), g1-literal.json (2551 B), g1-references.json (2971 B).

## Goal status after setup

- Goal 1 (semantic find/references): completed
- Goal 2 (prepared rename + apply): completed (with documented barrel-alias gap + typed refusal)
- Goal 3 (concise read + revision-only result via discovered controls): completed
- Goal 4 (behavior tests + candid report): completed

## Goal 2 — prepared rename, apply, gap recovery (completed)

- prepare-1 (key jsts-rename-prepare-1, req_271): PROVISIONAL plan plan_22f0b0b238e64245c4163df9723c20c6
  rev 1 / prep_ca96e8346d3d7d5cb561843ef8805326e596f64d32d8f1186cdf170090256d68. 2 ops:
  billing.ts decl (content 12 B = invoiceTotal), index.ts barrel (content 26 B =
  `invoiceTotal as quoteTotal`). Affected files: billing.ts + index.ts ONLY — main.js NOT included.
- Verification at prepare: format/check/tests unavailable (not_configured; parser_unavailable:.ts);
  diagnostics provisional (push_missing_current_document_proof). Evidence ev_8427acafcd6c51ddf2bcbf9ae6f2f16f
  = lsp_push from ts_ls#1, complete=false.
- Preview sizes explained: billing 132->134 (+2, one identifier; string untouched); index 59->75
  (+14 = ` as quoteTotal` alias the server emits to preserve the old public name).
- apply (key jsts-rename-apply-1, req_283, accept_provisional=true): COMMITTED, wsrev_6->wsrev_7,
  applied_from_provisional=true. Result: billing `function invoiceTotal`, index
  `export { invoiceTotal as quoteTotal, diagnosticName }`; main.js untouched (still quoteTotal).
- prepare-2 on barrel alias (key jsts-rename-prepare-2, req_289): TYPED REFUSAL, outcome failed,
  `rename-barrel-alias: symbol locator resolved to 0 declarations`. No mutation. Replay-archived
  (g2-refusal.json: idempotent replay, still failed, no new mutation).
- Gap recovery through Huyang (work the plan never touched — NOT a fallback redo of prepared ops):
  one edit_apply, 3 replace_literal ops (req_292, wsrev_7->wsrev_11, 4 replacements, no new
  diagnostics): index alias->plain `export { invoiceTotal, diagnosticName }`; main.js import;
  `quoteTotal(`->`invoiceTotal(` expected_count=2 (the `"quoteTotal"` string has no paren: untouched).
  Locations: index.ts:1, main.js:2, main.js:3 x2.
- No write-guard/trust weakening at any point. Frames: g2-prepare-evidence (3097 B),
  g2-plan-inspect (10835 B), g2-apply (12069 B), g2-refusal (1068 B).

## Goal 3 — concise read + revision-only result via discovered controls (completed)

- Discovered via --describe (frames archived): workspace_inspect view in
  {status,overview,map,revision}; read supports max_bytes/byte_offset/max_lines/start_line/end_line,
  view in {source,outline,history,changes}, response_mode in {full,compact}.
- workspace_inspect view=revision over target: revision-only data `{"revision":"wsrev_12"}`
  (no catalog); frame g3-inspect-revision.json 1527 B, text 699 chars.
- read response_mode=compact targets=[jsts/index.ts]: delivered 61/61 bytes with revision_id
  docrev_0bfbe9dd...; frame g3-read-compact.json 1184 B.
- Descriptor frames: g3-desc-inspect.json 1313 B, g3-desc-read.json 7409 B.
  Transport parsing piped via `python3 -c` (no heredoc). No large catalogs kept in context.

## Goal 4 — behavior tests + candid report (completed)

- Baseline (pre-change): `timeout 30s node main.js` => exit 0 (node v26.7.0, type-stripping runs .ts).
- Post-rename: exit 0. diagnosticName string value preserved in billing.ts AND main.js assert.
- Edge-case test added (wsrev_12, Huyang replace_literal on main.js:3):
  `assert.equal(invoiceTotal(0),0);` — zero-units boundary between the clamp (<0) and
  multiply branches. Rerun `timeout 30s node main.js` => exit 0.
- Per-goal: G1 completed; G2 completed (alias gap + refusal documented); G3 completed; G4 completed.
- Omissions: no tsc typecheck run (target has no .huyang.toml so verify_run stages report
  not_configured — recorded, not bypassed); no mode=definition search (references +
  symbol_find sufficed); no debugger use (not needed).
- Retries: MCP kind-as-string + edit_apply operation-wrapper shape (2 failed calls, recovered);
  change_plan requires idempotency_key (1 failed call, recovered). No language-server restart
  attempted (ts_ls attached on first probe; no bounded-retry loop needed).
- Unknowns: none load-bearing. (Why ts_ls emits the alias-preserving barrel form is a server
  behavior observation, not a claim.)
