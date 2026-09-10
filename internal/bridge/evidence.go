package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"agent99/internal/provider"
	workspacecore "agent99/internal/workspace"
)

type providerDiagnosticPayload struct {
	Batches []workspacecore.DiagnosticBatch `json:"batches"`
}

func recordProviderDiagnostics(ctx context.Context, workspace *workspacecore.Workspace, backend provider.Provider, files []workspacecore.PlanStageFile, revision, transactionID string) (workspacecore.DiagnosticReport, error) {
	if backend == nil {
		return workspacecore.DiagnosticReport{}, fmt.Errorf("diagnostic provider unavailable")
	}
	descriptor := backend.Descriptor()
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file.AfterExists {
			paths = append(paths, filepath.Join(descriptor.Root, filepath.FromSlash(file.Path)))
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	result, err := backend.Call(callCtx, provider.Request{
		Context: provider.RequestContext{
			RequestID: fmt.Sprintf("diagnostics_%s", transactionID), WorkspaceID: string(workspace.Identity().ID),
			Epoch: workspace.Identity().Epoch, TransactionID: transactionID, Deadline: deadline,
			Cancellation: descriptor.Cancellation,
		},
		Operation: "huyang_diagnostic_evidence",
		Arguments: map[string]any{"files": paths, "revision": revision, "transaction_id": transactionID},
	})
	if err != nil {
		return workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
			Kind: workspacecore.EvidencePush, ProviderID: string(descriptor.ID), Producer: descriptor.Backend, ProducerVersion: err.Error(),
			Document: workspace.Identity().Root, DocumentRevision: revision, TransactionID: transactionID,
			TimedOut: true, Selected: true, Dimension: "edited_documents",
		})
	}
	encoded, err := json.Marshal(result.Value)
	if err != nil {
		return workspacecore.DiagnosticReport{}, err
	}
	var payload providerDiagnosticPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
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

func diagnosticVerificationStage(revision string, report workspacecore.DiagnosticReport) workspacecore.VerificationStage {
	status := workspacecore.VerificationPassed
	complete := true
	if report.Confidence == workspacecore.ConfidenceProvisional || report.Confidence == workspacecore.ConfidenceUnavailable {
		status = workspacecore.VerificationSkipped
		complete = false
	}
	return workspacecore.VerificationStage{
		Stage: "diagnostics", Mode: "provider", StartedRevision: revision, Status: status,
		Coverage:    workspacecore.Coverage{Complete: complete, Skipped: append([]string(nil), report.ProvisionalReasons...), Semantic: string(report.Confidence)},
		EvidenceIDs: append([]string(nil), report.EvidenceIDs...),
	}
}

func corroborateDiagnosticsWithProjectCheck(workspace *workspacecore.Workspace, revision, transactionID string, stages []workspacecore.VerificationStage, report workspacecore.DiagnosticReport) (workspacecore.DiagnosticReport, error) {
	if report.Confidence != workspacecore.ConfidenceProvisional && report.Confidence != workspacecore.ConfidenceUnavailable {
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
