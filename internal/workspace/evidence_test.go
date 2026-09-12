package workspace

import (
	"path/filepath"
	"testing"
	"time"
)

func diagnosticVersion(value int64) *int64 { return &value }

func evidenceWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	state := t.TempDir()
	workspace, err := Open(OpenOptions{Kind: KindProject, Root: root, ProviderEpoch: 1, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, state
}

func finding(message string) DiagnosticFinding {
	return DiagnosticFinding{Range: DiagnosticRange{StartLine: 1, StartCharacter: 2, EndLine: 1, EndCharacter: 5}, Severity: 1, Code: "E1", Source: "fake", Message: message}
}

func TestDiagnosticEvidenceNormalizesDeduplicatesAndRetainsProviderConflicts(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	version := int64(7)
	base := DiagnosticBatch{Kind: EvidencePush, ProviderID: "gopls#1", Producer: "gopls", ProducerVersion: "v1",
		Document: "main.go", DocumentRevision: "rev_a", DocumentVersion: &version, ExpectedVersion: &version,
		Complete: true, Selected: true, Findings: []DiagnosticFinding{finding(" undefined   name "), finding("undefined name")}}
	first, err := workspace.RecordDiagnosticEvidence(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Confidence != ConfidenceAuthoritative || len(first.New) != 1 {
		t.Fatalf("first report = %#v", first)
	}

	pull := base
	pull.Kind, pull.ResultID = EvidencePull, "pull-1"
	second, err := workspace.RecordDiagnosticEvidence(pull)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.New) != 0 || second.PreexistingCount != 1 {
		t.Fatalf("push/pull duplicate leaked: %#v", second)
	}

	conflict := base
	conflict.ProviderID, conflict.Producer = "staticcheck#1", "staticcheck"
	conflict.Findings = []DiagnosticFinding{finding("different finding")}
	third, err := workspace.RecordDiagnosticEvidence(conflict)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.New) != 1 || third.New[0].ProviderID != "staticcheck#1" {
		t.Fatalf("complementary finding collapsed: %#v", third)
	}
}

// An edit above an untouched warning moves it. The warning is the same one,
// so it is neither resolved nor announced again: an agent reads the delta of
// an edit as what that edit caused.
func TestDiagnosticFindingThatOnlyMovedIsNotAnnouncedAgain(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	version := int64(1)
	same := func(line int) DiagnosticFinding {
		return DiagnosticFinding{Range: DiagnosticRange{StartLine: line, StartCharacter: 2, EndLine: line, EndCharacter: 5},
			Severity: 2, Code: "unusedvariable", Source: "gopls", Message: "declared and not used: total"}
	}
	base := DiagnosticBatch{Kind: EvidencePush, ProviderID: "gopls#1", Producer: "gopls", ProducerVersion: "v1",
		Document: "main.go", DocumentRevision: "rev_a", DocumentVersion: &version, ExpectedVersion: &version,
		Complete: true, Selected: true, Findings: []DiagnosticFinding{same(40), same(120)}}
	first, err := workspace.RecordDiagnosticEvidence(base)
	if err != nil {
		t.Fatal(err)
	}
	// The same message twice in one file is two findings, not one.
	if len(first.New) != 2 {
		t.Fatalf("first report = %#v", first)
	}

	shifted := base
	shifted.DocumentRevision = "rev_b"
	shifted.Findings = []DiagnosticFinding{same(47), same(127)}
	second, err := workspace.RecordDiagnosticEvidence(shifted)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.New) != 0 || len(second.Resolved) != 0 || second.CurrentCount != 2 {
		t.Fatalf("a shifted finding flapped: new=%#v resolved=%#v", second.New, second.Resolved)
	}
	// The reported range is the current one, not the one first seen.
	if second.Current[0].Finding.Range.StartLine != 47 {
		t.Fatalf("stale range reported: %#v", second.Current[0].Finding.Range)
	}

	// One of the two being fixed resolves exactly one item.
	fixed := shifted
	fixed.DocumentRevision = "rev_c"
	fixed.Findings = []DiagnosticFinding{same(47)}
	third, err := workspace.RecordDiagnosticEvidence(fixed)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.New) != 0 || len(third.Resolved) != 1 || third.CurrentCount != 1 {
		t.Fatalf("fixing one of two = new %#v resolved %#v", third.New, third.Resolved)
	}
}

