package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func baselineBatch(document string) DiagnosticBatch {
	v := int64(1)
	return DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test",
		Document: document, DocumentRevision: "native_before", Complete: true, Selected: true,
		Baseline: true, DocumentVersion: &v, ExpectedVersion: &v,
		Findings: []DiagnosticFinding{finding("preexisting")}}
}

func TestBaselineFirstTouchPersistsAcrossReconnectWithoutIntroduction(t *testing.T) {
	w, state := evidenceWorkspace(t)
	batch := baselineBatch("main.go")
	batch.TransactionID = "upcoming"
	batch.Candidates = []CulpritCandidate{{TransactionID: "upcoming", ExactSandbox: true}}
	first, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.New) != 0 || first.PreexistingCount != 1 || len(first.Current) != 1 ||
		first.Current[0].TransactionID != "" || len(first.Current[0].Attribution.Candidates) != 0 {
		t.Fatalf("baseline blamed upcoming mutation: %+v", first)
	}
	page, err := w.DiagnosticNoticesSince("", 10)
	if err != nil || len(page.Notices) != 1 || page.Notices[0].Kind != "persisting" {
		t.Fatalf("first touch invisible: %+v, %v", page, err)
	}
	bodies := w.DiagnosticFindings([]string{page.Notices[0].ID})
	if bodies[page.Notices[0].ID].Message != "preexisting" {
		t.Fatal("persisting notice lost its actionable finding body")
	}
	reopened, err := Open(OpenOptions{Kind: KindProject, Root: w.Identity().Root, StateDir: state,
		Identity: w.Identity().ID, ProviderEpoch: 1, StateSeq: w.Identity().StateSeq})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reopened.Diagnostics("")
	if err != nil || len(report.New) != 0 || len(report.Notices) != 1 || len(report.Current) != 1 {
		t.Fatalf("reconnected first touch: %+v, %v", report, err)
	}
	if _, err := reopened.Diagnostics(report.Cursor); err != nil {
		t.Fatal(err)
	}
	batch.Baseline, batch.TransactionID, batch.Candidates = false, "next_edit", nil
	later, err := reopened.RecordDiagnosticEvidence(batch)
	if err != nil || len(later.New) != 0 || later.PreexistingCount != 1 {
		t.Fatalf("preexisting classified as introduced: %+v, %v", later, err)
	}
	page, err = reopened.DiagnosticNoticesSince(report.Cursor, 10)
	if err != nil || len(page.Notices) != 0 {
		t.Fatalf("current repeat was announced: %+v, %v", page, err)
	}
}

func TestBaselineEquivalentStoredProofStillProcessesRecurrenceAndStaleRefresh(t *testing.T) {
	store, err := newDiagnosticStore("", "")
	if err != nil {
		t.Fatal(err)
	}
	batch := baselineBatch("a.go")
	first, err := store.record(batch)
	if err != nil {
		t.Fatal(err)
	}
	// Verify resolution and give the resolved item historical mutation blame.
	empty := batch
	empty.Baseline, empty.Findings = false, nil
	fixed, err := store.record(empty)
	if err != nil || len(fixed.Resolved) != 1 {
		t.Fatalf("resolution: %+v, %v", fixed, err)
	}
	item := store.state.Items[first.Current[0].ID]
	item.TransactionID = "historical"
	item.Attribution = rankCulprit([]CulpritCandidate{{TransactionID: "historical", ExactSandbox: true}})
	store.state.Items[item.ID] = item
	batch.TransactionID = "future"
	batch.Candidates = []CulpritCandidate{{TransactionID: "future", ExactSandbox: true}}
	returned, err := store.record(batch)
	if err != nil || len(returned.New) != 0 || returned.EvidenceIDs[0] != first.EvidenceIDs[0] {
		t.Fatalf("equivalent proof not reused independently of lifecycle: %+v, %v", returned, err)
	}
	page, err := store.noticesSince(fixed.Cursor, 10)
	if err != nil || len(page.Notices) != 1 || page.Notices[0].Kind != "new" ||
		len(page.Notices[0].Attribution.Candidates) != 0 ||
		returned.Current[0].TransactionID != "" || len(returned.Current[0].Attribution.Candidates) != 0 {
		t.Fatalf("recurrence invisible or falsely attributed: %+v %+v, %v", returned, page, err)
	}
	if _, err := store.documentChanged([]string{"a.go"}, "different", time.Now()); err != nil {
		t.Fatal(err)
	}
	staleHead := fmt.Sprintf("diagcur_%d", store.noticeHead())
	refreshed, err := store.record(batch)
	if err != nil || len(refreshed.New) != 0 || refreshed.StaleCount != 0 {
		t.Fatalf("stale baseline refresh: %+v, %v", refreshed, err)
	}
	page, err = store.noticesSince(staleHead, 10)
	if err != nil || len(page.Notices) != 1 || page.Notices[0].Kind != "persisting" {
		t.Fatalf("stale refresh invisible: %+v, %v", page, err)
	}
	if _, err := store.record(batch); err != nil {
		t.Fatal(err)
	}
	page, err = store.noticesSince(page.Cursor, 10)
	if err != nil || len(page.Notices) != 0 {
		t.Fatalf("repeat baseline was not silent: %+v, %v", page, err)
	}
}

