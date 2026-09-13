# Huyang Debugging Usability Workshop Report: baseline-haiku

## Baseline & Environment

- **Baseline commit**: 2c22cbf24c4242eca17533866e86a9da1827e8c6
- **Model/Harness**: claude-haiku-4-5 / claudeAgent (default provider)
- **Huyang server**: /home/igor/.local/share/huyang/bin/huyang
- **Huyang module**: github.com/iryzhkov/huyang v0.0.0-20260913104659-2c22cbf24c42
- **MCP socket**: /run/user/1000/huyang/control.sock
- **Workspace root**: /home/igor/.local/state/t3-steward/backlog-v2-workspaces/workers/9a9842d1ddc92dee/run-a2117e2db07b56f5d799a37f4fb686e2/task-f1bf060151fe31083c7724dda5320d3f/attempt-8026da40c32fe35dccbac4c472c43b67/workshop-baseline-haiku
- **Workspace ID**: ws_6fcdfc3ebc502d2f7630e9be8dfc3d50

## Goal

Diagnose why `deliver` is absent from the failing invocation (`go run . fail`) compared with the passing invocation (`go run .`). The service must accept zero and positive credits, but reject negative credits.

## Diagnosis: Root Cause

**Conclusion**: The `dispatch()` function uses `if credits <= 0` to reject requests, which incorrectly rejects zero-credit requests. It should be `if credits < 0`.

### Evidence

1. **Fixture setup**: Copied go.mod, main.go, main_test.go from /home/igor/Work/huyang/docs/friction/2026-09-13-debug-usability/fixtures/delivery/ with SHA-256 verification.

2. **Test failure message**:
   ```
   credits=0: got "rejected", want "sent:0"
   ```
   The test explicitly expects zero credits to be accepted and sent as "sent:0".

3. **Invocation behavior before repair**:
   - `go run .` (credits=1): outputs "sent:1", exits 0 ✓
   - `go run . fail` (credits=0): outputs "rejected", exits 1 ✗

4. **Code analysis**:
   - main.go line 8-11: `dispatch()` function returns early with "rejected" when `credits <= 0`
   - Spec requires: reject only negative credits; accept zero and positive
   - When credits=0: condition `0 <= 0` is true, function returns "rejected" without calling `deliver()`
   - This is why `deliver()` is absent from the failing run

### Execution Flow

**Passing run** (`go run .` with credits=1):
```
main() → run(1) → dispatch(1) → (1 <= 0? no) → deliver(1) → "sent:1" → exit 0
```

**Failing run** (`go run . fail` with credits=0):
```
main() → run(0) → dispatch(0) → (0 <= 0? yes) → return "rejected" (deliver() never called)
```

The function never reaches `deliver()` in the failing case because the condition short-circuits.

## Minimal Repair

Changed line 9 in main.go:
```diff
- if credits <= 0 {
+ if credits < 0 {
```

This single-character change ensures:
- Negative credits: rejected (condition true, returns early)
- Zero credits: accepted (condition false, calls deliver(0))
- Positive credits: accepted (condition false, calls deliver(credits))

## Verification

After repair:
- `go run .` → "sent:1", exit 0 ✓
- `go run . fail` → "sent:0", exit 0 ✓
- `go test ./...` → PASS ✓

All acceptance criteria met.

## Huyang Tool Usage

- **workspace_open**: Opened fixture module, detected Go build/test commands
- **edit_apply**: Applied single-character replacement via `replace_literal`
- **read**: Inspected main.go to understand dispatch logic
- No debugging sessions or breakpoints needed; static analysis sufficient

## Friction Events

None encountered. Huyang tools functioned as documented. The socket was available and operations completed without retry or fallback.

## Observations

1. **Static analysis sufficient**: The bug was diagnosable from code inspection without runtime tracing. The test output directly identified the wrong output for credits=0.

2. **Minimal change principle**: Only one operator was changed. No surrounding refactor or error handling needed.

3. **Clear specification**: The test case provided unambiguous correctness criteria (three test inputs with expected outputs).

## Status

**COMPLETED SUCCESSFULLY**

All acceptance criteria verified. Report and fixture changes committed to assigned worker branch.

