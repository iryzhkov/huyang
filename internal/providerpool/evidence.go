package providerpool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type providerDiagnosticPayload struct {
	Batches []workspacecore.DiagnosticBatch `json:"batches"`
}

func diagnosticSourcePath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	return clean != ".huyang.toml" &&
		clean != ".huyang/pipeline.json" &&
		filepath.Base(clean) != ".gitignore"
}

func RecordDiagnostics(ctx context.Context, workspace *workspacecore.Workspace, backend provider.Provider, files []workspacecore.PlanStageFile, revision, transactionID string, settleWait ...time.Duration) (workspacecore.DiagnosticReport, error) {
	return recordDiagnostics(ctx, workspace, backend, files, revision, transactionID, false, settleWait...)
}

// RecordStagedDiagnostics records what a language server made of bytes that
// exist only inside a preparation. The caller gets the same report, and the
// workspace's ledger is left describing the workspace: a proposal nobody
// applied is not evidence about the files on disk.
func RecordStagedDiagnostics(ctx context.Context, workspace *workspacecore.Workspace, backend provider.Provider, files []workspacecore.PlanStageFile, revision, transactionID string, settleWait ...time.Duration) (workspacecore.DiagnosticReport, error) {
	return recordDiagnostics(ctx, workspace, backend, files, revision, transactionID, true, settleWait...)
}

func recordDiagnostics(ctx context.Context, workspace *workspacecore.Workspace, backend provider.Provider, files []workspacecore.PlanStageFile, revision, transactionID string, staged bool, settleWait ...time.Duration) (workspacecore.DiagnosticReport, error) {
	if backend == nil {
		return workspacecore.DiagnosticReport{}, fmt.Errorf("diagnostic provider unavailable")
	}
	descriptor := backend.Descriptor()
	waitMS := 1500
	if len(settleWait) > 0 && settleWait[0] > 0 {
		waitMS = int(settleWait[0] / time.Millisecond)
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file.AfterExists && diagnosticSourcePath(file.Path) {
			paths = append(paths, filepath.Join(descriptor.Root, filepath.FromSlash(file.Path)))
		}
	}
	if len(paths) == 0 {
		return workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
			Kind: workspacecore.EvidenceProjectCheck, ProviderID: "not_applicable", Producer: "diagnostic_scope",
			Document: workspace.Identity().Root, DocumentRevision: revision, TransactionID: transactionID,
			Complete: true, Selected: true, Staged: staged, Dimension: "no_lsp_applicable_edited_documents",
		})
	}
	deadline := time.Now().Add(diagnosticEvidenceTimeout(waitMS))
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	result, err := Call(callCtx, workspace, backend, CallSpec{
		RequestID: fmt.Sprintf("diagnostics_%s", transactionID), TransactionID: transactionID,
	}, "huyang_diagnostic_evidence", map[string]any{"files": paths, "revision": revision, "transaction_id": transactionID, "wait_ms": waitMS})
	if err != nil {
		return workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
			Kind: workspacecore.EvidencePush, ProviderID: string(descriptor.ID), Producer: descriptor.Backend, ProducerVersion: err.Error(),
			Document: workspace.Identity().Root, DocumentRevision: revision, TransactionID: transactionID,
			TimedOut: true, Selected: true, Staged: staged, Dimension: "edited_documents",
		})
	}
	payload, err := diagnosticPayload(result)
	if err != nil {
		return workspacecore.DiagnosticReport{}, err
	}
	if len(payload.Batches) == 0 {
		return workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
			Kind: workspacecore.EvidencePush, ProviderID: string(descriptor.ID), Producer: descriptor.Backend,
			Document: workspace.Identity().Root, DocumentRevision: revision, TransactionID: transactionID,
			TimedOut: true, Selected: true, Staged: staged, Dimension: "edited_documents",
		})
	}
	// A pull may supersede a push for the same producer/document. Select
	// the strongest observation before recording, so an intermediate empty
	// batch cannot resolve findings that a current pull still reports.
	batches := selectDiagnosticBatches(payload.Batches)
	covered := map[string]bool{}
	for _, batch := range batches {
		covered[filepath.Clean(batch.Document)] = true
	}
	for _, path := range paths {
		if !covered[filepath.Clean(path)] {
			batches = append(batches, workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush,
				ProviderID: string(descriptor.ID), Producer: descriptor.Backend, Document: path, Selected: true, Dimension: "edited_documents"})
		}
	}
	preEdit := map[string]string{}
	for _, file := range files {
		if revision, err := workspace.DiagnosticPreEditRevision(file); err == nil {
			preEdit[filepath.Clean(filepath.Join(workspace.Identity().Root, file.Path))] = revision
		}
	}
	report := workspacecore.DiagnosticReport{Confidence: workspacecore.ConfidenceAuthoritative}
	for _, batch := range batches {
		batch.Selected = true
		batch.Staged = staged
		batch.TransactionID = transactionID
		batch.DocumentRevision = revision
		if relative, relErr := filepath.Rel(descriptor.Root, batch.Document); relErr == nil {
			batch.Document = filepath.Join(workspace.Identity().Root, relative)
		}
		batch.PreEditDocumentRevision = preEdit[filepath.Clean(batch.Document)]
		if !staged {
			if snapshot, err := workspace.Snapshot(batch.Document, workspacecore.ProviderLayer{}); err == nil {
				batch.DocumentRevision = string(snapshot.Revision)
			}
		}
		observed, recordErr := workspace.RecordDiagnosticEvidence(batch)
		if recordErr != nil {
			return workspacecore.DiagnosticReport{}, recordErr
		}
		mergeDiagnosticReport(&report, observed, staged)
	}
	return report, nil
}