func TestDiagnosticEvidenceRejectsUnselectedProviders(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	_, err := workspace.RecordDiagnosticEvidence(DiagnosticBatch{
		Kind: EvidencePush, ProviderID: "undeclared#1", Producer: "undeclared",
		Document: "main.go", Complete: true, Selected: false,
	})
	if err == nil {
		t.Fatal("unselected provider evidence was accepted")
	}
}

func TestDiagnosticBarrierPermutationsNeverCallIncompleteEvidenceClean(t *testing.T) {
	cases := []struct {
		name  string
		batch DiagnosticBatch
		want  DiagnosticConfidence
	}{
		{"wrong push version", DiagnosticBatch{Kind: EvidencePush, DocumentVersion: diagnosticVersion(1), ExpectedVersion: diagnosticVersion(2), Complete: true}, ConfidenceProvisional},
		{"pull without result", DiagnosticBatch{Kind: EvidencePull, Complete: true}, ConfidenceProvisional},
		{"workspace incomplete", DiagnosticBatch{Kind: EvidenceWorkspace, ResultID: "w", Complete: false}, ConfidenceProvisional},
		{"progress veto", DiagnosticBatch{Kind: EvidencePull, ResultID: "p", Complete: true, ProgressPending: true}, ConfidenceProvisional},
		{"timeout", DiagnosticBatch{Kind: EvidencePull, ResultID: "p", Complete: true, TimedOut: true}, ConfidenceProvisional},
		{"project check distinct", DiagnosticBatch{Kind: EvidenceProjectCheck, Complete: true}, ConfidenceCorroborated},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			test.batch.ProviderID, test.batch.Producer, test.batch.Selected = "fake#1", "fake", true
			test.batch.Document = "main.go"
			workspace, _ := evidenceWorkspace(t)
			report, err := workspace.RecordDiagnosticEvidence(test.batch)
			if err != nil {
				t.Fatal(err)
			}
			if report.Confidence != test.want {
				t.Fatalf("confidence=%s want %s", report.Confidence, test.want)
			}
			if report.Confidence == ConfidenceAuthoritative {
				t.Fatal("incomplete evidence was called authoritative")
			}
		})
	}
}

