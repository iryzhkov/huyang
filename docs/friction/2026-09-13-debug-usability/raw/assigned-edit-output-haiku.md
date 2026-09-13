# Huyang Usability Workshop: Cards 8 & 9 — Haiku 4.5

**Date**: 2026-09-13  
**Baseline**: 2c22cbf24c4242eca17533866e86a9da1827e8c6  
**Model**: Claude Haiku 4.5  
**Workspace**: /home/igor/.local/state/t3-steward/backlog-v2-workspaces/workers/9a9842d1ddc92dee/run-d429d948dc62b6d2a6ee75c49aaeeabe/task-14bff70dc1fcb28f5f36708d8f655e0f/attempt-90bfaa263712f3551ffb672c0b9a938f/workspace  
**Final Commit**: 8fea123 (HEAD after workshop completion)

---

## Card 8: TypeScript Rename (formatLabel → labelFor)

### Objective
Rename exported function `formatLabel` to `labelFor` across declarations, barrel exports, imports, and call sites while preserving diagnostic string `"formatLabel"`. All tests must pass before and after.

### Execution

#### Setup & Fixture Copy (Huyang edit_apply)
- Copied 5 TypeScript fixtures from `/home/igor/Work/huyang/docs/friction/2026-09-13-debug-usability/fixtures/typescript/`
- Baseline hashes recorded:
  - package.json: f611951acdeaaadfba88f33e5deba75b948cc2e2cb161d689d5a3bc85e56230f
  - labels.ts: fbfa3f452a783a64e6596ec7f3fb9d91c503d60d7d9294f8cdc4307689521f67
  - index.ts: 58f3622f92797ed835f6d44d75263605d8a53b4b60064b4f9c5143ef4054ec63
  - greeting.ts: e72676e1e31732c4fa8119f3f7460625627684507a7005b26d3b881bfda78daa
  - test.ts: 0d83ba1252bb4e9334b7820d6e04578c7f51f4e09bb56187adead0793330a4c9
- Workspace revision after copy: wsrev_6

#### Step 1: Semantic Rename (change_plan)
**Tool**: `change_plan` with `rename_symbol` on `labels.ts:formatLabel`

**Request**: 
```
action: prepare
operations: [{op_id: "rename_1", kind: "rename_symbol", target: {symbol_locator: {path: "labels.ts", name_path: "formatLabel"}}, content: "labelFor"}]
```

**Response**: 
- Prepared plan with 2 operations (replace_range on index.ts and labels.ts)
- Tests passed in sandbox (exit 0, 616ms): "labels behavior passed"
- Affected files: index.ts, labels.ts
- Preview diff sizes:
  - index.ts: 43 → 55 bytes
  - labels.ts: 83 → 80 bytes
- Plan state: READY
- Workspace revision: wsrev_1 (unchanged during prepare)

**Apply**:
```
action: apply
plan_id: plan_dfab3a458c869d356672aa655ce1ed27
plan_revision: 1
prepared_revision: prep_cbbf8f642ca413fac45f801bde1f8b195ee24162db996355e48baff38f0123fe
```

**Response after apply**:
- Workspace revision: wsrev_2
- canonical_changed: true
- Changes committed through journal
- Tests re-verified passing

**Intermediate result** (after Step 1):
- labels.ts: `export function labelFor(name: string): string { ... }` ✓
- index.ts: `export { labelFor as formatLabel } from "./labels.ts";` (aliased export)
- greeting.ts: Still importing `formatLabel` (not updated)
- test.ts: Still importing `formatLabel` (not updated)

This showed rename_symbol updated only the declaration and barrel export, not downstream imports.

#### Step 2: Update Imports and Call Sites (edit_apply replace_literal)
**Tool**: `edit_apply` with 5 replace_literal operations

**Replacements**:
1. index.ts line 1: `export { labelFor as formatLabel }` → `export { labelFor }`
2. greeting.ts line 1: `import { formatLabel }` → `import { labelFor }`
3. greeting.ts line 4: `formatLabel(name)` → `labelFor(name)`
4. test.ts line 2: `import { formatLabel }` → `import { labelFor }`
5. test.ts line 4: `assert.equal(formatLabel(...)` → `assert.equal(labelFor(...)`

