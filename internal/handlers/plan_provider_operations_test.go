package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// refactorProvider answers the two questions the plan seam asks a language
// server: what a refactor would change, as byte ranges, and where a symbol is
// referenced.
type refactorProvider struct {
	stubProvider
	edit       map[string]any
	references []any
	// reportedCount overrides the reference count, the way a server does
	// when it found more than it is willing to list.
	reportedCount int
	asked         []string
}

func (p *refactorProvider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	p.asked = append(p.asked, request.Operation)
	switch request.Operation {
	case "huyang_workspace_edit":
		return provider.Result{Value: p.edit}, nil
	case "references":
		count := len(p.references)
		if p.reportedCount > count {
			count = p.reportedCount
		}
		return provider.Result{Value: map[string]any{"count": count, "locations": p.references}}, nil
	}
	return provider.Result{Value: map[string]any{}}, nil
}

const refactorSource = "package p\n\nfunc Balance() int { return 0 }\n\nfunc Report() int { return Balance() }\n"

func newRefactorFixture(t *testing.T, backend *refactorProvider) (*Handlers, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ledger.go"), []byte(refactorSource), 0o600); err != nil {
		t.Fatal(err)
	}
	backend.stubProvider = stubProvider{descriptor: provider.Descriptor{ID: "refactor", Backend: "test", Epoch: 1, Root: root}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	opened := handlers.Execute(context.Background(), "req_open", "workspace_open", map[string]any{"kind": "project", "root": root})
	if opened["outcome"] != "ok" {
		t.Fatalf("open = %#v", opened)
	}
	return handlers, string(opened["workspace"].(workspacecore.Identity).ID), root
}

func planOperations(t *testing.T, result map[string]any) []workspacecore.PlanOperation {
	t.Helper()
	if result["outcome"] != "ok" {
		t.Fatalf("plan action = %#v", result)
	}
	return result["data"].(map[string]any)["plan"].(workspacecore.PlanRecord).Operations
}

// A rename is staged as the exact ranges the server proposes, bound to the
// current revision, each naming the request it came from, so the plan can be
// previewed, verified and rolled back like any other edit.
func TestRenameSymbolIsStagedAsTheRangesTheServerProposes(t *testing.T) {
	// Both occurrences of Balance, given from the end of the file backwards
	// the way the kernel orders them.
	backend := &refactorProvider{edit: map[string]any{"changes": []any{map[string]any{
		"file": "ledger.go", "edits": []any{
			map[string]any{"byte_start": 70, "byte_end": 77, "new_text": "Total"},
			map[string]any{"byte_start": 15, "byte_end": 22, "new_text": "Total"},
		},
	}}}}
	handlers, workspaceID, _ := newRefactorFixture(t, backend)
	created := handlers.Execute(context.Background(), "req_plan", "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "create",
		"operations": []any{map[string]any{
			"op_id": "rename", "kind": "rename_symbol", "content": "Total",
			"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Balance"}},
		}},
	})
	operations := planOperations(t, created)
	if len(operations) != 2 {
		t.Fatalf("rename staged %d operations: %#v", len(operations), operations)
	}
	for index, operation := range operations {
		if operation.Kind != workspacecore.OperationReplaceRange || operation.Target.FileRange == nil {
			t.Fatalf("operation %d = %#v", index, operation)
		}
		if operation.DerivedFrom != "rename_symbol rename" || operation.Content != "Total" {
			t.Fatalf("operation %d lost its provenance: %#v", index, operation)
		}
	}
	// From the end of the file backwards, and chained, so no edit moves the
	// bytes the next one is bound to.
	if operations[0].Target.FileRange.ByteStart != 70 || operations[1].Target.FileRange.ByteStart != 15 {
		t.Fatalf("edits are not ordered from the end backwards: %#v", operations)
	}
	if len(operations[1].DependsOn) != 1 || operations[1].DependsOn[0] != operations[0].OpID {
		t.Fatalf("edits are not chained: %#v", operations[1])
	}
	// The preview is the renamed file, which is what makes the refactor
	// reviewable before anything is staged.
	plan := created["data"].(map[string]any)["plan"].(workspacecore.PlanRecord)
	previewed := handlers.Execute(context.Background(), "req_preview", "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "preview", "plan_id": plan.PlanID, "plan_revision": int(plan.PlanRevision),
	})
	if previewed["outcome"] != "ok" {
		t.Fatalf("preview = %#v", previewed)
	}
	preview := previewed["data"].(map[string]any)["plan"].(workspacecore.PlanRecord).Preview
	if preview == nil || len(preview.Conflicts) != 0 {
		t.Fatalf("preview = %#v", preview)
	}
}