func TestDiagnosticPushAcceptsFreshPublishWithOrderedChangeBarrier(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	report, err := workspace.RecordDiagnosticEvidence(DiagnosticBatch{
		Kind: EvidencePush, ProviderID: "gopls#1", Producer: "gopls", Document: "main.go",
		Complete: true, ChangeBarrier: true, Selected: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Confidence != ConfidenceAuthoritative {
		t.Fatalf("confidence=%s want %s", report.Confidence, ConfidenceAuthoritative)
	}
}

func TestDiagnosticRecordReportsCurrentObservationDespiteHistoricalUnavailableEvidence(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	_, err := workspace.RecordDiagnosticEvidence(DiagnosticBatch{
		Kind: EvidenceUnavailable, ProviderID: "nvim_lsp", Producer: "nvim_lsp", Document: "main.go",
		Reason: "lsp_not_configured", Selected: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := workspace.RecordDiagnosticEvidence(DiagnosticBatch{
		Kind: EvidencePush, ProviderID: "gopls#1", Producer: "gopls", Document: "main.go",
		Complete: true, ChangeBarrier: true, Selected: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Confidence != ConfidenceAuthoritative {
		t.Fatalf("current confidence poisoned by history: %s", report.Confidence)
	}
}

func TestUnavailableEvidenceUsesExplicitReasonForKnownAndUnknownKinds(t *testing.T) {
	for _, kind := range []DiagnosticEvidenceKind{EvidenceUnavailable, DiagnosticEvidenceKind("future_evidence")} {
		confidence, reasons := confidenceFor(DiagnosticBatch{Kind: kind, Selected: true, Reason: "lsp_not_configured"})
		if confidence != ConfidenceUnavailable {
			t.Fatalf("kind %q confidence=%s", kind, confidence)
		}
		if len(reasons) != 1 || reasons[0] != "lsp_not_configured" {
			t.Fatalf("kind %q reasons=%v", kind, reasons)
		}
	}
	_, reasons := confidenceFor(DiagnosticBatch{Kind: EvidenceUnavailable, Selected: true})
	if len(reasons) != 1 || reasons[0] != "unsupported_diagnostic_evidence" {
		t.Fatalf("empty reason fallback=%v", reasons)
	}
}

func TestDiagnosticInboxPersistsAcknowledgementAndEvidence(t *testing.T) {
	workspace, state := evidenceWorkspace(t)
	id := workspace.Identity().ID
	version := int64(3)
	report, err := workspace.RecordDiagnosticEvidence(DiagnosticBatch{Kind: EvidencePush, ProviderID: "lua_ls#1", Producer: "lua_ls",
		Document: "main.lua", DocumentVersion: &version, ExpectedVersion: &version, Complete: true, Selected: true, Findings: []DiagnosticFinding{finding("bad")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.DiagnosticNotices(10)) != 1 {
		t.Fatal("late notice missing")
	}
	if _, err := workspace.Evidence(report.EvidenceIDs[0]); err != nil {
		t.Fatal(err)
	}

	root := workspace.Identity().Root
	reopened, err := Open(OpenOptions{Kind: KindProject, Root: root, ProviderEpoch: 1, StateDir: state, Identity: id, StateSeq: workspace.Identity().StateSeq})
	if err != nil {
		t.Fatal(err)
	}
	updates, err := reopened.Diagnostics("")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates.Notices) != 1 {
		t.Fatalf("persisted notices=%#v", updates.Notices)
	}
	if _, err := reopened.Diagnostics(updates.Cursor); err != nil {
		t.Fatal(err)
	}
	if got := reopened.DiagnosticNotices(10); len(got) != 0 {
		t.Fatalf("acknowledged notices remain: %#v", got)
	}
}

func TestCulpritAttributionUsesEvidenceNotTiming(t *testing.T) {
	if got := rankCulprit([]CulpritCandidate{{TransactionID: "tx_a", ExactSandbox: true}}); got.Rank != "exact" {
		t.Fatalf("exact=%#v", got)
	}
	if got := rankCulprit([]CulpritCandidate{{TransactionID: "tx_a", PostimageVersionMatch: true}, {TransactionID: "tx_b", PostimageVersionMatch: true}}); got.Rank != "ambiguous" {
		t.Fatalf("ambiguous=%#v", got)
	}
	if got := rankCulprit([]CulpritCandidate{{TransactionID: "tx_a", ChangedSymbol: true}}); got.Rank != "likely" {
		t.Fatalf("likely=%#v", got)
	}
	if got := rankCulprit(nil); got.Rank != "unattributed" {
		t.Fatalf("none=%#v", got)
	}
}

func TestDiagnosticResolutionCreatesDurableNotice(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	now := time.Now().UTC()
	batch := DiagnosticBatch{Kind: EvidencePull, ProviderID: "pyright#1", Producer: "pyright", Document: "main.py", ResultID: "r1", Complete: true, Selected: true, ObservedAt: now, Findings: []DiagnosticFinding{finding("bad")}}
	if _, err := workspace.RecordDiagnosticEvidence(batch); err != nil {
		t.Fatal(err)
	}
	batch.ResultID = "r2"
	batch.Findings = nil
	batch.ObservedAt = now.Add(time.Second)
	report, err := workspace.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Resolved) != 1 {
		t.Fatalf("resolved=%#v", report.Resolved)
	}
}

func TestDocumentContentChangeMarksFindingsStaleNotCurrentOrResolved(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	path := filepath.Join(workspace.Identity().Root, "main.go")
	if _, err := workspace.Snapshot(path, ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	batch := DiagnosticBatch{Kind: EvidencePull, ProviderID: "gopls#1", Producer: "gopls", Document: path,
		DocumentRevision: "wsrev_238", ResultID: "r1", Complete: true, Selected: true, ObservedAt: now,
		Findings: []DiagnosticFinding{finding("undefined: OpenRequest")}}
	first, err := workspace.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if first.CurrentCount != 1 || first.StaleCount != 0 || first.New[0].Status != DiagnosticStatusCurrent {
		t.Fatalf("first report = %+v", first)
	}

	// The document is rebuilt with other content and the producer stays silent.
	writeFile(t, path, "package main\n\nfunc main() {}\n")
	changed, err := workspace.Refresh(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := workspace.Diagnostics("")
	if err != nil {
		t.Fatal(err)
	}
	if report.CurrentCount != 0 || report.StaleCount != 1 || len(report.Stale) != 1 || len(report.Resolved) != 0 {
		t.Fatalf("after content change = %+v", report)
	}
	stale := report.Stale[0]
	if stale.Status != DiagnosticStatusStale || stale.StaleReason != "document_changed" || stale.StaleRevision != string(changed.Revision) {
		t.Fatalf("stale item = %+v", stale)
	}
	var kinds []string
	for _, notice := range report.Notices {
		kinds = append(kinds, notice.Kind)
	}
	if len(kinds) != 2 || kinds[0] != "new" || kinds[1] != "stale" {
		t.Fatalf("notice kinds = %v", kinds)
	}

	// A touch without a content change does not disturb anything.
	if _, err := workspace.Refresh(path, ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	if again, err := workspace.Diagnostics(report.Cursor); err != nil || len(again.Notices) != 0 {
		t.Fatalf("metadata-only refresh produced notices: %+v, %v", again, err)
	}

	// The producer re-publishes the same finding for the new content: it is
	// current again and announced as new.
	batch.ResultID, batch.DocumentRevision, batch.ObservedAt = "r2", "wsrev_240", now.Add(time.Second)
	republished, err := workspace.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(republished.New) != 1 || republished.CurrentCount != 1 || republished.StaleCount != 0 {
		t.Fatalf("republished report = %+v", republished)
	}
	if item := republished.New[0]; item.DocumentRevision != "wsrev_240" || item.Status != DiagnosticStatusCurrent {
		t.Fatalf("republished item provenance = %+v", item)
	}

	// Another rebuild, then a complete observation without the finding
	// verifies that the stale item is gone: resolved, not silently dropped.
	writeFile(t, path, "package main\n\nfunc main() { _ = 1 }\n")
	if _, err := workspace.Refresh(path, ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	batch.ResultID, batch.Findings, batch.ObservedAt = "r3", nil, now.Add(2*time.Second)
	resolved, err := workspace.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Resolved) != 1 || resolved.Resolved[0].Status != DiagnosticStatusResolved || resolved.CurrentCount != 0 || resolved.StaleCount != 0 {
		t.Fatalf("resolved report = %+v", resolved)
	}
}

func TestDiagnosticNoticesAreDeliveredOnceAndPagedByCursor(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	now := time.Now().UTC()
	for index, message := range []string{"one", "two", "three"} {
		batch := DiagnosticBatch{Kind: EvidencePull, ProviderID: "gopls#1", Producer: "gopls", Document: "main.go",
			ResultID: message, Complete: true, Selected: true, ObservedAt: now.Add(time.Duration(index) * time.Second),
			Findings: []DiagnosticFinding{finding("one"), finding("two"), finding("three")}[:index+1]}
		if _, err := workspace.RecordDiagnosticEvidence(batch); err != nil {
			t.Fatal(err)
		}
	}
	first := workspace.DiagnosticNotices(2)
	second := workspace.DiagnosticNotices(2)
	third := workspace.DiagnosticNotices(2)
	if len(first) != 2 || len(second) != 1 || len(third) != 0 {
		t.Fatalf("response-carried notices repeated: %d, %d, %d", len(first), len(second), len(third))
	}
	if first[0].Cursor == second[0].Cursor || first[1].Cursor == second[0].Cursor {
		t.Fatalf("delivered notices overlap: %+v %+v", first, second)
	}

	page, err := workspace.DiagnosticNoticesSince("", 2)
	if err != nil || len(page.Notices) != 2 || !page.More || page.Cursor != page.Notices[1].Cursor {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	rest, err := workspace.DiagnosticNoticesSince(page.Cursor, 2)
	if err != nil || len(rest.Notices) != 1 || rest.More || rest.Notices[0].Cursor == page.Notices[1].Cursor {
		t.Fatalf("second page = %+v, %v", rest, err)
	}
	empty, err := workspace.DiagnosticNoticesSince(rest.Cursor, 2)
	if err != nil || len(empty.Notices) != 0 || empty.Cursor != rest.Cursor {
		t.Fatalf("empty page = %+v, %v", empty, err)
	}
	// Paging does not acknowledge: the query still sees the whole delta.
	report, err := workspace.Diagnostics("")
	if err != nil || len(report.Notices) != 3 {
		t.Fatalf("query after paging = %+v, %v", report, err)
	}
}

func TestDiagnosticLedgerPrunesConsumedNoticesResolvedItemsAndUnreferencedEvidence(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	now := time.Now().UTC()
	old := now.Add(-2 * diagnosticRetentionWindow)
	batch := DiagnosticBatch{Kind: EvidencePull, ProviderID: "gopls#1", Producer: "gopls", Document: "main.go",
		ResultID: "r1", Complete: true, Selected: true, ObservedAt: old, Findings: []DiagnosticFinding{finding("kept"), finding("gone")}}
	first, err := workspace.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	batch.ResultID, batch.ObservedAt, batch.Findings = "r2", old.Add(time.Second), []DiagnosticFinding{finding("kept")}
	second, err := workspace.RecordDiagnosticEvidence(batch)
	if err != nil || len(second.Resolved) != 1 {
		t.Fatalf("second report = %+v, %v", second, err)
	}
	// Acknowledge everything so the notices can be dropped.
	report, err := workspace.Diagnostics("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Diagnostics(report.Cursor); err != nil {
		t.Fatal(err)
	}
	store := workspace.diagnostics
	store.mu.Lock()
	notices := len(store.state.Notices)
	_, resolvedKept := store.state.Items[second.Resolved[0].ID]
	_, currentKept := store.state.Items[second.Current[0].ID]
	_, firstEvidence := store.state.Evidence[first.EvidenceIDs[0]]
	_, secondEvidence := store.state.Evidence[second.EvidenceIDs[0]]
	store.mu.Unlock()
	if notices != 0 {
		t.Fatalf("acknowledged notices retained: %d", notices)
	}
	if resolvedKept || !currentKept {
		t.Fatalf("resolved item kept=%v current item kept=%v", resolvedKept, currentKept)
	}
	// The first evidence is still referenced by the current item; the
	// dimension references the second.
	if !firstEvidence || !secondEvidence {
		t.Fatalf("referenced evidence pruned: first=%v second=%v", firstEvidence, secondEvidence)
	}

	// Unacknowledged notices are capped and the gap is disclosed.
	store.mu.Lock()
	for index := 0; index < maxDiagnosticNotices+10; index++ {
		store.addNotice("new", DiagnosticItem{ID: "diag_bulk", Document: "main.go"})
	}
	store.pruneLocked(now)
	retained := len(store.state.Notices)
	store.mu.Unlock()
	if retained != maxDiagnosticNotices {
		t.Fatalf("notice cap not enforced: %d", retained)
	}
	stale, err := workspace.Diagnostics(report.Cursor)
	if err != nil || !stale.NoticesTruncated {
		t.Fatalf("old cursor did not disclose truncation: %+v, %v", stale, err)
	}
	fresh, err := workspace.Diagnostics(stale.Cursor)
	if err != nil || fresh.NoticesTruncated {
		t.Fatalf("current cursor reported truncation: %+v, %v", fresh, err)
	}
}