func publicationProven(batch workspacecore.DiagnosticBatch) bool {
	return batch.Complete && batch.DocumentVersion != nil && batch.ExpectedVersion != nil &&
		*batch.DocumentVersion >= *batch.ExpectedVersion && !batch.ProgressPending && !batch.TimedOut
}

func selectDiagnosticBatches(batches []workspacecore.DiagnosticBatch) []workspacecore.DiagnosticBatch {
	var selected []workspacecore.DiagnosticBatch
	index := map[string]int{}
	for _, batch := range batches {
		key := batch.ProviderID + "\x00" + batch.Document
		if i, ok := index[key]; ok {
			if publicationProven(batch) || !publicationProven(selected[i]) {
				selected[i] = batch
			}
		} else {
			index[key] = len(selected)
			selected = append(selected, batch)
		}
	}
	return selected
}

// Late payloads are ordered observations, not competing proof sources. Keep
// only the newest observation per document/producer, even if it weakens proof:
// a superseded complete empty set must never resolve a later positive set.
func coalesceLateDiagnosticBatches(batches []workspacecore.DiagnosticBatch) []workspacecore.DiagnosticBatch {
	var out []workspacecore.DiagnosticBatch
	index := map[string]int{}
	for _, batch := range batches {
		key := batch.ProviderID + "\x00" + batch.Document
		if i, ok := index[key]; ok {
			prior := out[i]
			if prior.ExpectedVersion != nil && batch.ExpectedVersion != nil && *batch.ExpectedVersion < *prior.ExpectedVersion {
				continue
			}
			if prior.DocumentVersion != nil && batch.DocumentVersion != nil && *batch.DocumentVersion < *prior.DocumentVersion {
				continue
			}
			out[i] = batch
		} else {
			index[key] = len(out)
			out = append(out, batch)
		}
	}
	return out
}

func mergeDiagnosticReport(report *workspacecore.DiagnosticReport, observed workspacecore.DiagnosticReport, staged bool) {
	rank := func(c workspacecore.DiagnosticConfidence) int {
		switch c {
		case workspacecore.ConfidenceAuthoritative:
			return 3
		case workspacecore.ConfidenceCorroborated:
			return 2
		case workspacecore.ConfidenceProvisional:
			return 1
		default:
			return 0
		}
	}
	if rank(observed.Confidence) < rank(report.Confidence) {
		report.Confidence = observed.Confidence
	}
	report.New = append(report.New, observed.New...)
	report.Current = append(report.Current, observed.Current...)
	report.Resolved = append(report.Resolved, observed.Resolved...)
	report.EvidenceIDs = append(report.EvidenceIDs, observed.EvidenceIDs...)
	for _, reason := range observed.ProvisionalReasons {
		found := false
		for _, prior := range report.ProvisionalReasons {
			if prior == reason {
				found = true
				break
			}
		}
		if !found {
			report.ProvisionalReasons = append(report.ProvisionalReasons, reason)
		}
	}
	report.Coverage, report.Cursor = observed.Coverage, observed.Cursor
	if staged {
		// Staged reports count only their own document. Canonical reports
		// already carry workspace totals and must not be summed.
		report.CurrentCount += observed.CurrentCount
		report.StaleCount += observed.StaleCount
		report.Stale = append(report.Stale, observed.Stale...)
	} else {
		report.CurrentCount, report.StaleCount = observed.CurrentCount, observed.StaleCount
		report.Stale = observed.Stale
	}
	report.PreexistingCount = max(0, report.CurrentCount-len(report.New))
}

