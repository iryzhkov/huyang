package workspace

import (
	"path/filepath"
	"testing"
)

func TestDiagnosticStaleRefreshPersistsButResolvedFindingRegresses(t *testing.T) {
	for _, producer := range []string{"gopls", "pyright", "bashls", "ts_ls"} {
		t.Run(producer, func(t *testing.T) {
			w, _ := evidenceWorkspace(t)
			path := filepath.Join(w.Identity().Root, "main.go")
			if _, err := w.Snapshot(path, ProviderLayer{}); err != nil {
				t.Fatal(err)
			}
			batch := DiagnosticBatch{Kind: EvidencePull, DocumentVersion: diagnosticVersion(1), ExpectedVersion: diagnosticVersion(1), ProviderID: producer + "#1", Producer: producer,
				Document: path, DocumentRevision: "before", ResultID: "r1", Complete: true, Selected: true,
				Findings: []DiagnosticFinding{finding("existing")}}
			record := func() DiagnosticReport {
				t.Helper()
				report, err := w.RecordDiagnosticEvidence(batch)
				if err != nil {
					t.Fatal(err)
				}
				return report
			}
			first := record()
			if len(first.New) != 1 {
				t.Fatalf("initial finding = %+v", first)
			}
			change := func(content string) {
				t.Helper()
				writeFile(t, path, content)
				if _, err := w.Refresh(path, ProviderLayer{}); err != nil {
					t.Fatal(err)
				}
			}

			change("package main\n\n// edit\n")
			batch.DocumentRevision, batch.ResultID = "after", "r2"
			batch.Findings = append(batch.Findings, finding("introduced"))
			refreshed := record()
			if len(refreshed.New) != 1 || refreshed.New[0].Finding.Message != "introduced" ||
				refreshed.PreexistingCount != 1 || refreshed.CurrentCount != 2 || refreshed.StaleCount != 0 {
				t.Fatalf("refresh called a persisting finding new: %+v", refreshed)
			}
			delta, err := w.Diagnostics(first.Cursor)
			if err != nil || len(delta.New) != 1 || len(delta.Resolved) != 0 ||
				len(delta.Notices) != 3 || delta.Notices[1].Kind != "persisting" {
				t.Fatalf("cursor delta = %+v, %v", delta, err)
			}

			// Absence is established before the same ID comes back.
			batch.Findings, batch.ResultID = nil, "r3"
			fixed := record()
			if len(fixed.Resolved) != 2 {
				t.Fatalf("resolved = %+v", fixed)
			}
			change("package main\n\n// regression\n")
			batch.Findings, batch.ResultID = []DiagnosticFinding{finding("existing")}, "r4"
			regression := record()
			if len(regression.New) != 1 || regression.New[0].ID != first.New[0].ID || regression.PreexistingCount != 0 {
				t.Fatalf("real regression was suppressed: %+v", regression)
			}
		})
	}
}

func TestDiagnosticPersistingNoticeSurvivesReopenAndAcknowledgement(t *testing.T) {
	w, state := evidenceWorkspace(t)
	path := filepath.Join(w.Identity().Root, "main.go")
	if _, err := w.Snapshot(path, ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	batch := DiagnosticBatch{Kind: EvidencePull, DocumentVersion: diagnosticVersion(1), ExpectedVersion: diagnosticVersion(1), ProviderID: "pyright#1", Producer: "pyright",
		Document: path, ResultID: "before", Complete: true, Selected: true,
		Findings: []DiagnosticFinding{finding("existing")}}
	first, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Diagnostics(first.Cursor); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "package main\n\n// edit\n")
	if _, err := w.Refresh(path, ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	batch.ResultID = "after"
	if _, err := w.RecordDiagnosticEvidence(batch); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(OpenOptions{Kind: KindProject, Root: w.Identity().Root, StateDir: state,
		Identity: w.Identity().ID, ProviderEpoch: 1, StateSeq: w.Identity().StateSeq})
	if err != nil {
		t.Fatal(err)
	}
	page, err := reopened.DiagnosticNoticesSince(first.Cursor, 10)
	if err != nil || len(page.Notices) != 2 || page.Notices[1].Kind != "persisting" {
		t.Fatalf("persisted notices = %+v, %v", page, err)
	}
	report, err := reopened.Diagnostics(first.Cursor)
	if err != nil || len(report.New) != 0 || len(report.Resolved) != 0 || report.PreexistingCount != 1 {
		t.Fatalf("reopened delta = %+v, %v", report, err)
	}
	// Even after the notice is acknowledged, diagnostics can retrieve the body.
	report, err = reopened.Diagnostics(report.Cursor)
	if err != nil || len(report.Current) != 1 || report.Current[0].Finding.Message != "existing" {
		t.Fatalf("current diagnostics after acknowledgement = %+v, %v", report, err)
	}
}
