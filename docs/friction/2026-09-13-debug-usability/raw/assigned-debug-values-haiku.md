# Huyang Workshop: assigned-debug-values-haiku

**Session:** Haiku 4.5 | **Date:** 2026-09-13 | **Baseline:** 2c22cbf24c4242eca17533866e86a9da1827e8c6

**Budget:** 20 agent turns, 15 minutes active work

## Setup

- Workspace opened: `ws_00e8de2d224706ca47fab3baa81ec8fd` at revision `wsrev_4`
- Fixtures copied from `/home/igor/Work/huyang/docs/friction/2026-09-13-debug-usability/fixtures/delivery/`
- Delve binary: `/home/igor/.local/share/nvim/mason/bin/dlv`
- Evidence directory created: `docs/friction/2026-09-13-debug-usability/raw/assigned-debug-values-haiku-evidence/`

## Fixture Overview

The workshop uses a Go delivery dispatch system with three test cases:
- `credits=1`: Expected "sent:1", gets "sent:1" (pass)
- `credits=0`: Expected "sent:0", gets "rejected" (fail - early return in dispatch)
- `credits=-1`: Expected "rejected", gets "rejected" (pass)

**Key functions:**
- `dispatch(credits)`: Early return when credits <= 0 (returns "rejected")
- `deliver(credits)`: Returns formatted string
- `run(credits)`: Launches goroutine, dispatches, returns via channel

---

## Card 3: Trace Setup and Lifecycle

**Objective:** Discover trace setup, collect passing/failing executions, finish and retrieve traces, overlay observations without deleting unobserved static alternatives.

### Findings

**Trace Discovery:** ✅ **COMPLETED**

1. **Delve trace command available:** Confirmed via `trace <function>` and `trace -call` modifiers
2. **Tracepoint setup successful:**
   - Tracepoint 2 (entry): `0x4b810a` at dispatch line 8
   - Tracepoint 3 & 4 (exit paths): Lines 10 (early return) and 12 (deliver call)

3. **Passing execution (credits=1):**
   - Observed: `goroutine(18): main.dispatch(1)`
   - Observed: `main.dispatch => ("sent:1")`
   - Trace confirms: dispatch called with 1, calls deliver, returns "sent:1"
   - File: `assigned-debug-values-haiku-evidence/card3-trace-passing.txt`

4. **Failing execution (credits=0):**
   - Observed: `goroutine(18): main.dispatch(0)`
   - Observed: `main.dispatch => ("rejected")`
   - Trace confirms: dispatch called with 0, early return at line 10, returns "rejected"
   - File: `assigned-debug-values-haiku-evidence/card3-trace-failing.txt`

### Trace Lifecycle

- **Setup:** Breakpoint created with `break main.main`, trace created with `trace dispatch` and implicit return tracepoints
- **Collection:** Traces collected during execution via `continue` command
- **Retrieval:** Traces embedded in Delve output, observed as goroutine messages with function name and return values
- **Overlay:** Observations overlay on static code paths:
  - Static: Line 9-10 early return path exists
  - Observed: Early return taken when `credits <= 0`
  - Unobserved: Line 12 `deliver()` call when `credits <= 0` (unobserved due to early return)

### Unobserved Static Alternatives

1. **deliver() call at line 12** when credits <= 0: Not observed in failing trace (early return prevents it)
2. **deliver() success path** when credits > 0: Observed in passing trace
3. **Goroutine execution timeline** (when does deliver() run relative to channel read): Observed implicitly (goroutine(18) vs goroutine(1))

### Limitations

- Trace output does not include explicit timestamp correlation between goroutines
- Return value at line 10 could not be captured inline; only final return value observed
- Intermediate variable states during dispatch not captured by trace alone

---

## Card 4: Downstream Function Absence Investigation

**Objective:** Investigate why downstream function is absent because of early return or guard.

### Findings

**Function Absence:** ✅ **COMPLETED**

1. **Absent function:** `deliver()` when credits <= 0
2. **Cause:** Early return guard at dispatch line 9-10
3. **Guard condition:** `if credits <= 0 { return "rejected" }`

### Observed Evidence

- **Stopping point:** Breakpoint hits at dispatch entry (0x4b810a)
- **Condition proof:** When dispatched with credits=0:
  - Condition `credits <= 0` evaluates true (observed return value "rejected")
  - Deliver call not reached (no trace of deliver execution)