func TestBaselineProofKeyIncludesNativeRevisionPlacementMultiplicityAndProducer(t *testing.T) {
	base := baselineBatch("a.go")
	second := finding("preexisting")
	second.Range.StartLine, second.Range.EndLine = 8, 8
	base.Findings = append(base.Findings, second)
	key := baselineProofKey(base, ConfidenceAuthoritative)
	reordered := base
	reordered.Findings = []DiagnosticFinding{base.Findings[1], base.Findings[0]}
	reordered.ObservedAt, reordered.StateSeq = time.Now(), 99
	reordered.TransactionID = "future"
	if got := baselineProofKey(reordered, ConfidenceAuthoritative); got != key {
		t.Fatal("equivalent proof was keyed by order or observation bookkeeping")
	}
	variants := []DiagnosticBatch{base, base, base, base, base, base, base, base, base}
	variants[0].DocumentRevision = "other_native"
	variants[1].Producer = "other_producer"
	variants[2].ProviderID = "other_provider"
	variants[3].Document = "b.go"
	variants[4].Dimension = "other_dimension"
	variants[5].Findings = []DiagnosticFinding{base.Findings[0]}
	variants[6].Findings = []DiagnosticFinding{base.Findings[0], base.Findings[0]}
	variants[7].ProducerVersion = "v2"
	variants[8].DocumentVersion = diagnosticVersion(2)
	for i, changed := range variants {
		if got := baselineProofKey(changed, ConfidenceAuthoritative); got == key {
			t.Fatalf("different proof %d deduplicated", i)
		}
	}
	base.Staged = true
	if baselineProofKey(base, ConfidenceAuthoritative) != "" {
		t.Fatal("staged proof entered canonical baseline index")
	}
	base.Staged = false
	if baselineProofKey(base, ConfidenceProvisional) != "" {
		t.Fatal("incomplete proof entered authoritative baseline index")
	}
}

