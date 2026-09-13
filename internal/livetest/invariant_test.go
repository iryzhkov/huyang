//go:build live

package livetest

// Invariants across the socket.
//
// The unit tests decide the rules; these decide whether the rules are
// reachable from a client. Each one declares an invariant on a real plan,
// prepares it against real language servers or a real test run, and looks at
// what the service says a caller may do next.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// prepareWithInvariants prepares one plan that declares invariants and returns
// the prepare envelope, whatever its outcome: a refused plan is the answer
// several of these tests are about.
func prepareWithInvariants(t *testing.T, session sessionHandle, workspaceID, key string, operations, invariants []any) map[string]any {
	t.Helper()
	return call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": key,
		"operations": operations, "invariants": invariants,
	})
}

// preparedPlan is the plan record, its prepared revision and its invariants as
// the reply carries them.
func preparedPlan(t *testing.T, envelope map[string]any) (map[string]any, string, map[string]map[string]any) {
	t.Helper()
	plan, _ := data(envelope)["plan"].(map[string]any)
	if plan == nil {
		t.Fatalf("reply carries no plan: %#v", envelope)
	}
	preparation, _ := plan["preparation"].(map[string]any)
	revision, _ := preparation["prepared_revision"].(string)
	invariants := map[string]map[string]any{}
	for _, value := range anySlice(plan["invariants"]) {
		invariant, _ := value.(map[string]any)
		id, _ := invariant["id"].(string)
		invariants[id] = invariant
	}
	return plan, revision, invariants
}

func anySlice(value any) []any {
	slice, _ := value.([]any)
	return slice
}

// nextActions are the actions a reply offers, which is how a caller learns
// what a refusal leaves open.
func nextActions(envelope map[string]any) []string {
	var actions []string
	for _, value := range anySlice(envelope["next"]) {
		entry, _ := value.(map[string]any)
		if action, _ := entry["action"].(string); action != "" {
			actions = append(actions, action)
		}
	}
	return actions
}

func offers(actions []string, wanted string) bool {
	for _, action := range actions {
		if action == wanted {
			return true
		}
	}
	return false
}

