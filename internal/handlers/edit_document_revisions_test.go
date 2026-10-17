package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Echo the provider wire attribution, rather than reconstructing it from disk.
type revisionEchoProvider struct {
	stubProvider
	mu        sync.Mutex
	revisions map[string]string
	late      []workspacecore.DiagnosticBatch
}

func (p *revisionEchoProvider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch request.Operation {
	case "huyang_diagnostic_evidence":
		if baseline, _ := request.Arguments["baseline"].(bool); baseline {
			return provider.Result{Value: map[string]any{}}, nil
		}
		p.revisions, _ = request.Arguments["document_revisions"].(map[string]string)
		tx, _ := request.Arguments["transaction_id"].(string)
		version := int64(3)
		var batches []workspacecore.DiagnosticBatch
		for _, path := range request.Arguments["files"].([]string) {
			batch := workspacecore.DiagnosticBatch{
				Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test",
				Document: path, DocumentRevision: p.revisions[path], TransactionID: tx,
				DocumentVersion: &version, ExpectedVersion: &version,
				Complete: true, Selected: true, Dimension: "edited_documents",
				Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Message: "immediate " + path}},
			}
			batches = append(batches, batch)
			batch.Findings = []workspacecore.DiagnosticFinding{{Severity: 1, Message: "late " + path}}
			batch.Candidates = []workspacecore.CulpritCandidate{{TransactionID: tx, PostimageVersionMatch: true}}
			p.late = append(p.late, batch)
		}
		return provider.Result{Value: map[string]any{"batches": batches}}, nil
	case "huyang_late_evidence":
		batches := p.late
		p.late = nil
		return provider.Result{Value: map[string]any{"batches": batches}}, nil
	}
	return provider.Result{Value: map[string]any{}}, nil
}

func TestEditLateEvidenceUsesReceiptDocumentRevisions(t *testing.T) {
	for _, mode := range []string{"single", "multi_file", "repeated_file"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			paths := []string{"a.go"}
			if mode == "multi_file" {
				paths = append(paths, "nested/b.go")
			}
			for _, path := range paths {
				full := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte("package fixture\n\nconst Value=1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			backend := &revisionEchoProvider{stubProvider: stubProvider{
				descriptor: provider.Descriptor{ID: "test", Backend: "test", Epoch: 1, Root: root},
			}}
			h := newTestHandlers(t, fixedFactory{backend: backend})
			opened := h.Execute(context.Background(), "open", "workspace_open", map[string]any{"root": root})
			if opened["outcome"] != "ok" {
				t.Fatalf("open: %#v", opened)
			}
			id := opened["workspace"].(workspacecore.Identity).ID
			operations := []any{}
			for _, path := range paths {
				operations = append(operations, map[string]any{
					"kind": "replace_literal", "path": path, "old": "Value=1", "new": "Value=2",
				})
			}
			if mode == "repeated_file" {
				operations = append(operations, map[string]any{
					"kind": "replace_literal", "path": paths[0], "old": "Value=2", "new": "Value=3",
				})
			}
			args := map[string]any{"workspace_id": string(id), "idempotency_key": mode, "format": true}
			if mode == "single" {
				args["operation"] = operations[0]
			} else {
				args["operations"] = operations
			}
			result := h.Execute(context.Background(), "edit", "edit_apply", args)
			if result["outcome"] != "ok" {
				t.Fatalf("edit: %#v", result)
			}
			data := result["data"].(map[string]any)
			revisions := map[string]workspacecore.RevisionID{}
			if len(paths) == 1 {
				revisions[paths[0]] = data["document_revision"].(workspacecore.RevisionID)
			} else {
				revisions = data["document_revisions"].(map[string]workspacecore.RevisionID)
			}
			workspace := h.registry.Lookup(id)
			immediate, err := workspace.Diagnostics("")
			if err != nil || len(immediate.Current) != len(paths) {
				t.Fatalf("immediate report: %+v, %v", immediate, err)
			}
			for _, item := range immediate.Current {
				path, err := filepath.Rel(root, item.Document)
				if err != nil || item.DocumentRevision != string(revisions[filepath.ToSlash(path)]) {
					t.Fatalf("immediate revision: %+v, receipt=%v", item, revisions)
				}
			}
			backend.mu.Lock()
			for path, revision := range revisions {
				if !strings.HasPrefix(string(revision), "docrev_") ||
					backend.revisions[filepath.Join(root, path)] != string(revision) {
					t.Errorf("provider revision for %s: %v, receipt=%v", path, backend.revisions, revisions)
				}
			}
			backend.mu.Unlock()
			h.Execute(context.Background(), "diagnostics", "diagnostics", map[string]any{"workspace_id": string(id), "full": true})
			late, err := workspace.Diagnostics(immediate.Cursor)
			if err != nil || len(late.Current) != len(paths) || len(late.New) != len(paths) {
				t.Fatalf("late report: %+v, %v", late, err)
			}
			for _, item := range late.Current {
				path, err := filepath.Rel(root, item.Document)
				if err != nil || item.DocumentRevision != string(revisions[filepath.ToSlash(path)]) ||
					item.TransactionID != "edit_edit" || !strings.HasPrefix(item.Finding.Message, "late ") {
					t.Fatalf("late attribution: %+v, receipt=%v", item, revisions)
				}
			}
		})
	}
}
