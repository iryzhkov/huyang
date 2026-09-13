# Evidence Index: Assisted Transport Verification

## Summary
Huyang baseline v5d244ad tested via experimental MCP profile with Python transport helper.

## Evidence Files

### Goal 1: Large Payload (131KB) with Marker Excerpt
- **File:** `goal1-synthetic-json.md`
- **Content:** Synthetic JSON creation, read attempts, limitation analysis
- **Status:** JSON created (131,185 bytes), marker excerpt goal not achieved

### Goal 2: Workspace Revision with Minimal Output
- **File:** `goal2-revision.md`
- **Content:** Workspace open response, revision identifiers, response measurements
- **Status:** Workspace revision obtained (wsrev_3), full response 4,003 bytes

### Goal 3: Mutation Receipt from Edit Operations
- **File:** `goal3-edit-responses.json` (response data)
- **Content:** Normal edit_apply response with mutation receipt structure
- **Status:** Normal edit receipt captured (2,238 bytes), change_plan not tested

## Response Data Files
- `goal2-tools-list.json` — Tools available via MCP (7,694 bytes)
- `goal3-edit-ops-response.json` — Edit failure response (2,182 bytes)

## Key Measurements

| Goal | Metric | Value | Status |
|------|--------|-------|--------|
| 1 | Synthetic payload size | 131,185 bytes | ✓ Exceeds minimum |
| 1 | Marker excerpt response | 288,177 bytes | ✗ Exceeds 4,096 target |
| 2 | Workspace ID length | 36 bytes | ✓ Obtained |
| 2 | Full workspace response | 4,003 bytes | ⚠ Not minimal |
| 3 | Edit response size | 2,238 bytes | ✓ Measured |
| 3 | Mutation data size | 68 bytes | ✓ Extracted |

## Baseline Limitations Found

1. **response_mode=compact** — Not supported (returns empty response)
2. **max_bytes parameter** — Not functional (ignored)
3. **Streaming/chunking** — Not available
4. **change_plan prepare** — Returns empty response in experimental profile

## Service Quality

- ✓ Socket stability: Full 30+ minute session
- ✓ JSON-RPC compliance: Correct protocol implementation
- ✓ Error handling: Structured outcome/code fields
- ✗ Response compaction: No minimal output mode
- ✗ Large payload handling: No chunking support

## Transport Configuration

- **Helper:** `mcp_transport.py` (Python subprocess adapter)
- **Profile:** `experimental`
- **Protocol:** JSON-RPC 2.0, newline-delimited frames
- **Socket:** Unix domain socket

## Handoff Details

- **Working socket:** `/tmp/huyang-goals23-1585685.sock` (PID 1585688)
- **Test workspace:** `/tmp/test-workspace-goal2-simple/` (git repo)
- **Synthetic files:** 
  - `/tmp/huyang-test-workspace-1584625/goal1-large.json` (131 KB)
  - `/tmp/test-workspace-goal2-simple/code.txt`

---

All evidence collected, no user interaction required for continuation.