// trust makes one root's configured commands runnable for this disposable
// installation only, by writing the trust file in its own config home.
func (l *live) trust(t *testing.T, root string) {
	t.Helper()
	directory := filepath.Join(l.homeDir, ".config", "huyang")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "[trust]\nroots = [\"" + root + "\"]\n"
	if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedKnownBaseline makes the workspace's diagnostic ledger hold evidence
// about ledger.go against the bytes that are there now: break it, wait for
// the finding, put it back, wait for the file to come clean. It reports
// whether that worked, because a language server that never answered is a
// fact about the machine rather than a reason to fail.
func seedKnownBaseline(t *testing.T, session sessionHandle, workspaceID string) bool {
	t.Helper()
	// A language server that has just opened a module answers the first edit
	// before it has published anything, and polling the ledger will not make
	// it publish: the ledger is a record, not a question. So the edit is made
	// again, under a different name, until a finding arrives or the attempts
	// run out.
	names := []string{"orphan", "stray", "spare", "loose"}
	broken := call(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "baseline-break",
		"operation": map[string]any{
			"kind": "replace_literal", "path": "ledger.go",
			"old": "func Unused() int {\n\treturn 0\n}",
			"new": "func Unused() int {\n\tvar " + names[0] + " string\n\treturn " + names[0] + "\n}",
		},
	})
	if verdict := outcome(broken); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("seeding the baseline = %s: %s", verdict, summary(broken))
	}
	seeded, last := recordedFinding(broken), names[0]
	for index := 1; index <= len(names) && !seeded; index++ {
		if waitForCanonicalFinding(t, session, workspaceID, 3*time.Second) {
			seeded = true
			break
		}
		if index == len(names) {
			break
		}
		nudged := call(t, session, "edit_apply", map[string]any{
			"workspace_id": workspaceID, "idempotency_key": "baseline-nudge-" + names[index],
			"operation": map[string]any{
				"kind": "replace_literal", "path": "ledger.go", "expected_count": 2,
				"old": last, "new": names[index],
			},
		})
		if verdict := outcome(nudged); verdict != "ok" && verdict != "provisional" {
			t.Fatalf("nudging the language server = %s: %s", verdict, summary(nudged))
		}
		last = names[index]
		seeded = recordedFinding(nudged)
	}
	if !seeded {
		return false
	}
	restored := call(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "baseline-restore",
		"operation": map[string]any{
			"kind": "replace_literal", "path": "ledger.go",
			"old": "func Unused() int {\n\tvar " + last + " string\n\treturn " + last + "\n}", "new": "func Unused() int {\n\treturn 0\n}",
		},
	})
	if verdict := outcome(restored); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("restoring the fixture = %s: %s", verdict, summary(restored))
	}
	// The baseline is only useful once the error is gone from it again:
	// otherwise the finding the plan stages matches one the ledger already
	// holds, and the comparison would call a new error unchanged.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		report := call(t, session, "diagnostics", map[string]any{"workspace_id": workspaceID})
		inner, _ := data(report)["diagnostics"].(map[string]any)
		if argFloat(inner["current_count"]) == 0 {
			t.Logf("canonical baseline: known and clean")
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// recordedFinding reports whether an edit's own reply says the workspace
// recorded a finding for it, which is the earliest and most reliable moment
// the ledger is known to have one.
func recordedFinding(envelope map[string]any) bool {
	delta, _ := data(envelope)["diagnostic_delta"].(map[string]any)
	return len(anySlice(delta["new"])) > 0
}

// A plan that breaks the code cannot prove it did not, and the refusal says
// which invariant and why. Nothing here is applicable afterwards.
func TestABrokenEditCannotProveItIntroducedNothing(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	// A baseline first. The comparison behind this invariant is only honest
	// when the workspace has looked at these files against the bytes that are
	// there now, so the fixture is broken and fixed on canonical until the
	// ledger holds evidence about it. Without that the answer is unknown,
	// which is correct but proves nothing about the violated path.
	seeded := seedKnownBaseline(t, session, workspaceID)

	prepared := prepareWithInvariants(t, session, workspaceID, "clean-prepare",
		[]any{map[string]any{
			"op_id": "break-unused", "kind": "replace_symbol",
			"content": "// Unused is called from nowhere.\nfunc Unused() int {\n\tvar orphan string\n\treturn orphan\n}\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Unused"}},
		}},
		[]any{map[string]any{"id": "clean", "kind": "no_new_diagnostics", "enforcement": "required"}})

	plan, revision, invariants := preparedPlan(t, prepared)
	clean := invariants["clean"]
	if clean == nil {
		t.Fatalf("the prepared plan carries no invariant: %#v", plan)
	}
	t.Logf("invariant: %v - %v", clean["status"], clean["detail"])
	if clean["status"] == "proven" {
		t.Fatalf("a staged type error proved that nothing new was introduced: %#v", clean)
	}
	if seeded && clean["status"] != "violated" {
		t.Fatalf("with a complete baseline a staged type error was not a violation: %#v", clean)
	}
	if !seeded {
		t.Logf("no canonical baseline was established, and the answer says so rather than guessing")
	}
	if clean["evaluated_revision"] != revision {
		t.Fatalf("the answer names %v, the preparation is %s", clean["evaluated_revision"], revision)
	}
	if plan["state"] != "PROVISIONAL" {
		t.Fatalf("state = %v, want PROVISIONAL for an unproven required invariant", plan["state"])
	}
	if actions := nextActions(prepared); offers(actions, "apply") {
		t.Fatalf("an unproven required invariant still offered apply: %v", actions)
	}

	// And the refusal holds when a caller ignores that and applies anyway,
	// including with the flag that accepts incomplete evidence.
	applied := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "apply", "idempotency_key": "invariant-apply",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
		"prepared_revision": revision, "accept_provisional": true,
	})
	if applied["code"] != "invariant_not_proven" {
		t.Fatalf("apply = %v/%v: %s", applied["outcome"], applied["code"], summary(applied))
	}
	if hashes := hashTree(t, root); hashes["ledger.go"] == "" {
		t.Fatal("the fixture lost its ledger")
	}
}

