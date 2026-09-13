package handlers

// The semantic view of a change, wherever the change is.
//
// One builder answers for a plan that has only been previewed, a plan that has
// been prepared, and a plan that has been applied, because all three are the
// same question asked at different moments and an agent that got three
// different answers would have to decide which to believe. What differs
// between them is the evidence available, and that difference is reported
// rather than smoothed over.

import (
	"context"
	"fmt"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// semanticView answers preview and inspect as a semantic summary when the
// caller asked for that view, which only the experimental catalog offers.
func (h *Handlers) semanticView(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request planRequest, outcome planOutcome) map[string]any {
	if request.View != "semantic" || outcome.err != nil {
		return nil
	}
	return h.planSemantic(ctx, requestID, workspace, outcome.plan)
}

// planSemantic summarises what one plan means. A committed plan is summarised
// from the bytes that were written, every other state from the bytes the
// preview holds.
func (h *Handlers) planSemantic(ctx context.Context, requestID string, workspace *workspacecore.Workspace, plan workspacecore.PlanRecord) map[string]any {
	diffs, from, to := planSemanticSource(workspace, plan)
	if len(diffs) == 0 {
		return mcpapi.Envelope(requestID, workspace, "unavailable", "semantic_evidence_unavailable",
			"this plan holds no exact bytes to compare; preview or prepare it first",
			map[string]any{"plan_id": plan.PlanID, "state": plan.State})
	}
	input := workspacecore.SemanticSummaryInput{
		FromRevision: from, ToRevision: to, Files: semanticFiles(diffs), Invariants: plan.Invariants,
	}
	if plan.Preparation != nil {
		input.Verification = plan.Preparation.Verification
		for _, stage := range plan.Preparation.Verification {
			input.EvidenceIDs = append(input.EvidenceIDs, stage.EvidenceIDs...)
		}
	}
	input.Diagnostics, input.Gaps = h.semanticDiagnostics(ctx, requestID, workspace, plan)
	summary := workspacecore.BuildSemanticChangeSummary(input)
	result := mcpapi.Envelope(requestID, workspace, "ok", "", summary.Summary, map[string]any{
		"semantic_summary": summary, "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision,
		"state": plan.State,
	})
	result["evidence"] = map[string]any{"ids": mcpapi.NonNilStrings(summary.EvidenceIDs), "truncated": false}
	return result
}

// planSemanticSource picks the exact bytes to compare and names both ends of
// the comparison, so a reader always knows which two things were compared.
func planSemanticSource(workspace *workspacecore.Workspace, plan workspacecore.PlanRecord) ([]workspacecore.ExactDiff, string, string) {
	canonical := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	if plan.State == workspacecore.PlanCommitted && plan.Preparation != nil && len(plan.Preparation.CommittedDiffs) > 0 {
		from := plan.Preparation.CanonicalFromRevision
		if from == "" {
			from = plan.Preparation.BaseRevision
		}
		return plan.Preparation.CommittedDiffs, from, plan.Preparation.CanonicalRevision
	}
	if plan.Preview == nil {
		return nil, canonical, ""
	}
	to := plan.Preview.PreviewRevision
	if plan.Preparation != nil {
		to = plan.Preparation.PreparedRevision
	}
	return plan.Preview.Diffs, canonical, to
}

// semanticFiles turns exact diffs into the two-sided input the builder reads.
// A missing hash on either side is what says the file was created or deleted.
func semanticFiles(diffs []workspacecore.ExactDiff) []workspacecore.SemanticFileInput {
	files := make([]workspacecore.SemanticFileInput, 0, len(diffs))
	for _, diff := range diffs {
		files = append(files, workspacecore.SemanticFileInput{
			Path: diff.Path, Before: diff.Before, After: diff.After,
			BeforeExists: diff.BeforeSHA256 != "", AfterExists: diff.AfterSHA256 != "",
		})
	}
	return files
}

// semanticDiagnostics is what the language server makes of the change, when
// there is still a preparation to ask. A plan that was applied or discarded no
// longer has one, and that is a gap rather than a clean report.
func (h *Handlers) semanticDiagnostics(ctx context.Context, requestID string, workspace *workspacecore.Workspace, plan workspacecore.PlanRecord) (*workspacecore.DiagnosticDelta, []string) {
	stager := h.pool.Stager(workspace.Identity().ID, plan.PlanID)
	if stager == nil {
		return nil, []string{"diagnostics: this plan has no prepared sandbox to ask"}
	}
	view, prepared := stager.Prepared()
	if !prepared {
		return nil, []string{"diagnostics: this plan is not prepared, so nothing has read its bytes"}
	}
	files := view.Files
	if len(files) > preparedDiagnosticBudget {
		return nil, []string{fmt.Sprintf(
			"diagnostics: the plan stages %d files and one call diagnoses %d", len(files), preparedDiagnosticBudget)}
	}
	var findings []workspacecore.ComparableFinding
	for _, path := range files {
		absolute, err := preparedPath(view, path)
		if err != nil {
			return nil, []string{"diagnostics: " + err.Error()}
		}
		value, failure := h.callPrepared(ctx, requestID+"_"+path, workspace, view, "diagnostics", map[string]any{
			"root": view.Tree, "file": absolute,
		})
		if failure != nil {
			return nil, []string{"diagnostics: no language server answered for " + path}
		}
		findings = append(findings, comparableFindings(path, value)...)
	}
	return h.preparedDelta(ctx, requestID, workspace, plan, view, findings, files), nil
}

// revisionDiffSemantic summarises a canonical revision range.
//
// Only a committed plan keeps the bytes of both sides; a direct edit's receipt
// keeps hashes and byte counts, which is enough to prove what changed and not
// enough to say what it meant. So the summary covers the plans in the range
// and says plainly that the rest of it could not be read, rather than
// presenting a partial answer as the whole one.
func (h *Handlers) revisionDiffSemantic(ctx context.Context, requestID string, workspace *workspacecore.Workspace, span revisionRange) map[string]any {
	plans := workspace.CommittedPlansBetween(span.fromSeq, span.toSeq)
	var diffs []workspacecore.ExactDiff
	var stages []workspacecore.VerificationStage
	var invariants []workspacecore.PlanInvariant
	var evidence []string
	for _, plan := range plans {
		diffs = append(diffs, plan.Preparation.CommittedDiffs...)
		stages = append(stages, plan.Preparation.Verification...)
		invariants = append(invariants, plan.Invariants...)
		for _, stage := range plan.Preparation.Verification {
			evidence = append(evidence, stage.EvidenceIDs...)
		}
	}
	gaps := []string{}
	if steps := span.toSeq - span.fromSeq; uint64(len(plans)) < steps {
		gaps = append(gaps, fmt.Sprintf(
			"%d of the %d revision step(s) in this range were not applied plans; their receipts keep hashes and byte counts rather than content, so nothing semantic can be read from them",
			steps-uint64(len(plans)), steps))
	}
	if len(diffs) == 0 {
		result := mcpapi.Envelope(requestID, workspace, "unavailable", "semantic_evidence_unavailable",
			"no applied plan covers this revision range, and a direct edit's receipt keeps hashes rather than content",
			map[string]any{"from_revision": span.from, "to_revision": span.to, "gaps": gaps})
		result["next"] = []any{
			map[string]any{"tool": "change_plan", "action": "inspect", "view": "semantic",
				"note": "A plan knows the bytes on both sides of its own change."},
		}
		return result
	}
	summary := workspacecore.BuildSemanticChangeSummary(workspacecore.SemanticSummaryInput{
		FromRevision: span.from, ToRevision: span.to, Files: semanticFiles(diffs),
		Verification: stages, Invariants: invariants, EvidenceIDs: evidence, Gaps: gaps,
	})
	result := mcpapi.Envelope(requestID, workspace, "ok", "", summary.Summary, map[string]any{
		"semantic_summary": summary, "from_revision": span.from, "to_revision": span.to,
		"plans": len(plans),
	})
	result["evidence"] = map[string]any{"ids": mcpapi.NonNilStrings(summary.EvidenceIDs), "truncated": false}
	return result
}
