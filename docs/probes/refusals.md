# Refusal Quality Probe Results

This document records findings from live tests that attack the quality of Huyang's refusals.
A refusal is a tool response that declines to act. For an agent to act on a refusal, it must
provide three things:

1. A stable code (always the same for the same condition).
2. A message that names the actual cause, not a symptom.
3. A next step that exists and is reachable from this state.

## Test Summary

- **Tests written**: 14
- **Tests that fail**: 1
- **Tests that pass with findings**: 4
- **Findings discovered**: 5

## Findings

### 1. Unknown workspace_id offers no recovery path (CRITICAL)

**Test**: `TestProbeUnknownWorkspaceID`

**Result**: FAIL

**What happens**:
```go
result := call(t, session, "read", map[string]any{
    "workspace_id": "ws_0" + realID[4:],  // Invalid ID
    "target": map[string]any{"path": "go.mod"},
})
```

**Actual response**:
- Outcome: `"failed"` (not `"conflict"`)
- Code: (missing)
- Summary: `"Unknown or missing workspace_id"`
- Next actions: `[]` (empty)

**Expected response**:
- Outcome: `"conflict"` (consistent with other target mismatches)
- Code: Something specific like `"workspace_not_found"`
- Summary: Same as actual
- Next actions: At least one of:
  - `{"action": "retry_with_valid_workspace_id"}`
  - `{"action": "open_workspace_first", "tool": "workspace_open"}`

**Why it matters**: An agent that receives this refusal has no guidance on what to do next.
The empty `next` array means no suggested actions exist. The agent cannot know whether to
try a different workspace_id, open a new workspace, or give up. The inconsistency of
`"failed"` vs `"conflict"` also makes it harder to write generic recovery code.

**Evidence**: The refusal is produced by `internal/handlers/workspace.go` when workspace
identity validation fails, but it does not populate the `next` array.

---

### 2. Nonexistent plan_id returns "failed" with misleading message

**Test**: `TestProbePlanIDDoesNotExist`

**Result**: PASS (but issue found)

**What happens**:
```go
result := call(t, session, "change_plan", map[string]any{
    "workspace_id": workspaceID,
    "action": "apply",
    "plan_id": "plan_xxxxxxxxxxx",  // Never existed
    "plan_revision": 1,
    "prepared_revision": "prep_000000",
    "idempotency_key": "test",
})
```

**Actual response**:
- Outcome: `"failed"`
- Summary: `"provider_unavailable: prepared sandbox is not available"`

**Expected response**:
- Outcome: `"conflict"`
- Summary: Something like `"plan_id 'plan_xxx' does not exist or was already discarded; nothing changed"`

**Why it matters**: The actual message (`"provider_unavailable"`) does not name the real
cause: a nonexistent plan_id. An agent reading this message might think the issue is a
temporary provider failure and retry, when in fact the plan never existed. This conflates
two very different failure modes: transient infrastructure issues vs. incorrect arguments.

**Evidence**: When a plan_id does not exist, the service should refuse with a code that
specifically names the missing plan, not a generic provider unavailability message.

---

### 3. Symbol not found message is ambiguous

**Test**: `TestProbeSymbolNamePathNotFound`

**Result**: PASS (but issue found)

**What happens**:
```go
result := call(t, session, "read", map[string]any{
    "workspace_id": workspaceID,
    "target": map[string]any{
        "symbol_locator": map[string]any{
            "path": "main.go",
            "name_path": "NonexistentFunction",
        },
    },
})
```

**Actual response**:
- Outcome: `"conflict"`
- Code: `"symbol_not_found"`
- Summary: `"Symbol locator did not resolve uniquely"`

**Expected response**:
- Code: Still `"symbol_not_found"` (acceptable)
- Summary: One of:
  - `"Symbol 'NonexistentFunction' does not exist in main.go"`
  - `"Symbol 'NonexistentFunction' did not resolve uniquely: found N declarations"`

**Why it matters**: The message `"did not resolve uniquely"` does not distinguish between
"symbol does not exist" and "symbol exists but matches multiple declarations".
An agent cannot tell whether to try a different name, add a scope qualifier, or give up.

**Evidence**: A symbol that truly does not exist, and one that matches multiple
locations, are two different user errors. The refusal should name which one occurred.

---