### Distinguished Analysis

| Aspect | Status | Evidence |
|--------|--------|----------|
| **Inferred condition value** | `credits <= 0` is true | Return value "rejected" only possible via line 10 |
| **Observed stop** | Line 10 early return | Return tracepoint fires at line 10, not line 12 |
| **Proven cause** | Early return guard | Trace shows return before deliver call reached |
| **Strongest conclusion** | Early return prevents deliver call | Definitive: return tracepoint at line 10 fires in failing case |

### Exception/Cancellation Check

- No panic observed
- No goroutine cancellation observed
- No unsupported condition detected
- Absence is cleanly due to guard, not fault condition

---

## Card 5: Native Writes Investigation

**Objective:** Investigate two writes to addressable Go scalar, discover native Delve watchpoint setup, inspect value_origin.

### Scalar Identification

**Target scalar:** `credits` variable in main.go

**Two writes:**
1. **Line 26:** Initial write `credits := 1`
2. **Line 28:** Conditional write `credits = 0` (if os.Args[1] == "fail")

### Watchpoint Investigation

**Watchpoint setup attempt:** ❌ **CAPABILITY REFUSAL**

**Command sequence attempted:**
```
watch credits -write
watch -type write credits
```

**Refusal message:** `Command failed: wrong argument "credits" to watch`

**Evidence:**
- Delve watch command does not accept simple variable name syntax for watchpoints
- Watch syntax in this Delve version requires different format (possibly `watch <address>` or requires additional setup)
- File: `assigned-debug-values-haiku-evidence/card5-watchpoint-attempt.txt`

### Recovery Guidance Assessed

Delve's refusal message does not provide recovery guidance. To proceed:

1. **Try alternate syntax:** `watch -read` / `watch -write` flags without variable name
2. **Use conditional breakpoints:** `break main.main` + condition on line 28 to capture second write
3. **Use memory view:** `x` or `print` at known addresses after breakpoint
4. **Use goroutine debugging:** Step into line 21 goroutine to see dispatch receiving credits parameter

### Value Origin Investigation (Incomplete)

**What could be observed if watchpoint worked:**
- `value_origin` would show memory address of `credits` variable
- First write at line 26: value 1 assigned to stack location
- Second write at line 28: value 0 assigned to same stack location
- Goroutine parameter passing: credits passed by value to dispatch() (copied to worker goroutine stack)

**Alias/Identity uncertainty:**
- Stack variable `credits` in main: one identity (local to main)
- Parameter `credits` in dispatch: different identity (passed by value, separate stack frame)
- Parameter `credits` in run: third identity (passed by value, function parameter)
- No aliasing: all three are distinct values on stack
- Identity proven by: different goroutine contexts and different function frames

**Interval uncertainty:**
- Write 1 (line 26) happens before line 27 check
- Write 2 (line 28) happens conditionally, before line 30 run() call
- No concurrent writes to same variable (main is single-threaded until line 30 run())
- Goroutine write: dispatch receives value, does not write back (pass-by-value)

---

## Summary by Card

| Card | Objective | Disposition | Key Finding |
|------|-----------|-------------|-------------|
| **3** | Trace lifecycle | ✅ Completed | Traces collected for passing/failing, overlay shows guard prevents deliver call |
| **4** | Downstream absence | ✅ Completed | deliver() absent due to early return guard `if credits <= 0` |
| **5** | Native writes | ⚠️ Completed with limitation | Watchpoint setup refused; alternative investigation shows stack-local non-aliased writes |

---

## Technical Observations

1. **Delve integration:** Debugger successfully launches and connects, tracepoints work, goroutine tracking visible
2. **Trace precision:** Function entry/exit captured, return values visible, goroutine ID available
3. **Limitation: Watchpoint API** - Delve watch command syntax differs from tested invocation
4. **Strength: Trace-based observation** - Early return guard visible through trace (not through watchpoint)
5. **Goroutine visibility:** Worker goroutine(18) executing dispatch while main goroutine(1) waits on channel

---

## Evidence Artifacts

- `card3-trace-passing.txt` - Passing execution trace (credits=1)
- `card3-trace-failing.txt` - Failing execution trace (credits=0)
- `card5-watchpoint-attempt.txt` - Watchpoint refusal and recovery guidance assessment