// RecordDiagnosticBaseline reads only already-loaded provider buffers. It neither
// attaches a server nor sends an LSP request, so the pre-edit path adds one local
// RPC and no analysis wait.
func RecordDiagnosticBaseline(ctx context.Context, workspace *workspacecore.Workspace, backend provider.Provider) error {
	callCtx, cancel := context.WithTimeout(ctx, lateEvidenceTimeout)
	defer cancel()
	result, err := Call(callCtx, workspace, backend, CallSpec{RequestID: "diagnostic_baseline"}, "huyang_diagnostic_evidence", map[string]any{"baseline": true, "wait_ms": 0})
	if err != nil {
		return err
	}
	payload, err := diagnosticPayload(result)
	if err != nil {
		return err
	}
	var batches []workspacecore.DiagnosticBatch
	for _, batch := range payload.Batches {
		if batch.Staged || !publicationProven(batch) {
			continue
		}
		batch.Selected, batch.Baseline = true, true
		batch.TransactionID, batch.Candidates = "", nil
		if relative, relErr := filepath.Rel(backend.Descriptor().Root, batch.Document); relErr == nil {
			batch.Document = filepath.Join(workspace.Identity().Root, relative)
		}
		batches = append(batches, batch)
	}
	_, err = workspace.RecordDiagnosticBaselines(selectDiagnosticBatches(batches))
	if err != nil {
		return err
	}
	return nil
}

// lateEvidenceTimeout bounds the collection of late batches: the kernel
// answers from memory, so the call never waits on a server.
const lateEvidenceTimeout = 2 * time.Second

// CollectLateEvidence records the findings a language server published
// after a verdict had already answered unavailable for the same buffer
// version (a server that attached late). The kernel attributes each batch
// to the transaction whose version it matched, so the ledger's notices
// name that transaction; nothing is attributed by arrival time. It returns
// how many batches were recorded and never fails the caller's own work.
func CollectLateEvidence(ctx context.Context, workspace *workspacecore.Workspace, backend provider.Provider) (int, error) {
	if backend == nil {
		return 0, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, lateEvidenceTimeout)
	defer cancel()
	result, err := Call(callCtx, workspace, backend, CallSpec{RequestID: "late_evidence"}, "huyang_late_evidence", nil)
	if err != nil {
		return 0, err
	}
	payload, err := diagnosticPayload(result)
	if err != nil {
		return 0, err
	}
	descriptor := backend.Descriptor()
	recorded := 0
	for _, batch := range coalesceLateDiagnosticBatches(payload.Batches) {
		batch.Selected = true
		if relative, relErr := filepath.Rel(descriptor.Root, batch.Document); relErr == nil {
			batch.Document = filepath.Join(workspace.Identity().Root, relative)
		}
		if _, err := workspace.RecordDiagnosticEvidence(batch); err != nil {
			return recorded, err
		}
		recorded++
	}
	return recorded, nil
}

// diagnosticPayload reads the diagnostic batches of a provider result. The
// kernel reports them as typed Result.Evidence; the untyped Value is decoded
// only for a provider that predates that field.
func diagnosticPayload(result provider.Result) (providerDiagnosticPayload, error) {
	if len(result.Evidence) > 0 {
		encoded, err := json.Marshal(result.Evidence)
		if err != nil {
			return providerDiagnosticPayload{}, err
		}
		var batches []workspacecore.DiagnosticBatch
		if err := json.Unmarshal(encoded, &batches); err != nil {
			return providerDiagnosticPayload{}, err
		}
		return providerDiagnosticPayload{Batches: batches}, nil
	}
	encoded, err := json.Marshal(result.Value)
	if err != nil {
		return providerDiagnosticPayload{}, err
	}
	var payload providerDiagnosticPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return providerDiagnosticPayload{}, err
	}
	return payload, nil
}

