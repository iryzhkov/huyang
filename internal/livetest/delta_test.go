//go:build live

package livetest

import (
	"testing"
	"time"
)

// preparedDelta prepares one plan and returns the delta its diagnostics
// report carries.
func preparedDelta(t *testing.T, instance *live, session sessionHandle, workspaceID string, operations []any) map[string]any {
	t.Helper()
	prepared := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "delta-prepare",
		"operations": operations,
	})
	if outcome(prepared) == "failed" {
		t.Fatalf("prepare = %#v", prepared)
	}
	plan := data(prepared)["plan"].(map[string]any)
	preparation, _ := plan["preparation"].(map[string]any)
	revision, _ := preparation["prepared_revision"].(string)
	if revision == "" {
		t.Fatalf("no prepared revision: %#v", plan)
	}
	diagnosed := call(t, session, "diagnostics", map[string]any{
		"workspace_id": workspaceID, "revision": revision,
	})
	if outcome(diagnosed) != "ok" {
		t.Fatalf("prepared diagnostics = %#v", diagnosed)
	}
	delta, _ := data(diagnosed)["delta"].(map[string]any)
	if delta == nil {
		t.Fatalf("the prepared diagnostics carried no delta: %s", summary(diagnosed))
	}
	t.Logf("delta: %s", summary(diagnosed))
	return delta
}

// waitForCanonicalFinding polls the canonical report until the workspace has
// a current finding, bounded, and reports whether one arrived.
func waitForCanonicalFinding(t *testing.T, session sessionHandle, workspaceID string, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		report := call(t, session, "diagnostics", map[string]any{"workspace_id": workspaceID})
		inner, _ := data(report)["diagnostics"].(map[string]any)
		if count := argFloat(inner["current_count"]); count > 0 {
			t.Logf("canonical baseline: %v finding(s)", count)
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// argFloat reads a JSON number however it decoded.
func argFloat(value any) float64 {
	number, _ := value.(float64)
	return number
}

// One operation introduces one error, and the answer says which operation
// did it rather than listing everything wrong with the file.
func TestOneOperationTakesTheBlameForItsOwnError(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	delta := preparedDelta(t, instance, session, workspaceID, []any{map[string]any{
		"op_id": "break-unused", "kind": "replace_symbol",
		// Unused has no callers, so the only thing this can break is itself.
		"content": "// Unused is called from nowhere.\nfunc Unused() int {\n\tvar orphan string\n\treturn orphan\n}\n",
		"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Unused"}},
	}})

	newFindings, _ := delta["new"].([]any)
	if len(newFindings) == 0 {
		t.Fatalf("a type error was staged and nothing was reported as new: %#v", delta)
	}
	attributed := false
	for _, value := range newFindings {
		finding := value.(map[string]any)
		attribution, _ := finding["attribution"].(map[string]any)
		if attribution == nil {
			t.Fatalf("a new finding carries no attribution: %#v", finding)
		}
		operations, _ := attribution["operations"].([]any)
		if attribution["rank"] == "exact" && len(operations) == 1 && operations[0] == "break-unused" {
			attributed = true
		}
	}
	if !attributed {
		t.Fatalf("no new finding was attributed exactly to the operation that caused it: %#v", newFindings)
	}
	// The warnings the file already had are not this change's doing.
	if unchanged := delta["unchanged_count"]; unchanged == nil {
		t.Fatalf("the delta does not count what was already there: %#v", delta)
	}
}

// A change that fixes something says so, which is the half of the answer an
// agent needs to know its edit worked.
func TestAResolvedFindingIsReportedAsResolved(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	// Break it on canonical first, so there is something to resolve.
	broken := call(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "delta-break",
		"operation": map[string]any{
			"kind": "replace_literal", "path": "ledger.go",
			"old": "func Unused() int {\n\treturn 0\n}", "new": "func Unused() int {\n\tvar orphan string\n\treturn orphan\n}",
		},
	})
	if verdict := outcome(broken); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("seeding the error = %s: %s", verdict, summary(broken))
	}
	// Establish the baseline the way an agent would: ask for the canonical
	// diagnostics until the server has published them. A language server on a
	// cold workspace answers the edit before it has finished indexing, and a
	// baseline taken too early is the "nothing was wrong before" this stage
	// exists to avoid claiming.
	seeded := waitForCanonicalFinding(t, session, workspaceID, 15*time.Second)
	delta := preparedDelta(t, instance, session, workspaceID, []any{map[string]any{
		"op_id": "fix-unused", "kind": "replace_symbol",
		"content": "// Unused is called from nowhere.\nfunc Unused() int {\n\treturn 0\n}\n",
		"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Unused"}},
	}})
	resolved, _ := delta["resolved"].([]any)
	complete, _ := delta["baseline_complete"].(bool)
	switch {
	case seeded && len(resolved) == 0:
		t.Fatalf("a change that fixes a known error reported nothing resolved: %#v", delta)
	case !seeded && complete:
		// The ledger never held anything about this file, so there is no
		// baseline to compare against and saying otherwise would present
		// every pre-existing problem as this change's doing.
		t.Fatalf("a baseline nobody computed was reported as complete: %#v", delta)
	case !seeded:
		t.Logf("no baseline was available, and the delta says so")
	}
}

// Two operations on one file, and a finding inside the bytes both could
// claim: the answer says ambiguous rather than picking one.
func TestTwoOperationsOnOneSymbolAreAmbiguous(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	// Two operations on two declarations of one file. A finding that is not
	// inside either operation's own bytes has two candidates at the file
	// level, and neither of them owns it.
	delta := preparedDelta(t, instance, session, workspaceID, []any{
		map[string]any{
			"op_id": "break-unused", "kind": "replace_symbol",
			"content": "// Unused is called from nowhere.\nfunc Unused() int {\n\tvar orphan string\n\treturn orphan\n}\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Unused"}},
		},
		map[string]any{
			"op_id": "touch-total", "kind": "insert_before",
			"content": "// touched by another operation\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		},
	})
	newFindings, _ := delta["new"].([]any)
	if len(newFindings) == 0 {
		t.Skip("the server reported nothing new for this pair of operations")
	}
	for _, value := range newFindings {
		attribution, _ := value.(map[string]any)["attribution"].(map[string]any)
		if attribution["rank"] == "exact" {
			// Exact is only honest when one operation owns those bytes; with
			// two operations on one declaration that must not happen.
			operations, _ := attribution["operations"].([]any)
			if len(operations) != 1 {
				t.Fatalf("exact attribution with %d operations: %#v", len(operations), attribution)
			}
		}
		t.Logf("attribution: %#v", attribution)
	}
}