// An inline the server does not offer is refused with what it does offer,
// rather than with a bare failure.
func TestInlineSymbolRefusesWithTheActionsTheServerOffers(t *testing.T) {
	backend := &refactorProvider{edit: map[string]any{
		"changes": []any{},
		"actions_offered": []any{
			map[string]any{"title": "Extract to function", "kind": "refactor.extract"},
			map[string]any{"title": "Organize imports", "kind": "source.organizeImports"},
		},
	}}
	handlers, workspaceID, _ := newRefactorFixture(t, backend)
	result := handlers.Execute(context.Background(), "req_plan", "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "create",
		"operations": []any{map[string]any{
			"op_id": "inline", "kind": "inline_symbol",
			"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Balance"}},
		}},
	})
	summary, _ := result["summary"].(string)
	if result["outcome"] == "ok" || !strings.Contains(summary, "Extract to function") {
		t.Fatalf("inline without a matching action = %#v", result)
	}
}

// A declaration is deleted only when the server reports no reference to it
// outside its own lines; a caller elsewhere refuses the deletion and is named.
func TestSafeDeleteSymbolRefusesWhileCallersRemain(t *testing.T) {
	backend := &refactorProvider{references: []any{
		map[string]any{"file": "ledger.go", "line": 3},
		map[string]any{"file": "ledger.go", "line": 5},
	}}
	handlers, workspaceID, _ := newRefactorFixture(t, backend)
	deletion := func() map[string]any {
		return handlers.Execute(context.Background(), "req_plan", "change_plan", map[string]any{
			"workspace_id": workspaceID, "action": "create",
			"operations": []any{map[string]any{
				"op_id": "drop", "kind": "safe_delete_symbol",
				"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Balance"}},
			}},
		})
	}
	refused := deletion()
	summary, _ := refused["summary"].(string)
	if refused["outcome"] == "ok" || !strings.Contains(summary, "ledger.go:5") {
		t.Fatalf("deletion with a caller left = %#v", refused)
	}
	// The declaration's own line is not a reason to keep it.
	backend.references = []any{map[string]any{"file": "ledger.go", "line": 3}}
	operations := planOperations(t, deletion())
	if len(operations) != 1 || operations[0].Kind != workspacecore.OperationDeleteSymbol {
		t.Fatalf("safe delete staged %#v", operations)
	}
	if operations[0].DerivedFrom != "safe_delete_symbol drop" || operations[0].Target.FileRange == nil {
		t.Fatalf("safe delete lost its provenance or its range: %#v", operations[0])
	}
}

// A reference list the server truncated cannot prove a deletion safe, so the
// deletion is refused rather than taken on trust.
func TestSafeDeleteSymbolRefusesATruncatedReferenceList(t *testing.T) {
	backend := &refactorProvider{
		references:    []any{map[string]any{"file": "ledger.go", "line": 3}},
		reportedCount: 40,
	}
	handlers, workspaceID, _ := newRefactorFixture(t, backend)
	result := handlers.Execute(context.Background(), "req_plan", "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "create",
		"operations": []any{map[string]any{
			"op_id": "drop", "kind": "safe_delete_symbol",
			"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Balance"}},
		}},
	})
	summary, _ := result["summary"].(string)
	if result["outcome"] == "ok" || !strings.Contains(summary, "40 references") {
		t.Fatalf("a truncated list must refuse: %#v", result)
	}
}
