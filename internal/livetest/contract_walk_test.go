//go:build live

package livetest

import (
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// Every value the catalog advertises, called for real.
//
// rename_symbol, move_symbols and apply_code_action were advertised in the
// schema and in the contract for weeks with no implementation behind them:
// each fell into a default branch, so no plan containing one could preview.
// Tool-level tests did not notice, because change_plan itself was covered
// forty times over. The gap was one enum value deep, which is where this walk
// looks.
//
// The bar is not "succeeds": half of these cannot succeed against a fixture
// without a language server, and a refusal is a fine answer. The bar is that
// the server *has an answer* - a named outcome and a summary that says
// something about this call - rather than an unknown-kind error or a silent
// nothing.
func TestEveryAdvertisedOperationKindHasAnImplementation(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("full")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	// One target per kind, each the shape that kind is documented to take.
	declaration := map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Unused"}}
	cases := []struct {
		kind      string
		operation map[string]any
	}{
		{"replace_symbol", map[string]any{"target": declaration, "content": "func Unused() int {\n\treturn 1\n}\n"}},
		{"delete_symbol", map[string]any{"target": declaration}},
		{"insert_before", map[string]any{"target": declaration, "content": "// inserted\n"}},
		{"insert_after", map[string]any{"target": declaration, "content": "\n// inserted\n"}},
		{"create_file", map[string]any{"path": "created.go", "content": "package huyangfixture\n"}},
		{"move_file", map[string]any{"from": "report.go", "to": "moved.go"}},
		{"copy_file", map[string]any{"from": "report.go", "to": "copied.go"}},
		{"delete_file", map[string]any{"path": "report.go"}},
		{"rename_symbol", map[string]any{"target": declaration, "content": "Renamed"}},
		{"safe_delete_symbol", map[string]any{"target": declaration}},
		{"inline_symbol", map[string]any{"target": declaration}},
		{"apply_code_action", map[string]any{"target": declaration, "content": "Browse documentation"}},
		{"replace_matches", map[string]any{"target": map[string]any{"handle": "set_absent"}, "content": "x"}},
		{"replace_range", map[string]any{"target": map[string]any{"handle": "rng_absent"}, "content": "x"}},
	}
	advertised := advertisedOperationKinds(t)
	for _, probe := range cases {
		delete(advertised, probe.kind)
		operation := map[string]any{"op_id": "probe", "kind": probe.kind}
		for key, value := range probe.operation {
			operation[key] = value
		}
		result := call(t, session, "change_plan", map[string]any{
			"workspace_id": workspaceID, "action": "create", "idempotency_key": "walk-" + probe.kind,
			"operations": []any{operation},
		})
		if outcome(result) == "" || summary(result) == "" {
			t.Fatalf("%s answered without an outcome: %#v", probe.kind, result)
		}
		// The one answer that means nobody implemented the kind.
		if strings.Contains(summary(result), "unknown operation kind") ||
			strings.Contains(summary(result), "requires provider or result-set validation") {
			t.Fatalf("%s is advertised but has no implementation: %s", probe.kind, summary(result))
		}
		// A plan that was created must also be able to say what it would do.
		if outcome(result) == "ok" {
			plan := data(result)["plan"].(map[string]any)
			previewed := call(t, session, "change_plan", map[string]any{
				"workspace_id": workspaceID, "action": "preview", "idempotency_key": "walk-preview-" + probe.kind,
				"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
			})
			if outcome(previewed) != "ok" {
				t.Fatalf("%s created a plan that cannot preview: %s", probe.kind, summary(previewed))
			}
		}
	}
	if len(advertised) > 0 {
		t.Fatalf("the catalog advertises operation kinds this walk does not exercise: %v", advertised)
	}
}

// advertisedOperationKinds reads the kinds out of the frozen catalog, so a
// kind added to the schema without a probe here fails this test rather than
// shipping unexercised.
func advertisedOperationKinds(t *testing.T) map[string]bool {
	t.Helper()
	for _, descriptor := range mcpapi.Catalog(mcpapi.ProfileEdit) {
		if descriptor.Name != "change_plan" {
			continue
		}
		properties := descriptor.InputSchema["properties"].(map[string]any)
		operations := properties["operations"].(map[string]any)
		items := operations["items"].(map[string]any)
		kindSchema := items["properties"].(map[string]any)["kind"].(map[string]any)
		kinds := map[string]bool{}
		for _, value := range kindSchema["enum"].([]any) {
			kinds[value.(string)] = true
		}
		return kinds
	}
	t.Fatal("the edit profile does not advertise change_plan")
	return nil
}

// The semantic relations, each asked of a real language server. A relation
// with no kernel operation behind it answers nothing at all, which is what
// this catches.
func TestEveryAdvertisedNavigationRelationAnswers(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("orient")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	for _, relation := range []string{
		"definition", "type_definition", "implementation", "references",
		"incoming_calls", "outgoing_calls", "hover",
	} {
		result := call(t, session, "navigate", map[string]any{
			"workspace_id": workspaceID, "relation": relation, "symbol": "Total",
		})
		if outcome(result) == "" || summary(result) == "" {
			t.Fatalf("%s answered without an outcome: %#v", relation, result)
		}
		if strings.Contains(summary(result), "unknown") {
			t.Fatalf("%s is advertised but unknown to the kernel: %s", relation, summary(result))
		}
	}
}
