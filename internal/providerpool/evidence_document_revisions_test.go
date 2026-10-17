package providerpool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestAppliedDiagnosticRevisionIsNotReplacedByLaterSnapshot(t *testing.T) {
	root, providerRoot := t.TempDir(), t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	applied, err := w.Snapshot(path, workspacecore.ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Observe the external write through the workspace's refresh API; Snapshot
	// deliberately reuses the cached document until an explicit refresh.
	current, err := w.Refresh(path, workspacecore.ProviderLayer{})
	if err != nil || current.Revision == applied.Revision {
		t.Fatalf("fixture did not advance document: %+v, %v", current, err)
	}
	version := int64(3)
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{
		ID: "test", Root: providerRoot, Epoch: 1,
	}}, batches: []workspacecore.DiagnosticBatch{{
		Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test",
		Document: filepath.Join(providerRoot, "a.go"), Complete: true,
		DocumentVersion: &version, ExpectedVersion: &version,
		Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Message: "applied postimage"}},
	}}}
	report, err := RecordAppliedDiagnostics(context.Background(), w, p,
		[]workspacecore.PlanStageFile{{Path: "a.go", AfterExists: true}},
		map[string]workspacecore.RevisionID{"a.go": applied.Revision}, "wsrev_2", "edit_original")
	if err != nil || len(report.New) != 1 {
		t.Fatalf("report: %+v, %v", report, err)
	}
	item := report.New[0]
	if item.Document != path || item.DocumentRevision != string(applied.Revision) || item.TransactionID != "edit_original" {
		t.Fatalf("postimage replaced: %+v", item)
	}
	detail, err := w.Evidence(report.EvidenceIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	var batch workspacecore.DiagnosticBatch
	if err := json.Unmarshal(detail.Payload, &batch); err != nil {
		t.Fatal(err)
	}
	if batch.DocumentRevision != string(applied.Revision) {
		t.Fatalf("stored proof rebound to current document: %+v", batch)
	}
}

func TestAppliedDiagnosticMissingRevisionDoesNotBorrowWorkspaceRevision(t *testing.T) {
	root := t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Root: root, Epoch: 1}},
		batches: []workspacecore.DiagnosticBatch{{
			Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test",
			Document: filepath.Join(root, "missing.go"), DocumentRevision: "wsrev_wrong",
			Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Message: "unknown revision"}},
		}}}
	report, err := RecordAppliedDiagnostics(context.Background(), w, p,
		[]workspacecore.PlanStageFile{{Path: "missing.go", AfterExists: true}}, nil, "wsrev_2", "edit_missing")
	if err != nil || len(report.New) != 1 || report.New[0].DocumentRevision != "" {
		t.Fatalf("missing revision guessed: %+v, %v", report, err)
	}
}

func TestStagedDiagnosticRevisionRemainsPreparationScoped(t *testing.T) {
	root, sandbox := t.TempDir(), t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Root: sandbox, Epoch: 1}}}
	files := []workspacecore.PlanStageFile{}
	for _, name := range []string{"a.go", "b.go"} {
		files = append(files, workspacecore.PlanStageFile{Path: name, AfterExists: true})
		p.batches = append(p.batches, workspacecore.DiagnosticBatch{
			Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test",
			Document: filepath.Join(sandbox, name),
			Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Message: name}},
		})
	}
	report, err := RecordStagedDiagnostics(context.Background(), w, p, files, "preparation_scope", "plan")
	if err != nil || len(report.New) != 2 {
		t.Fatalf("staged report: %+v, %v", report, err)
	}
	for _, item := range report.New {
		if item.DocumentRevision != "preparation_scope" || item.TransactionID != "plan" {
			t.Fatalf("staged attribution changed: %+v", item)
		}
	}
	current, err := w.Diagnostics("")
	if err != nil || len(current.Current) != 0 {
		t.Fatalf("staged diagnostics entered canonical ledger: %+v, %v", current, err)
	}
}
