package mcpapi

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestModernRegistryMatchesFrozenProfiles(t *testing.T) {
	if err := ValidateRegistry(); err != nil {
		t.Fatal(err)
	}
	fixtureBytes, err := os.ReadFile("../../docs/plans/fixtures/huyang-v1alpha1/contract-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Catalogs map[string][]string `json:"catalogs"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, profile := range ProfileOrder {
		got := CatalogNames(profile)
		want := fixture.Catalogs[string(profile)]
		if !slices.Equal(got, want) {
			t.Fatalf("%s catalog = %v, want %v", profile, got, want)
		}
		first, err := CatalogJSON(profile)
		if err != nil {
			t.Fatal(err)
		}
		second, err := CatalogJSON(profile)
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(second) {
			t.Fatalf("%s catalog generation is nondeterministic", profile)
		}
	}
}

func TestChangePlanRequestBoundsAdvertiseChunkedRecovery(t *testing.T) {
	schema := changePlanSchema(statefulProperties(), []string{"create_file"})
	properties := schema["properties"].(map[string]any)
	operations := properties["operations"].(map[string]any)
	if operations["maxItems"] != MaxPlanOperations || !strings.Contains(operations["description"].(string), "edit.mode=add") {
		t.Fatalf("operations bound is not actionable: %#v", operations)
	}
	operation := operations["items"].(map[string]any)
	content := operation["properties"].(map[string]any)["content"].(map[string]any)
	if content["maxLength"] != MaxPlanContentBytes {
		t.Fatalf("content bound = %#v", content)
	}
	tooMany := make([]any, MaxPlanOperations+1)
	for index := range tooMany {
		tooMany[index] = map[string]any{"op_id": fmt.Sprintf("op-%d", index), "kind": "create_file"}
	}
	err := ValidateToolArguments(schema, map[string]any{
		"workspace_id": "ws_test", "idempotency_key": "bounded", "action": "create", "operations": tooMany,
	})
	if err == nil || !strings.Contains(err.Error(), "edit.mode=add") {
		t.Fatalf("oversized plan error is not actionable: %v", err)
	}
	tooLarge := make(json.RawMessage, MaxToolArgumentBytes+1)
	err = ValidateToolArgumentSize(tooLarge)
	if err == nil || !strings.Contains(err.Error(), "safe transport limit") || !strings.Contains(err.Error(), "edit.mode=add") {
		t.Fatalf("oversized request error is not actionable: %v", err)
	}
}

// A numeric bound in the catalog is a promise about what the service accepts,
// so an argument outside it is refused here rather than passed to a handler
// that has no bound of its own: context_lines is the case that showed it,
// where a million lines of context were attached to every hit.
func TestDeclaredNumericBoundsAreRefused(t *testing.T) {
	schema := searchSchema()
	err := ValidateToolArguments(schema, map[string]any{
		"workspace_id": "ws_test", "query": "needle", "context_lines": float64(1000000),
	})
	if err == nil || !strings.Contains(err.Error(), "maximum 20") {
		t.Fatalf("context_lines above its declared maximum was accepted: %v", err)
	}
	if err := ValidateToolArguments(schema, map[string]any{
		"workspace_id": "ws_test", "query": "needle", "context_lines": float64(20),
	}); err != nil {
		t.Fatalf("context_lines at its declared maximum was refused: %v", err)
	}
	if err := ValidateToolArguments(schema, map[string]any{
		"workspace_id": "ws_test", "query": "needle", "limit": float64(0),
	}); err == nil || !strings.Contains(err.Error(), "minimum 1") {
		t.Fatalf("limit below its declared minimum was accepted: %v", err)
	}
}

// A property in the wrong place is the commonest first call against a tool:
// read takes target.path, and {path: ...} is the natural guess. The refusal
// names where the property belongs so the second attempt is the right one
// instead of another guess.
func TestAMisplacedPropertyIsToldWhereItBelongs(t *testing.T) {
	var read map[string]any
	for _, descriptor := range Tools {
		if descriptor.Name == "read" {
			read = descriptor.InputSchema
		}
	}
	if read == nil {
		t.Fatal("the catalog has no read tool")
	}
	err := ValidateToolArguments(read, map[string]any{
		"workspace_id": "ws_test", "path": "go.mod",
	})
	if err == nil {
		t.Fatal("read accepted a top-level path")
	}
	if !strings.Contains(err.Error(), "target.path") || !strings.Contains(err.Error(), "targets.path") {
		t.Fatalf("the refusal does not say where path belongs: %v", err)
	}
}

