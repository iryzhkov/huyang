package providerpool

import (
	"context"
	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"os"
	"path/filepath"
	"testing"
)

func TestLateEvidenceCoalescesSupersededEmptyBeforeLedger(t *testing.T) {
	root := t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	v := int64(5)
	batch := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test", Document: filepath.Join(root, "a.go"), Complete: true, Selected: true, DocumentVersion: &v, ExpectedVersion: &v, Findings: []workspacecore.DiagnosticFinding{{Message: "still broken", Severity: 1}}}
	first, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	empty := batch
	empty.Findings = nil
	positive := batch
	positive.Complete = false
	positive.ProgressPending = true
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Root: root, Epoch: 1}}, batches: []workspacecore.DiagnosticBatch{empty, positive}}
	count, err := CollectLateEvidence(context.Background(), w, p)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("recorded %d batches", count)
	}
	report, err := w.Diagnostics(first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Resolved) != 0 || len(report.New) != 0 || report.CurrentCount != 1 {
		t.Fatalf("false transition: %+v", report)
	}
	for _, notice := range report.Notices {
		if notice.Kind == "resolved" || notice.Kind == "new" {
			t.Fatalf("transient notice: %+v", notice)
		}
	}
	older := batch
	old := int64(4)
	older.DocumentVersion = &old
	selected := coalesceLateDiagnosticBatches([]workspacecore.DiagnosticBatch{batch, older})
	if len(selected) != 1 || *selected[0].DocumentVersion != v {
		t.Fatal("older publication superseded current")
	}
}

func TestStagedMultiDocumentDiagnosticCounts(t *testing.T) {
	root := t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	v := int64(1)
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Root: root, Epoch: 1}}}
	for _, name := range []string{"a.go", "b.go"} {
		p.batches = append(p.batches, workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test", Document: filepath.Join(root, name), Complete: true, DocumentVersion: &v, ExpectedVersion: &v, Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Message: name}}})
	}
	files := []workspacecore.PlanStageFile{{Path: "a.go", AfterExists: true}, {Path: "b.go", AfterExists: true}}
	report, err := RecordStagedDiagnostics(context.Background(), w, p, files, "staged", "tx")
	if err != nil {
		t.Fatal(err)
	}
	if report.CurrentCount != 2 || report.PreexistingCount != 0 || len(report.New) != 2 {
		t.Fatalf("staged counts: %+v", report)
	}
	current, _ := w.Diagnostics("")
	if current.CurrentCount != 0 {
		t.Fatal("staged findings entered canonical ledger")
	}
}

func TestBaselineBoundToExactStageBeforeImage(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "external_change"}[external], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "a.go")
			if err := os.WriteFile(path, []byte("package a\n"), 0600); err != nil {
				t.Fatal(err)
			}
			w, err := workspacecore.New(workspacecore.KindProject, root, 1)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := w.Snapshot(path, workspacecore.ProviderLayer{})
			if err != nil {
				t.Fatal(err)
			}
			v := int64(1)
			b := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test", Document: path, Complete: true, DocumentVersion: &v, ExpectedVersion: &v, DocumentRevision: string(snapshot.Revision)}
			p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Root: root, Epoch: 1}}, batches: []workspacecore.DiagnosticBatch{b}}
			if err := RecordDiagnosticBaseline(context.Background(), w, p); err != nil {
				t.Fatal(err)
			}
			before := []byte("package a\n")
			if external {
				before = []byte("package a\n// external error\n")
				if err := os.WriteFile(path, before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			pre, err := w.Refresh(path, workspacecore.ProviderLayer{})
			if err != nil {
				t.Fatal(err)
			}
			after := append(append([]byte(nil), before...), []byte("// unrelated\n")...)
			if err := os.WriteFile(path, after, 0600); err != nil {
				t.Fatal(err)
			}
			b.Findings = []workspacecore.DiagnosticFinding{{Message: "error", Severity: 1}}
			p.batches = []workspacecore.DiagnosticBatch{b}
			report, err := RecordDiagnostics(context.Background(), w, p, []workspacecore.PlanStageFile{{Path: "a.go", Before: before, BeforeExists: true, BeforeDisk: pre.Disk, After: after, AfterExists: true}}, "wsrev", "tx")
			if err != nil {
				t.Fatal(err)
			}
			if len(report.New) != 1 || report.New[0].BaselineMissing != external {
				t.Fatalf("external=%v report=%+v", external, report)
			}
		})
	}
}

func TestDiagnosticBaselinePartialPublicationAndProgress(t *testing.T) {
	for _, progress := range []bool{false, true} {
		t.Run(map[bool]string{false: "seed_only", true: "progress_open"}[progress], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "a.go")
			if err := os.WriteFile(path, []byte("package a\n"), 0600); err != nil {
				t.Fatal(err)
			}
			w, err := workspacecore.New(workspacecore.KindProject, root, 1)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := w.Snapshot(path, workspacecore.ProviderLayer{})
			if err != nil {
				t.Fatal(err)
			}
			v := int64(1)
			first := workspacecore.DiagnosticFinding{Message: "first", Severity: 1}
			omitted := workspacecore.DiagnosticFinding{Message: "omitted", Severity: 1}
			batch := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush,
				ProviderID: "test#1", Producer: "test", Document: path, Complete: true, Selected: true,
				DocumentVersion: &v, ExpectedVersion: &v, DocumentRevision: "older_revision",
				Findings: []workspacecore.DiagnosticFinding{first, omitted}}
			initial, err := w.RecordDiagnosticEvidence(batch)
			if err != nil {
				t.Fatal(err)
			}
			batch.DocumentRevision = string(snapshot.Revision)
			batch.Findings = []workspacecore.DiagnosticFinding{first}
			batch.ProgressPending = progress
			p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Root: root, Epoch: 1}},
				batches: []workspacecore.DiagnosticBatch{batch}}
			if err := RecordDiagnosticBaseline(context.Background(), w, p); err != nil {
				t.Fatal(err)
			}
			current, err := w.Diagnostics(initial.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			if current.CurrentCount != 2 || len(current.New) != 0 || len(current.Resolved) != 0 {
				t.Fatalf("partial baseline changed existing findings: %+v", current)
			}
			batch.ProgressPending = false
			batch.PreEditDocumentRevision = string(snapshot.Revision)
			batch.DocumentRevision = "after_revision"
			batch.Findings = []workspacecore.DiagnosticFinding{first, omitted, {Message: "new finding", Severity: 1}}
			after, err := w.RecordDiagnosticEvidence(batch)
			if err != nil {
				t.Fatal(err)
			}
			if len(after.Resolved) != 0 || len(after.New) != 1 || after.New[0].Finding.Message != "new finding" ||
				after.New[0].BaselineMissing != progress {
				t.Fatalf("false return or insufficient baseline proof: %+v", after)
			}
			updates, err := w.Diagnostics(initial.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, notice := range updates.Notices {
				if notice.Kind == "resolved" || (notice.Kind == "new" && notice.ID != after.New[0].ID) {
					t.Fatalf("false resolved/new transition: %+v", notice)
				}
			}
		})
	}
}