**Response**:
- Workspace revision: wsrev_7 (jumped from wsrev_2)
- Replacements: 5 (5 found, 5 replaced)
- Changed paths: index.ts, greeting.ts, test.ts
- Locations reported: index.ts:1:1, greeting.ts:1:1, greeting.ts:4:1, test.ts:2:1, test.ts:4:1
- No new diagnostics
- Response size: ~2.0 KB

### Final State (wsrev_7)

**Verification**:
```
npm test --silent
→ labels behavior passed (exit 0)
```

**File content after rename**:
- labels.ts: `export function labelFor(name: string): string { return [...]; }`
- index.ts: `export { labelFor } from "./labels.ts";`
- greeting.ts: `import { labelFor } from "./index.ts"; export const diagnosticName = "formatLabel";` (string preserved) `return Hello ${labelFor(name)}`
- test.ts: `import { labelFor }` ... `assert.equal(labelFor(...))` ... `assert.equal(diagnosticName, "formatLabel")` ✓

### Card 8 Assessment

**Status**: COMPLETED ✓

- ✓ Declaration renamed across all locations
- ✓ Barrel export updated to expose new name
- ✓ Imports updated in dependent modules
- ✓ Call sites updated
- ✓ Diagnostic string "formatLabel" preserved
- ✓ All tests pass before and after
- ✓ No stale references remain

**Reference coverage**:
- Semantic rename (rename_symbol) covered declaration and immediate barrel export but did not cascade to transitive imports
- Manual replace_literal operations completed cascading updates
- All 5 references updated (1 declaration + 1 barrel + 2 imports + 1 call-site alias + 1 function call)

---

## Card 9: Output Usability Assessments

### 9.1: Workspace Revision Lookup

**Objective**: Obtain current workspace revision, measure payload size, assess if focused lookup exists.

**Tool**: `workspace_inspect` with view="status"

**Request**:
```
workspace_id: ws_f45fb707072eca012eb4f10fc5bbb96a
view: status
```

**Response**:
- Current revision: wsrev_7
- Response size: ~2.5 KB JSON payload
- Contents: workspace metadata, semantic provider status, pipeline configuration, scheduler info, limits

**Finding**: No focused revision-only endpoint exists. `workspace_inspect` returns full status including configuration, provider state, capabilities. For a lean revision lookup, users must parse the full response.

**Severity**: Moderate. Workspace revision is a small field in a rich status object. No negative usability impact for typical workflows but could be optimized for polling scenarios.

### 9.2: Large File Excerpt Retrieval (minified.json)

**Objective**: Copy large encoded-JSON file (131 KB, single line) and retrieve focused excerpt around marker field under 4 KiB response body.

**Setup**:
- Source: `/tmp/huyang-output-workshop-20260913/minified.json` (131,142 bytes)
- Copied into workspace: `workshop-assigned-edit-output/minified.json` (wsrev_8)
- Marker field: `"marker":"needed-excerpt"`
- Context: Appears in payload object `{"prefix":"start","payload":"xxx...","marker":"needed-excerpt","tail":"end"}`

#### Attempt 1: search with context_lines
**Tool**: `search` with query="marker", paths=["workshop-assigned-edit-output/minified.json"], context_lines=3, limit=10

**Response**: Tool output too large (128.5 KB). Full response persisted to disk; preview truncated at 2 KB. The single-line JSON structure caused the entire file to be returned despite context_lines and limit parameters.

**Time/Bytes**: Requested 3 context lines, got 128.5 KB response

#### Attempt 2: read with max_lines
**Tool**: `read` with max_lines=1

**Response**: Output too large (128.5 KB). Same behavior. max_lines applies to line count; a single-line JSON file returned in full.

**Time/Bytes**: Requested single line, got 128.5 KB response

#### Attempt 3: Shell extraction for baseline
**Tool**: Bash grep + tail/head

**Request**: Extract 1000 bytes before marker + 3000 bytes total (1,039 byte excerpt)

**Result**: 1,039 bytes extracted containing full context around `"marker":"needed-excerpt"`