func TestEveryRegisteredToolDeclaresASchedulerClass(t *testing.T) {
	if err := ValidateRegistry(); err != nil {
		t.Fatal(err)
	}
	want := map[string]SchedulerClass{
		"workspace_open": ClassProviderRead, "workspace_inspect": ClassProviderRead,
		"execution_graph": ClassProviderRead, "path_explain": ClassProviderRead,
		"search": ClassPureRead, "symbol_find": ClassProviderRead, "navigate": ClassProviderRead,
		"read": ClassPureRead, "diagnostics": ClassPureRead, "code_actions": ClassProviderRead,
		"edit_apply": ClassCanonicalWrite, "change_plan": ClassSandboxWrite, "verify_run": ClassExternalJob,
		"revision_diff": ClassCanonicalWrite, "evidence_get": ClassPureRead,
		"language_server_status": ClassProviderRead, "language_server_setup": ClassCanonicalWrite,
		"debug_session": ClassProviderRead, "debug_breakpoints": ClassProviderRead,
		"debug_control": ClassProviderRead, "debug_inspect": ClassProviderRead,
	}
	for _, descriptor := range Tools {
		if !KnownClass(descriptor.Class) {
			t.Errorf("tool %s has no scheduler class", descriptor.Name)
		}
		if expected, ok := want[descriptor.Name]; !ok {
			t.Errorf("tool %s is not covered by the class table; add it here and on the descriptor", descriptor.Name)
		} else if descriptor.Class != expected {
			t.Errorf("tool %s class = %s, want %s", descriptor.Name, descriptor.Class, expected)
		}
		if ToolClass(descriptor.Name) != descriptor.Class {
			t.Errorf("ToolClass(%s) disagrees with the descriptor", descriptor.Name)
		}
	}
	for name := range want {
		if ToolClass(name) == ClassCanonicalWrite && want[name] != ClassCanonicalWrite {
			t.Errorf("tool %s is missing from the registry", name)
		}
	}
	if got := ClassForCall("change_plan", map[string]any{"action": "inspect"}); got != ClassCanonicalWrite {
		t.Errorf("change_plan inspect class = %s", got)
	}
	if got := ClassForCall("change_plan", map[string]any{"action": "prepare"}); got != ClassSandboxWrite {
		t.Errorf("change_plan prepare class = %s", got)
	}
	if got := ClassForCall("read", map[string]any{"target": map[string]any{"symbol_locator": map[string]any{}}}); got != ClassProviderRead {
		t.Errorf("symbol read class = %s", got)
	}
	if got := ClassForCall("read", map[string]any{"target": map[string]any{"path": "a"}}); got != ClassPureRead {
		t.Errorf("path read class = %s", got)
	}
	missing := ToolDescriptor{Name: "unregistered", InputSchema: schemaObject(map[string]any{})}
	previous := Tools
	Tools = append(append([]ToolDescriptor(nil), previous...), missing)
	defer func() { Tools = previous }()
	if err := ValidateRegistry(); err == nil || !strings.Contains(err.Error(), "scheduler class") {
		t.Errorf("registry accepted a tool without a class: %v", err)
	}
}

func TestTransactionStateEnumMatchesWorkspacePlanStates(t *testing.T) {
	schema := OutputEnvelopeSchema()
	transaction := schema["properties"].(map[string]any)["transaction"].(map[string]any)
	state := transaction["properties"].(map[string]any)["state"].(map[string]any)
	var advertised []string
	for _, value := range state["enum"].([]any) {
		advertised = append(advertised, value.(string))
	}
	sort.Strings(advertised)
	workspaceStates := []workspacecore.PlanState{
		workspacecore.PlanOpen, workspacecore.PlanPreviewed, workspacecore.PlanPreparing, workspacecore.PlanConflicted,
		workspacecore.PlanFailed, workspacecore.PlanProvisional, workspacecore.PlanReady, workspacecore.PlanCommitting,
		workspacecore.PlanCommitted, workspacecore.PlanRecoveryRequired, workspacecore.PlanRollingBack,
		workspacecore.PlanRolledBack, workspacecore.PlanExpired, workspacecore.PlanDiscarded,
	}
	var expected []string
	for _, value := range workspaceStates {
		expected = append(expected, string(value))
	}
	sort.Strings(expected)
	if strings.Join(advertised, ",") != strings.Join(expected, ",") {
		t.Fatalf("transaction.state enum = %v, want %v", advertised, expected)
	}
}