func TestBaselineMultiDocumentRepeatedStorageAndPersistenceBounded(t *testing.T) {
	w, _ := evidenceWorkspace(t)
	writes := 0
	w.diagnostics.writeFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		return atomicWriteFile(path, data, mode)
	}
	var batches []DiagnosticBatch
	for i := 0; i < 20; i++ {
		batches = append(batches, baselineBatch(fmt.Sprintf("loaded_%d.go", i)))
	}
	first, err := w.RecordDiagnosticEvidenceBatches(batches)
	if err != nil || writes != 1 || len(first) != 20 {
		t.Fatalf("multi-document persistence: writes=%d reports=%d err=%v", writes, len(first), err)
	}
	initial, _ := json.Marshal(w.diagnostics.state)
	// Measure storage over 10,000 observations without 500 disk fsyncs. The
	// write seam still counts each persistence and checks its complete payload.
	w.diagnostics.writeFile = func(_ string, data []byte, _ os.FileMode) error {
		writes++
		var persisted diagnosticState
		if err := json.Unmarshal(data, &persisted); err != nil {
			return err
		}
		if len(persisted.Items) != 20 {
			return errors.New("partial bulk ledger published")
		}
		return nil
	}
	for edit := 0; edit < 500; edit++ {
		for i := range batches {
			batches[i].ObservedAt = time.Now().UTC()
			batches[i].StateSeq = uint64(edit + 2)
		}
		if _, err := w.RecordDiagnosticEvidenceBatches(batches); err != nil {
			t.Fatal(err)
		}
	}
	state := w.diagnostics.state
	final, _ := json.Marshal(state)
	if writes != 501 || len(state.Evidence) != 20 || len(state.Notices) != 20 ||
		len(final) > len(initial)+256 || len(w.diagnostics.baselineIndex) != 20 {
		t.Fatalf("repeated baselines grew: writes=%d evidence=%d notices=%d bytes=%d->%d index=%d",
			writes, len(state.Evidence), len(state.Notices), len(initial), len(final), len(w.diagnostics.baselineIndex))
	}
	assertDiagnosticReferenceBounds(t, state)
	// A semantic change must still generate the preexisting notice and body.
	batches[0].Findings = append(batches[0].Findings, finding("new cross-file error"))
	before := fmt.Sprintf("diagcur_%d", state.Sequence)
	updated, err := w.RecordDiagnosticEvidenceBatches(batches[:1])
	if err != nil || len(updated[0].New) != 0 {
		t.Fatalf("semantic update: %+v, %v", updated, err)
	}
	page, err := w.DiagnosticNoticesSince(before, 10)
	if err != nil || len(page.Notices) != 1 || page.Notices[0].Kind != "persisting" {
		t.Fatalf("semantic update was deduplicated away: %+v, %v", page, err)
	}
}

func assertDiagnosticReferenceBounds(t *testing.T, state diagnosticState) {
	t.Helper()
	check := func(owner string, ids []string) {
		if len(ids) > maxDiagnosticEvidenceReferences {
			t.Fatalf("%s owns %d evidence references", owner, len(ids))
		}
		for _, id := range ids {
			if _, exists := state.Evidence[id]; !exists {
				t.Fatalf("%s references missing evidence %s", owner, id)
			}
		}
	}
	for id, item := range state.Items {
		check(id, item.EvidenceIDs)
	}
	for name, dimension := range state.Dimensions {
		check(name, dimension.EvidenceIDs)
	}
	for name, dimension := range state.ProviderDimensions {
		check(name, dimension.EvidenceIDs)
	}
}

func TestBaselineUniqueObservationsBoundEveryReferenceOwnerAndFailClosed(t *testing.T) {
	store, _ := newDiagnosticStore("", "")
	batch := baselineBatch("a.go")
	first, err := store.record(batch)
	if err != nil {
		t.Fatal(err)
	}
	oldID := first.EvidenceIDs[0]
	for i := 0; i < 2000; i++ {
		batch.DocumentRevision = fmt.Sprintf("native_%d", i)
		batch.Findings[0].Range.StartLine = i
		batch.Findings[0].Range.EndLine = i
		if _, err := store.record(batch); err != nil {
			t.Fatal(err)
		}
	}
	assertDiagnosticReferenceBounds(t, store.state)
	if len(store.state.Evidence) > maxUnreferencedDiagnosticEvidence+maxDiagnosticEvidenceReferences ||
		len(store.baselineIndex) > len(store.state.Evidence) {
		t.Fatalf("unique observations retained unbounded history: evidence=%d index=%d", len(store.state.Evidence), len(store.baselineIndex))
	}
	if _, err := store.evidenceByID(oldID); err == nil {
		t.Fatal("expired proof still retained beyond storage cap")
	}
	batch.Baseline = false
	batch.PreEditDocumentRevision = "native_before"
	batch.Findings = append(batch.Findings, finding("introduced with old missing proof"))
	report, err := store.record(batch)
	if err != nil || len(report.New) != 1 || !report.New[0].BaselineMissing {
		t.Fatalf("newer proof substituted for missing preimage: %+v, %v", report, err)
	}
	batch = baselineBatch("a.go")
	again, err := store.record(batch)
	if err != nil || again.EvidenceIDs[0] == oldID {
		t.Fatalf("expired immutable evidence ID was reused: %+v, %v", again, err)
	}
	if _, err := store.evidenceByID(oldID); err == nil {
		t.Fatal("missing historical proof was silently replaced")
	}
}

