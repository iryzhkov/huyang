package bridge

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

func recordProviderDiagnostics(ctx context.Context, workspace *workspacecore.Workspace, backend provider.Provider, files []workspacecore.PlanStageFile, revision, transactionID string, settleWait ...time.Duration) (workspacecore.DiagnosticReport, error) {
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
			Complete: true, Selected: true, Dimension: "no_lsp_applicable_edited_documents",
		})
	}
	deadline := time.Now().Add(diagnosticEvidenceTimeout(waitMS))
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	result, err := callProvider(callCtx, workspace, backend, providerCall{
		RequestID: fmt.Sprintf("diagnostics_%s", transactionID), TransactionID: transactionID,
	}, "huyang_diagnostic_evidence", map[string]any{"files": paths, "revision": revision, "transaction_id": transactionID, "wait_ms": waitMS})
	if err != nil {
		return workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
			Kind: workspacecore.EvidencePush, ProviderID: string(descriptor.ID), Producer: descriptor.Backend, ProducerVersion: err.Error(),
			Document: workspace.Identity().Root, DocumentRevision: revision, TransactionID: transactionID,
			TimedOut: true, Selected: true, Dimension: "edited_documents",
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
			TimedOut: true, Selected: true, Dimension: "edited_documents",
		})
	}
	var report workspacecore.DiagnosticReport
	for _, batch := range payload.Batches {
		batch.Selected = true
		batch.TransactionID = transactionID
		batch.DocumentRevision = revision
		if relative, relErr := filepath.Rel(descriptor.Root, batch.Document); relErr == nil {
			batch.Document = filepath.Join(workspace.Identity().Root, relative)
		}
		report, err = workspace.RecordDiagnosticEvidence(batch)
		if err != nil {
			return workspacecore.DiagnosticReport{}, err
		}
	}
	return report, nil
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

func diagnosticVerificationStage(revision string, report workspacecore.DiagnosticReport) workspacecore.VerificationStage {
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

func corroborateDiagnosticsWithProjectCheck(workspace *workspacecore.Workspace, revision, transactionID string, stages []workspacecore.VerificationStage, report workspacecore.DiagnosticReport) (workspacecore.DiagnosticReport, error) {
	if report.Confidence != workspacecore.ConfidenceProvisional && report.Confidence != workspacecore.ConfidenceUnavailable {
		return report, nil
	}
	// A project check can corroborate only freshness uncertainty from an
	// attached provider. Any simultaneous availability failure must remain
	// visible instead of being promoted to semantic evidence.
	corroboratable := len(report.ProvisionalReasons) > 0
	for _, reason := range report.ProvisionalReasons {
		switch reason {
		case "diagnostic_barrier_timed_out", "push_missing_current_document_proof", "pull_incomplete_or_missing_result_id", "work_done_progress_pending":
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
				Complete: true, Selected: true, Dimension: "edited_documents",
			})
		}
	}
	return report, nil
}