// What the test suite says settles the assertion, and it is the real suite:
// this one runs the project's own `go test` inside the sandbox, which needs a
// trusted root and no language server at all.
//
// Both halves matter. A change the tests pass proves the assertion, and a
// change they fail never reaches a prepared revision at all, because a
// verification stage that fails stops the preparation rather than producing
// something applicable.
func TestTheTestSuiteSettlesTestsPass(t *testing.T) {
	instance := start(t)
	root := fixture(t, "go")
	instance.trust(t, root)
	session := instance.connect("experimental")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "tests-pass",
		[]any{map[string]any{
			// store_test.go asserts Sum(Ledger{}) == Total(), so changing what
			// Total returns is a change the tests catch.
			"op_id": "change-total", "kind": "replace_symbol",
			// No trailing newline: the declaration's range ends at its closing
			// brace, and one more would leave the file gofmt would rewrite,
			// which this trusted root checks before anything else.
			"content": "// Total is the amount left in the ledger.\nfunc Total() int {\n\treturn 8\n}",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
		[]any{map[string]any{"id": "tests", "kind": "tests_pass"}})

	t.Logf("prepare: %s/%v %s", outcome(prepared), prepared["code"], summary(prepared))
	plan, _, invariants := preparedPlan(t, prepared)
	tests := invariants["tests"]
	if tests == nil {
		t.Fatalf("the prepared plan carries no invariant: %#v", plan)
	}
	t.Logf("invariant: %v - %v", tests["status"], tests["detail"])
	if tests["status"] != "proven" {
		t.Fatalf("a real passing test run did not prove the assertion: %#v", tests)
	}

	// The other half: a change the suite fails on. The pipeline stops at the
	// failing stage, so there is no prepared revision to apply and no answer
	// to weigh - the refusal itself is the answer, and it names the stage.
	refused := prepareWithInvariants(t, session, workspaceID, "tests-fail",
		[]any{map[string]any{
			"op_id": "break-sum", "kind": "replace_symbol",
			"content": "// Sum reads the interface rather than either implementation, so it is a\n// caller a file-level reader would miss.\nfunc Sum(store Store) int { return store.Balance() + 1 }",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "store.go", "name_path": "Sum"}},
		}},
		[]any{map[string]any{"id": "tests", "kind": "tests_pass"}})
	t.Logf("refused: %s/%v %s", outcome(refused), refused["code"], summary(refused))
	if outcome(refused) != "failed" {
		t.Fatalf("a change the tests fail on was prepared anyway: %s", summary(refused))
	}
	if !strings.Contains(summary(refused), "tests") {
		t.Fatalf("the refusal does not name the stage that failed: %s", summary(refused))
	}
	broken, _, _ := preparedPlan(t, refused)
	if broken["state"] != "FAILED" {
		t.Fatalf("state = %v after a failing test stage", broken["state"])
	}
}

// The same assertion, in a workspace where nothing can run it: unknown, and
// unknown does not pass.
func TestWithoutTrustedCommandsTestsPassIsUnknown(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "tests-unknown",
		[]any{map[string]any{
			"op_id": "change-total", "kind": "replace_symbol",
			"content": "// Total is the amount left in the ledger.\nfunc Total() int {\n\treturn 8\n}\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
		[]any{map[string]any{"id": "tests", "kind": "tests_pass"}})

	plan, revision, invariants := preparedPlan(t, prepared)
	tests := invariants["tests"]
	if tests == nil || tests["status"] != "unknown" {
		t.Fatalf("an untrusted root did not make the assertion unknown: %#v", tests)
	}
	applied := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "apply", "idempotency_key": "unknown-apply",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
		"prepared_revision": revision, "accept_provisional": true,
	})
	if applied["code"] != "invariant_not_proven" {
		t.Fatalf("an unknown required invariant did not block apply: %v/%v %s",
			applied["outcome"], applied["code"], summary(applied))
	}
}

// The same unknown, declared advisory: it is still said out loud, it still
// costs an explicit acceptance, and it does not stop the change.
func TestAnAdvisoryUnknownLeavesThePlanApplicable(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "advisory-prepare",
		[]any{map[string]any{
			"op_id": "change-total", "kind": "replace_symbol",
			"content": "// Total is the amount left in the ledger.\nfunc Total() int {\n\treturn 8\n}\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
		[]any{map[string]any{"id": "tests", "kind": "tests_pass", "enforcement": "advisory"}})

	plan, revision, invariants := preparedPlan(t, prepared)
	if invariants["tests"]["status"] != "unknown" {
		t.Fatalf("unexpected advisory answer: %#v", invariants["tests"])
	}
	if plan["state"] != "PROVISIONAL" {
		t.Fatalf("state = %v, want PROVISIONAL", plan["state"])
	}
	if actions := nextActions(prepared); !offers(actions, "apply") {
		t.Fatalf("an advisory gap left no way forward: %v", actions)
	}
	applied := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "apply", "idempotency_key": "advisory-apply",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
		"prepared_revision": revision, "accept_provisional": true,
	})
	if verdict := outcome(applied); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("apply = %s: %s", verdict, summary(applied))
	}
	content, err := os.ReadFile(filepath.Join(root, "ledger.go"))
	if err != nil || !strings.Contains(string(content), "return 8") {
		t.Fatalf("the accepted plan did not land: %v", err)
	}
}