func TestBaselineBulkFailureLeavesMemoryDiskAndCursorUnchanged(t *testing.T) {
	w, _ := evidenceWorkspace(t)
	batch := baselineBatch("a.go")
	if _, err := w.RecordDiagnosticEvidence(batch); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(w.diagnostics.state)
	beforeCursor := fmt.Sprintf("diagcur_%d", w.diagnostics.state.Sequence)
	onDisk, err := os.ReadFile(w.diagnostics.path)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	w.diagnostics.writeFile = func(string, []byte, os.FileMode) error {
		attempts++
		return errors.New("persist failed")
	}
	batches := []DiagnosticBatch{baselineBatch("a.go"), baselineBatch("b.go")}
	batches[0].Findings = append(batches[0].Findings, finding("must roll back"))
	if reports, err := w.RecordDiagnosticEvidenceBatches(batches); err == nil || len(reports) != 0 || attempts != 1 {
		t.Fatalf("bulk failure: reports=%+v attempts=%d err=%v", reports, attempts, err)
	}
	after, _ := json.Marshal(w.diagnostics.state)
	currentDisk, _ := os.ReadFile(w.diagnostics.path)
	if string(before) != string(after) || string(onDisk) != string(currentDisk) {
		t.Fatal("failed save exposed partial memory/disk/cursor transitions")
	}
	// Validate all input before processing, including the later batch.
	batches[1].Producer = ""
	if _, err := w.RecordDiagnosticEvidenceBatches(batches); err == nil || attempts != 1 {
		t.Fatal("invalid batch caused persistence or was accepted")
	}
	w.diagnostics.writeFile = nil
	batches[1].Producer = "test"
	if _, err := w.RecordDiagnosticEvidenceBatches(batches); err != nil {
		t.Fatal(err)
	}
	page, err := w.DiagnosticNoticesSince(beforeCursor, 10)
	if err != nil || len(page.Notices) != 2 {
		t.Fatalf("retry lost transitions: %+v, %v", page, err)
	}
}

func TestBaselineNativeInvalidationsShareBulkSaveAndRollback(t *testing.T) {
	w, _ := evidenceWorkspace(t)
	var batches []DiagnosticBatch
	for _, name := range []string{"a.go", "b.go"} {
		path := filepath.Join(w.Identity().Root, name)
		writeFile(t, path, "package fixture\n")
		snapshot, err := w.Snapshot(path, ProviderLayer{})
		if err != nil {
			t.Fatal(err)
		}
		batch := baselineBatch(path)
		batch.Baseline, batch.DocumentRevision = false, string(snapshot.Revision)
		batch.Findings = append(batch.Findings, finding("omitted"))
		batches = append(batches, batch)
	}
	initial, err := w.RecordDiagnosticEvidenceBatches(batches)
	if err != nil {
		t.Fatal(err)
	}
	for i := range batches {
		writeFile(t, batches[i].Document, "package fixture\n// external change\n")
		batches[i].Baseline = true
		batches[i].Findings = batches[i].Findings[:1]
	}
	before, _ := json.Marshal(w.diagnostics.state)
	attempts := 0
	w.diagnostics.writeFile = func(string, []byte, os.FileMode) error {
		attempts++
		return errors.New("save failure")
	}
	if _, err := w.RecordDiagnosticBaselines(batches); err == nil || attempts != 1 {
		t.Fatalf("native bulk failure: attempts=%d err=%v", attempts, err)
	}
	after, _ := json.Marshal(w.diagnostics.state)
	if string(before) != string(after) {
		t.Fatal("external invalidation escaped failed baseline transaction")
	}
	w.diagnostics.writeFile = func(path string, data []byte, mode os.FileMode) error {
		attempts++
		return atomicWriteFile(path, data, mode)
	}
	reports, err := w.RecordDiagnosticBaselines(batches)
	if err != nil || attempts != 2 || len(reports) != 2 {
		t.Fatalf("native multi-document save: attempts=%d reports=%d err=%v", attempts, len(reports), err)
	}
	last := reports[len(reports)-1]
	if last.CurrentCount != 2 || last.StaleCount != 2 || len(last.New) != 0 || len(last.Resolved) != 0 {
		t.Fatalf("external omissions were lost or left current: %+v", last)
	}
	page, err := w.DiagnosticNoticesSince(initial[len(initial)-1].Cursor, 20)
	if err != nil || len(page.Notices) != 6 {
		t.Fatalf("external invalidation/refresh notices: %+v, %v", page, err)
	}
	w.diagnostics.writeFile = nil
	if _, err := w.RecordDiagnosticBaselines(batches); err != nil {
		t.Fatal(err)
	}
	repeated, err := w.DiagnosticNoticesSince(page.Cursor, 20)
	if err != nil || len(repeated.Notices) != 0 {
		t.Fatalf("same native baseline reannounced freshness: %+v, %v", repeated, err)
	}
}