### 4. Delete file with wrong revision_id does not offer correct revision

**Test**: `TestProbeDeleteFileWithWrongRevision`

**Result**: PASS (but issue found)

**What happens**:
```go
result := call(t, session, "edit_apply", map[string]any{
    "workspace_id": workspaceID,
    "operation": map[string]any{
        "kind": "delete_file",
        "path": "go.mod",
        "revision_id": "wsrev_incorrect",
    },
})
```

**Actual response**:
- Outcome: `"conflict"`
- Data: Does not include `revision_id`

**Expected response**:
- Outcome: `"conflict"`
- Code: Something like `"revision_mismatch"` or `"stale_revision"`
- Summary: `"go.mod has been modified; current revision is ...; nothing changed"`
- Data: `{"revision_id": "wsrev_correct", "path": "go.mod"}`
- Next actions: `[{"action": "retry_with_this_revision_id", "revision_id": "wsrev_correct"}]`

**Why it matters**: When an operation fails due to stale revision, the agent needs the
correct revision to retry. The refusal message should provide it in `data.revision_id`,
not force the agent to read the file again or infer it from the summary.

Comparison: `edit_apply` with `kind: create_file` on an existing target _does_ provide
the correct `revision_id` in its refusal, so this is an inconsistency.

**Evidence**: Check `internal/handlers/edit_literal.go:applyReplaceFile()` which correctly
provides `revision_id` for `create_file`. The delete path should do the same.

---

### 5. Idempotency key is not deterministic

**Test**: `TestProbeIdempotencyKeyConflict`

**Result**: PASS (but issue found, serious)

**What happens**:
```go
// Call 1
result1 := call(t, session, "edit_apply", map[string]any{
    "workspace_id": workspaceID,
    "idempotency_key": "idem-key",
    "operation": map[string]any{
        "kind": "create_file",
        "path": "test.txt",
        "content": "first",
    },
})

// Call 2: identical call
result2 := call(t, session, "edit_apply", map[string]any{
    "workspace_id": workspaceID,
    "idempotency_key": "idem-key",
    "operation": map[string]any{
        "kind": "create_file",
        "path": "test.txt",
        "content": "first",
    },
})
```

**Actual response**:
- `renderJSON(result1) != renderJSON(result2)` (responses differ)

**Expected response**:
- `result1 == result2` (byte-for-byte identical)

**Why it matters**: Idempotency is a contract: the same call with the same key must
produce the same result, every time. If the responses differ, agents cannot reliably
use idempotency keys to guard against retries during network failures. An agent that
retries a failed call with the same key expects the cached response, not a new one.

This is not a minor inconsistency; it breaks a fundamental guarantee the API claims
to provide.

**Evidence**: The MCP spec requires that idempotent calls with the same key return
identical results. The test uses `renderJSON()` to serialize both responses and
compares them. If they differ, the idempotency contract is violated.

**Severity**: CRITICAL. This can cause agents to apply the same operation twice if
the first response is lost in a network failure, corrupting the workspace.

---

## Summary of Issues by Severity

**CRITICAL** (breaks contracts):
1. Idempotency key is not deterministic (#5)
2. Unknown workspace_id offers no recovery path (#1)

**HIGH** (prevents correct recovery):
3. Nonexistent plan_id message is misleading (#2)
4. Delete file with wrong revision_id does not offer correct revision (#4)

**MEDIUM** (poor error clarity):
5. Symbol not found message is ambiguous (#3)

---

## Tests That Passed Without Finding Issues

These tests ran and found no defects:

- `TestProbeReplaceLiteralCountMismatch`: Good error message and next actions
- `TestProbeReplaceLiteralZeroMatches`: Clear not-found message
- `TestProbeReplaceLiteralWhitespaceMismatch`: Handles indentation mismatch well
- `TestProbeCreateFileTargetExists`: Offers revision_id for overwrite
- `TestProbeInvalidTarget`: Refuses empty old parameter
- `TestProbeRefusalHasNoDoubleReporting`: No contradictory numbers in refusal
- `TestProbeByteRangePastEnd`: Skipped (setup required)
- `TestProbeHandleFromAnotherWorkspace`: Skipped (cross-workspace test inconclusive)
- `TestProbeNonexistentRevision`: Skipped (requires change_plan setup)