// What the language server says about the staged bytes: a declaration nothing
// calls proves the assertion, one with callers names them, and a workspace
// with no server says it does not know.
func TestReferencesInTheStagedBytesSettleTheAssertion(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "references",
		[]any{map[string]any{
			"op_id": "touch-report", "kind": "insert_before",
			"content": "// touched by a plan that asserts something about other code\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Unused"}},
		}},
		[]any{
			map[string]any{"id": "orphan", "kind": "no_references", "enforcement": "advisory",
				"scope": map[string]any{"symbol": map[string]any{"path": "ledger.go", "name_path": "Unused"}}},
			map[string]any{"id": "called", "kind": "no_references", "enforcement": "advisory",
				"scope": map[string]any{"symbol": map[string]any{"path": "ledger.go", "name_path": "Total"}}},
			map[string]any{"id": "still-there", "kind": "symbol_exists", "enforcement": "advisory",
				"scope": map[string]any{"symbol": map[string]any{"path": "ledger.go", "name_path": "Unused"}}},
			map[string]any{"id": "never-was", "kind": "symbol_absent", "enforcement": "advisory",
				"scope": map[string]any{"symbol": map[string]any{"path": "ledger.go", "name_path": "Retired"}}},
		})

	_, _, invariants := preparedPlan(t, prepared)
	for id, invariant := range invariants {
		t.Logf("%s: %v - %v", id, invariant["status"], invariant["detail"])
	}
	if status := invariants["called"]["status"]; status == "proven" {
		t.Fatalf("a declaration with callers proved that nothing refers to it: %#v", invariants["called"])
	}
	if status := invariants["still-there"]["status"]; status != "proven" {
		t.Fatalf("a declaration that is in the staged file was not found: %#v", invariants["still-there"])
	}
	if status := invariants["never-was"]["status"]; status != "proven" {
		t.Fatalf("a declaration that is in no file was reported present: %#v", invariants["never-was"])
	}
	// The orphan is proven when a server answered and unknown when none did;
	// both are honest, and violated is not.
	if status := invariants["orphan"]["status"]; status == "violated" {
		t.Fatalf("a declaration nothing calls was reported as referenced: %#v", invariants["orphan"])
	}
}

// A symbol assertion about a language no native parser reads is unknown.
// The staged file is TypeScript, where the only evidence available without a
// parser is the first mention of the name outside a comment: that is a call
// site or a string as readily as a declaration, and "proven" would be a
// text scan wearing the word proof.
func TestASymbolAssertionWithoutAParserIsUnknown(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "typescript")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "typescript-symbol",
		[]any{map[string]any{
			"op_id": "add-note", "kind": "create_file", "path": "note.txt", "content": "touched\n",
		}},
		[]any{
			map[string]any{"id": "still-there", "kind": "symbol_exists", "enforcement": "advisory",
				"scope": map[string]any{"symbol": map[string]any{"path": "ledger.ts", "name_path": "total"}}},
			map[string]any{"id": "never-was", "kind": "symbol_absent", "enforcement": "advisory",
				"scope": map[string]any{"symbol": map[string]any{"path": "ledger.ts", "name_path": "retired"}}},
		})

	_, _, invariants := preparedPlan(t, prepared)
	for id, invariant := range invariants {
		if invariant["status"] != "unknown" {
			t.Fatalf("%s was settled by a text scan of a language no parser reads: %#v", id, invariant)
		}
		t.Logf("%s: %v", id, invariant["detail"])
	}
}

// Without a language server, a reference assertion is unknown rather than
// quietly true.
func TestWithoutALanguageServerNoReferencesIsUnknown(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := prepareWithInvariants(t, session, workspaceID, "no-server",
		[]any{map[string]any{
			"op_id": "touch-unused", "kind": "insert_before",
			"content": "// touched\n",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Unused"}},
		}},
		[]any{map[string]any{"id": "orphan", "kind": "no_references",
			"scope": map[string]any{"symbol": map[string]any{"path": "ledger.go", "name_path": "Unused"}}}})

	_, _, invariants := preparedPlan(t, prepared)
	orphan := invariants["orphan"]
	if orphan == nil || orphan["status"] != "unknown" {
		t.Fatalf("a machine with no language server settled a reference question: %#v", orphan)
	}
	t.Logf("orphan: %v", orphan["detail"])
}
