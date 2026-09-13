# Probe: honesty

Tests for places where Huyang claims knowledge it cannot have: reports absence of evidence as evidence of absence, or claims to have checked something it didn't.

Rule: every field in a reply that claims knowledge must distinguish "we did not check" from "we checked and found nothing".

## Finding 1: api_compatible proven without semantic provider (FAILS)

**Severity:** High. An invariant that claims to protect the public API is proven based on no actual API analysis.

**Call sequence:**
1. `start(t)` - Create a Huyang instance with NO language servers installed
2. `change_plan` with `prepare` action and operation `kind: replace_symbol` on `ledger.go`'s `Unused` function
3. Invariant: `kind: api_compatible`

**What happened:**
The invariant returned:
```json
{
  "id": "api",
  "kind": "api_compatible",
  "status": "proven",
  "coverage": {
    "semantic": "api_surface",
    "files_read": 1,
    "complete": true
  },
  "detail": "nothing exported by 1 affected file(s) was removed or changed",
  "enforcement": "required"
}
```

**What should have happened:**
The status should be `"unknown"` because:
- No language server was running (we used `start(t)` not `startWithLanguageServers(t)`)
- The coverage shows `"semantic": "api_surface"` but `files_read: 1` came from parsing Go syntax, not from semantic analysis
- Without a semantic provider, api_compatible cannot be proven

**Why this matters:**
An agent relying on `status: proven` for api_compatible will incorrectly assume the public API is safe when in fact it was never analyzed.

**Test file:** `internal/livetest/probe_honesty_test.go:TestProbeApiCompatibleProvenWithoutSemanticCoverage`
**Status:** FAILING

---

## Finding 2: symbol_exists proven without semantic provider (FAILS)

**Severity:** High. A symbol assertion is proven without semantic data.

**Call sequence:**
1. `start(t)` - Create a Huyang instance with NO language servers
2. `change_plan` with `prepare` action and operation `kind: replace_symbol` on `ledger.go`'s `Unused` function
3. Invariant: `kind: symbol_exists` for symbol `ledger.go/Unused`

**What happened:**
The invariant returned:
```json
{
  "id": "exists",
  "kind": "symbol_exists",
  "status": "proven",
  "coverage": {
    "semantic": "parser_sections",
    "files_read": 1,
    "complete": true
  },
  "detail": "Unused is declared in ledger.go at prep_...",
  "enforcement": "required"
}
```

**What should have happened:**
The status should be `"unknown"` because:
- No language server was running to resolve symbols semantically
- The parser_sections coverage shows we parsed Go syntax, not that we resolved symbols
- Symbol existence cannot be proven without semantic analysis

**Why this matters:**
An agent relying on `status: proven` for symbol_exists will trust that a symbol still exists in a changed file, when in fact the check was never performed.

**Test file:** `internal/livetest/probe_honesty_test.go:TestProbeSymbolExistsProvenWithoutSemanticData`
**Status:** FAILING

---

## Finding 3: diagnostic_delta absent when no language server present (UNCLEAR)

**Severity:** Low, but needs clarification.

**Call sequence:**
1. `start(t)` - Create a Huyang instance with NO language servers
2. `edit_apply` to touch a file in the opened workspace

**What happened:**
The edit response contained no `diagnostic_delta` field.

**What the test expected:**
The presence or absence of `diagnostic_delta` should distinguish "we didn't check" from "we checked and found clean". The test checked whether `baseline_complete` is false when the delta is present.

**Status:** No diagnostic_delta means we skip the check. This may be correct—the test logs "no diagnostic_delta in edit response" but doesn't fail. Need to verify whether the absence is intentional or a bug.

**Test file:** `internal/livetest/probe_honesty_test.go:TestProbeNoLanguageServerDiagnosticsAreNotClean`
**Status:** FAILING (but on the wrong assertion)

---

## Test results summary

Ran 11 probe tests:
- **2 FAIL with real defects found** (api_compatible, symbol_exists proven without checks)
- **1 FAIL on test setup** (diagnostic_delta not present—needs investigation)
- **8 PASS** (correct behavior detected)

Key findings:
- Invariants can be "proven" despite no semantic provider ever running
- The coverage field claims "api_surface" and "parser_sections" but the distinction between "parsed" and "analyzed" is lost
- Agents reading `status: proven` cannot tell whether the check actually ran