// diagnosticEvidenceTimeout bounds provider overhead while still allowing the
// caller-requested diagnostic settle window. A stalled LSP therefore produces
// provisional timed-out evidence promptly instead of consuming the whole
// tool-call deadline.
func diagnosticEvidenceTimeout(waitMS int) time.Duration {
	if waitMS < 0 {
		waitMS = 0
	}
	return time.Duration(waitMS)*time.Millisecond + 2*time.Second
}

func DiagnosticVerificationStage(revision string, report workspacecore.DiagnosticReport) workspacecore.VerificationStage {
	status := workspacecore.VerificationPassed
	complete := true
	errorCount := 0
	for _, item := range report.Current {
		if item.Finding.Severity == 0 || item.Finding.Severity == 1 {
			errorCount++
		}
	}
	if report.Confidence == workspacecore.ConfidenceProvisional || report.Confidence == workspacecore.ConfidenceUnavailable {
		status = workspacecore.VerificationSkipped
		complete = false
	}
	output := ""
	if errorCount > 0 {
		status = workspacecore.VerificationFailed
		output = fmt.Sprintf("%d error diagnostics", errorCount)
	}
	return workspacecore.VerificationStage{
		Stage: "diagnostics", Mode: "provider", StartedRevision: revision, Status: status, Output: output,
		Coverage:    workspacecore.Coverage{Complete: complete, Skipped: append([]string(nil), report.ProvisionalReasons...), Semantic: string(report.Confidence)},
		EvidenceIDs: append([]string(nil), report.EvidenceIDs...),
	}
}

func CorroborateWithProjectCheck(workspace *workspacecore.Workspace, revision, transactionID string, stages []workspacecore.VerificationStage, report workspacecore.DiagnosticReport) (workspacecore.DiagnosticReport, error) {
	return corroborateWithProjectCheck(workspace, revision, transactionID, stages, report, false)
}

// CorroborateStagedWithProjectCheck is the same corroboration for a
// preparation: the project check ran inside the sandbox, so what it proves is
// proved about the staged bytes and recorded as such.
func CorroborateStagedWithProjectCheck(workspace *workspacecore.Workspace, revision, transactionID string, stages []workspacecore.VerificationStage, report workspacecore.DiagnosticReport) (workspacecore.DiagnosticReport, error) {
	return corroborateWithProjectCheck(workspace, revision, transactionID, stages, report, true)
}

func corroborateWithProjectCheck(workspace *workspacecore.Workspace, revision, transactionID string, stages []workspacecore.VerificationStage, report workspacecore.DiagnosticReport, staged bool) (workspacecore.DiagnosticReport, error) {
	if report.Confidence != workspacecore.ConfidenceProvisional && report.Confidence != workspacecore.ConfidenceUnavailable {
		return report, nil
	}
	// A project check can corroborate only freshness uncertainty from an
	// attached provider. Any simultaneous availability failure must remain
	// visible instead of being promoted to semantic evidence.
	corroboratable := len(report.ProvisionalReasons) > 0
	for _, reason := range report.ProvisionalReasons {
		switch reason {
		case "rust_diagnostics_pending", "diagnostic_barrier_timed_out", "push_missing_current_document_proof", "pull_incomplete_or_missing_result_id", "work_done_progress_pending":
		default:
			corroboratable = false
		}
	}
	if !corroboratable {
		return report, nil
	}
	for _, stage := range stages {
		if stage.Stage == "check" && stage.Status == workspacecore.VerificationPassed {
			return workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
				Kind: workspacecore.EvidenceProjectCheck, ProviderID: "project_check", Producer: "configured_project_check",
				Document: workspace.Identity().Root, DocumentRevision: revision, TransactionID: transactionID,
				Complete: true, Selected: true, Staged: staged, Dimension: "edited_documents",
			})
		}
	}
	return report, nil
}