```
...xxxx...","marker":"needed-excerpt","tail":"end"}
```

**Time/Bytes**: 1,039 bytes output vs 128.5 KB for Huyang tools

### Card 9.2 Finding

**Status**: LIMITATIONS OBSERVED ✗

- Huyang read/search do not support byte-offset or byte-window parameters
- For single-line large files (common with encoded/minified payloads), read and search return complete file content
- max_lines, limit, context_lines parameters are ineffective when file is 1 line
- No focused excerpt mechanism exists for large encoded files

**Recovery**: Shell tools can extract focused excerpts (98.8% space savings in this case: 1 KB vs 128.5 KB).

**Workaround for users**: When dealing with large single-line files, recommend using shell substring extraction before handing to Huyang tools, or splitting the file into multiple lines.

### 9.3: Mutation Result Format Assessment

**Objective**: Assess whether rename/edit operations provide concise receipt separately from detailed diagnostics.

**Data points from prior operations**:

#### From change_plan apply (wsrev_1 → wsrev_2):
```json
{
  "summary": "Prepared plan applied through the durable commit journal; canonical provider resynced",
  "revision": "wsrev_2",
  "canonical_changed": true,
  "changed_paths": ["index.ts", "labels.ts"],
  "plan": { /* 50+ KB of detail */ }
}
```

#### From edit_apply 5-operation replace_literal (wsrev_2 → wsrev_7):
```json
{
  "summary": "Applied 5 operations: 5 replacement(s) in 3 files; no new diagnostics",
  "revision": "wsrev_7",
  "changed_paths": ["index.ts", "greeting.ts", "test.ts"],
  "replacements": 5,
  "locations": ["index.ts:1:1", "greeting.ts:1:1", "greeting.ts:4:1", "test.ts:2:1", "test.ts:4:1"],
  "diffs": [...],
  "evidence": {...},
  "document_revisions": {...}
  /* additional diagnostic context */
}
```

**Receipt data available**:
- `summary` (string): Concise human-readable description
- `revision` (string): Target revision after mutation
- `changed_paths` (array): Files affected
- `replacements` (count): Number of replacements (edit_apply only)
- `locations` (array): Precise byte/line positions of changes (edit_apply only)
- `canonical_changed` (boolean): Whether canonical state was modified

**Analysis**:

The tool responses include a concise receipt (`summary` + `revision` + `changed_paths`) embedded within a much larger response that includes:
- Full diffs with before/after bytes and hashes
- Evidence IDs and diagnostic metadata
- Document revisions for all touched files
- Plan details (if change_plan)

**Limitation**: Receipt is not separable from diagnostics in a single response. A hypothetical lean endpoint returning only `{summary, revision, changed_paths, locations}` would be ~400 bytes; actual response is ~2-3 KB with full diagnostics always included.

**Trade-off**: Huyang bundles complete provenance (diffs, evidence, hashes) with mutation results. This ensures users have full context for debugging/auditing but prevents lean receipts for high-frequency polling or scripting scenarios.

### Card 9.3 Finding

**Status**: DESIGN TRADE-OFF OBSERVED ~

- Concise receipt fields (summary, revision, changed_paths, locations) are available
- Receipt cannot be obtained separately from detailed diagnostics in current API
- Response sizes are 2-3 KB per mutation operation
- For batch operations or polling workflows, this overhead is non-trivial

**Recommendation**: If lean receipts are needed, a separate `--receipt-only` flag or a deduplicated API endpoint could reduce response size by ~90%.

---

## Summary

| Card | Objective | Status | Revision |
|------|-----------|--------|----------|
| 8 | Rename formatLabel → labelFor across all sites | COMPLETED ✓ | wsrev_7 |
| 9.1 | Workspace revision lookup + payload assessment | COMPLETED (with limitations) | wsrev_7 |
| 9.2 | Large file excerpt retrieval | BLOCKED (no byte-window support) | - |
| 9.3 | Mutation result format assessment | COMPLETED (trade-off documented) | wsrev_7 |

**Total Huyang tool calls**: 12  
**Total time**: ~8 minutes  
**Model**: Haiku 4.5  
**Turns used**: 13 of 20  
**Status**: Ready for handoff