func TestBaselineRetainedPreparationProofSurvivesChurnAndLedgerReopen(t *testing.T) {
	w, stateDir := evidenceWorkspace(t)
	stage := baselineBatch("prepared.go")
	stage.Staged, stage.Baseline = true, false
	stage.DocumentRevision, stage.TransactionID = "prepared_revision", "plan"
	proof, err := w.RecordDiagnosticEvidence(stage)
	if err != nil {
		t.Fatal(err)
	}
	id := proof.EvidenceIDs[0]
	original, err := w.Evidence(id)
	if err != nil {
		t.Fatal(err)
	}
	// Model the retained preparation owner without invoking a provider. The
	// same stage IDs are persisted by the plan machinery and read on reopen.
	plan := PlanRecord{PlanID: "retained", Preparation: &PlanPreparation{
		Verification: []VerificationStage{{Stage: "diagnostics", EvidenceIDs: []string{id}}},
	}}
	w.plansMu.Lock()
	w.plans[plan.PlanID] = plan
	w.plansMu.Unlock()
	batch := baselineBatch("a.go")
	w.diagnostics.writeFile = func(string, []byte, os.FileMode) error { return nil }
	for i := 0; i < 600; i++ {
		batch.DocumentRevision = fmt.Sprintf("native_%d", i)
		if _, err := w.RecordDiagnosticEvidence(batch); err != nil {
			t.Fatal(err)
		}
	}
	kept, err := w.Evidence(id)
	if err != nil || string(kept.Payload) != string(original.Payload) {
		t.Fatalf("retained preparation proof pruned/replaced: %v", err)
	}
	w.diagnostics.writeFile = nil
	// Force age pruning too. Query must protect the preparation's exact ID.
	w.diagnostics.mu.Lock()
	aged := w.diagnostics.state.Evidence[id]
	aged.RecordedAt = time.Now().Add(-2 * diagnosticRetentionWindow)
	w.diagnostics.state.Evidence[id] = aged
	w.diagnostics.mu.Unlock()
	if _, err := w.Diagnostics(""); err != nil {
		t.Fatal(err)
	}
	reopened, err := newDiagnosticStore(stateDir, w.Identity().ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.evidenceByID(id)
	if err != nil || string(persisted.Payload) != string(original.Payload) {
		t.Fatalf("prepared proof not persisted: %v", err)
	}
	// Once the plan owner is gone, its old ID expires and retrieval fails.
	w.plansMu.Lock()
	delete(w.plans, plan.PlanID)
	w.plansMu.Unlock()
	if _, err := w.Diagnostics(""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Evidence(id); err == nil {
		t.Fatal("released old preparation proof did not expire")
	}
}
