package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// planRequest is the decoded change_plan call. Operations and Edit are
// present only for the actions that carry them; PlanRevision is zero when
// omitted, which the workspace reads as the plan's current revision.
type planRequest struct {
	Action            string
	PlanID            string
	PlanRevision      uint64
	PreparedRevision  string
	AcceptProvisional bool
	Operations        []workspacecore.PlanOperation
	HasOperations     bool
	Edit              workspacecore.PlanEdit
}

// planOutcome is what one plan action produced: the record to render and,
// when the action failed, the error to classify. A failed action may still
// carry the plan so the caller sees its state.
type planOutcome struct {
	summary string
	plan    workspacecore.PlanRecord
	err     error
}

func decodePlanRequest(arguments map[string]any) (planRequest, error) {
	request := planRequest{
		PlanID:           fmt.Sprint(arguments["plan_id"]),
		PlanRevision:     uintArgument(arguments["plan_revision"]),
		PreparedRevision: fmt.Sprint(arguments["prepared_revision"]),
	}
	request.Action, _ = arguments["action"].(string)
	request.AcceptProvisional, _ = arguments["accept_provisional"].(bool)
	if raw, present := arguments["operations"]; present {
		request.HasOperations = true
		if err := decodeJSON(raw, &request.Operations); err != nil {
			return request, err
		}
	}
	if raw, present := arguments["edit"]; present {
		if err := decodeJSON(raw, &request.Edit); err != nil {
			return request, err
		}
	}
	return request, nil
}

// decodeJSON converts a decoded-JSON value into a typed record.
func decodeJSON(value any, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func (h *Handlers) changePlan(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	request, err := decodePlanRequest(arguments)
	if err != nil {
		return h.planFailure(requestID, workspace, request.Action, planOutcome{err: err})
	}
	var outcome planOutcome
	switch request.Action {
	case "create":
		outcome = h.planCreate(ctx, requestID, workspace, request)
	case "edit":
		outcome = h.planEdit(ctx, requestID, workspace, request)
	case "preview":
		outcome = planPreview(workspace, request)
	case "inspect":
		outcome = planInspect(workspace, request)
	case "prepare":
		outcome = h.planPrepare(ctx, requestID, workspace, request)
	case "discard":
		outcome = h.planDiscard(ctx, workspace, request)
	case "apply":
		return h.planApply(ctx, requestID, workspace, request)
	default:
		outcome.err = fmt.Errorf("unknown change_plan action %q", request.Action)
	}
	if outcome.err != nil {
		return h.planFailure(requestID, workspace, request.Action, outcome)
	}
	return planResult(requestID, workspace, outcome.summary, outcome.plan)
}

func (h *Handlers) planCreate(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request planRequest) planOutcome {
	if err := h.resolvePlanSymbolLocators(ctx, requestID, workspace, request.Operations); err != nil {
		return planOutcome{err: err}
	}
	operations, err := h.expandProviderOperations(ctx, requestID, workspace, request.Operations)
	if err != nil {
		return planOutcome{err: err}
	}
	plan, err := workspace.CreatePlan(operations)
	return planOutcome{summary: "Plan intent created; canonical workspace unchanged", plan: plan, err: err}
}

func (h *Handlers) planEdit(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request planRequest) planOutcome {
	if err := h.resolvePlanSymbolLocators(ctx, requestID, workspace, request.Edit.Operations); err != nil {
		return planOutcome{err: err}
	}
	expanded, err := h.expandProviderOperations(ctx, requestID, workspace, request.Edit.Operations)
	if err != nil {
		return planOutcome{err: err}
	}
	request.Edit.Operations = expanded
	plan, err := workspace.EditPlan(request.PlanID, request.PlanRevision, request.Edit)
	return planOutcome{summary: "Plan intent updated; canonical workspace unchanged", plan: plan, err: err}
}

func planPreview(workspace *workspacecore.Workspace, request planRequest) planOutcome {
	plan, err := workspace.PreviewPlan(request.PlanID, request.PlanRevision)
	return planOutcome{summary: "Deterministic plan preview recorded; canonical workspace unchanged", plan: plan, err: err}
}

func planInspect(workspace *workspacecore.Workspace, request planRequest) planOutcome {
	plan, err := workspace.InspectPlan(request.PlanID, request.PlanRevision)
	return planOutcome{summary: "Plan inspection loaded from durable intent", plan: plan, err: err}
}

// planPrepare stages the plan in an isolated sandbox. Inline operations
// create the plan first. A failed prepare reports the plan in its failed
// state when the workspace still knows it.
func (h *Handlers) planPrepare(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request planRequest) planOutcome {
	planID, revision := request.PlanID, request.PlanRevision
	if request.HasOperations {
		created := h.planCreate(ctx, requestID, workspace, request)
		if created.err != nil {
			return created
		}
		planID, revision = created.plan.PlanID, created.plan.PlanRevision
	}
	var plan workspacecore.PlanRecord
	stager, err := h.pool.PlanStager(workspace, planID, revision, true)
	if err == nil {
		plan, err = workspace.PreparePlan(ctx, planID, revision, stager)
	}
	if err != nil && planID != "" {
		if failed, inspectErr := workspace.InspectPlan(planID, revision); inspectErr == nil {
			plan = failed
		}
	}
	return planOutcome{summary: "Plan prepared in an isolated sandbox; canonical workspace unchanged", plan: plan, err: err}
}

// planDiscard rolls back a prepared sandbox when one exists and discards
// the plan otherwise. A failed or conflicted plan whose sandbox is already
// gone is discarded without one.
func (h *Handlers) planDiscard(ctx context.Context, workspace *workspacecore.Workspace, request planRequest) planOutcome {
	summary := "Plan discarded and isolated sandbox removed; canonical workspace unchanged"
	current, err := workspace.InspectPlan(request.PlanID, request.PlanRevision)
	if err != nil {
		return planOutcome{err: err}
	}
	switch current.State {
	case workspacecore.PlanReady, workspacecore.PlanProvisional, workspacecore.PlanFailed, workspacecore.PlanConflicted:
	default:
		plan, err := workspace.DiscardPlan(request.PlanID, request.PlanRevision)
		return planOutcome{summary: summary, plan: plan, err: err}
	}
	stager, stagerErr := h.pool.PlanStager(workspace, request.PlanID, request.PlanRevision, false)
	if stagerErr == nil {
		plan, err := workspace.RollbackPlan(ctx, request.PlanID, request.PlanRevision, stager)
		return planOutcome{summary: summary, plan: plan, err: err}
	}
	terminal := current.State == workspacecore.PlanFailed || current.State == workspacecore.PlanConflicted
	if terminal && workspacecore.ErrorCode(stagerErr) == workspacecore.CodeProviderUnavailable {
		plan, err := workspace.DiscardPlan(request.PlanID, request.PlanRevision)
		return planOutcome{summary: summary, plan: plan, err: err}
	}
	return planOutcome{err: stagerErr}
}

// planApply commits the prepared plan through the journal, recovering the
// stager after a service restart when necessary, and resyncs the canonical
// provider afterwards.
func (h *Handlers) planApply(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request planRequest) map[string]any {
	var plan workspacecore.PlanRecord
	wasProvisional := false
	if current, inspectErr := workspace.InspectPlan(request.PlanID, request.PlanRevision); inspectErr == nil {
		plan = current
		wasProvisional = current.State == workspacecore.PlanProvisional
	}
	stager, err := h.pool.PlanStager(workspace, request.PlanID, request.PlanRevision, false)
	if err != nil {
		recoveredStager, recoveredPlan, recoverable, recoveryErr := h.recoverPreparedStager(ctx, workspace, request.PreparedRevision)
		switch {
		case recoveryErr != nil:
			err = recoveryErr
		case recoverable:
			stager, plan, err = recoveredStager, recoveredPlan, nil
			wasProvisional = recoveredPlan.State == workspacecore.PlanProvisional
		}
	}
	if err == nil {
		var options []workspacecore.CommitOption
		if request.AcceptProvisional {
			options = append(options, workspacecore.AcceptProvisional("diagnostics"))
		}
		committed, commitErr := workspace.CommitPlan(ctx, request.PlanID, request.PlanRevision, request.PreparedRevision, stager, options...)
		if commitErr == nil || committed.PlanID != "" {
			plan = committed
		}
		err = commitErr
	}
	if err != nil {
		return h.planFailure(requestID, workspace, request.Action, planOutcome{plan: plan, err: err})
	}
	result := planResult(requestID, workspace, "Prepared plan applied through the durable commit journal; canonical provider resynced", plan)
	if wasProvisional {
		markAppliedFromProvisional(result, plan)
	}
	if _, resyncErr := h.pool.Resync(ctx, workspace); resyncErr != nil {
		result["outcome"] = "provisional"
		result["code"] = "provider_resync_failed"
		result["summary"] = "Prepared plan applied, but canonical provider resynchronization failed"
		result["warnings"] = append(result["warnings"].([]string), resyncErr.Error())
		result["next"] = []any{map[string]any{
			"tool": "language_server_setup", "action": "restart", "use_new_idempotency_key": true,
		}}
	}
	return result
}

func markAppliedFromProvisional(result map[string]any, plan workspacecore.PlanRecord) {
	result["outcome"] = "provisional"
	result["summary"] = "Prepared plan applied with explicitly accepted incomplete diagnostic evidence; canonical provider resynced"
	result["warnings"] = append(result["warnings"].([]string), "The plan was PROVISIONAL because diagnostic evidence was incomplete; the exact prepared revision was explicitly accepted.")
	data := result["data"].(map[string]any)
	data["applied_from_provisional"] = true
	if plan.Preparation != nil {
		data["provisional_accepted"] = mcpapi.NonNilStrings(plan.Preparation.ProvisionalAccepted)
		data["missing_coverage"] = plan.Preparation.MissingCoverage
	}
}

// planResult renders a successful plan action: the record, its transaction
// state, the verification evidence, and the outcome the state implies.
func planResult(requestID string, workspace *workspacecore.Workspace, summary string, plan workspacecore.PlanRecord) map[string]any {
	data := map[string]any{"plan": plan}
	if plan.State == workspacecore.PlanCommitted && plan.Preparation != nil {
		fromRevision := plan.Preparation.CanonicalFromRevision
		if fromRevision == "" {
			fromRevision = plan.Preparation.BaseRevision
		}
		data["from_revision"] = fromRevision
		data["revision"] = plan.Preparation.CanonicalRevision
		data["changed_paths"] = append([]string(nil), plan.Preparation.AffectedFiles...)
		data["canonical_changed"] = plan.Preparation.CanonicalChanged
	}
	result := mcpapi.Envelope(requestID, workspace, "ok", "", summary, data)
	result["transaction"] = map[string]any{"id": plan.PlanID, "state": plan.State}
	if plan.Preparation != nil {
		evidenceIDs := make([]string, 0)
		for _, stage := range plan.Preparation.Verification {
			evidenceIDs = append(evidenceIDs, stage.EvidenceIDs...)
		}
		result["evidence"] = map[string]any{"ids": evidenceIDs, "truncated": false}
	}
	if plan.State == workspacecore.PlanProvisional {
		result["outcome"] = "provisional"
		if plan.Preparation != nil {
			result["next"] = []any{map[string]any{
				"tool": "change_plan", "action": "apply", "plan_id": plan.PlanID,
				"plan_revision": plan.PlanRevision, "prepared_revision": plan.Preparation.PreparedRevision,
				"use_new_idempotency_key": true,
				"note":                    "Apply only if you explicitly accept the incomplete verification evidence for this exact prepared revision.",
			}}
		}
	}
	if plan.Preview != nil && plan.Preview.Outcome == "conflict" {
		result["outcome"] = "conflict"
		result["code"] = "plan_validation_conflicts"
		result["summary"] = fmt.Sprintf("Plan preview found %d conflicts; canonical workspace unchanged", len(plan.Preview.Conflicts))
	}
	if plan.State == workspacecore.PlanConflicted && plan.Conflict != nil {
		result["outcome"] = "conflict"
		result["code"] = plan.Conflict.Code
		result["summary"] = "Plan is CONFLICTED: " + plan.Conflict.Message
		result["next"] = planStateNext(plan)
	}
	return result
}

// planFailure renders a failed plan action with its classified code and the
// recovery the action and the plan's state allow.
func (h *Handlers) planFailure(requestID string, workspace *workspacecore.Workspace, action string, outcome planOutcome) map[string]any {
	plan, err := outcome.plan, outcome.err
	code, envelopeOutcome := classifyPlanError(err, plan)
	data := map[string]any{"action": action, "canonical_changed": false}
	if plan.PlanID != "" {
		data["plan"] = plan
		if plan.Preparation != nil {
			data["canonical_changed"] = plan.Preparation.CanonicalChanged
		}
	}
	result := mcpapi.Envelope(requestID, workspace, envelopeOutcome, code, err.Error(), data)
	if code == workspacecore.CodeProvisionalNotAccepted && plan.PlanID != "" {
		gaps := []workspacecore.VerificationGap{}
		if plan.Preparation != nil {
			gaps = plan.Preparation.MissingCoverage
		}
		data["missing_coverage"] = gaps
	}
	if code == workspacecore.CodePlanStateInvalid && plan.PlanID != "" {
		data["state"] = plan.State
	}
	if next := planFailureNext(action, code, plan); next != nil {
		result["next"] = next
	}
	return result
}

// planFailureNext picks the follow-up for a failed action from the code and
// the plan's state; nil leaves the envelope without one.
func planFailureNext(action, code string, plan workspacecore.PlanRecord) []any {
	withPlan := func(next string, extra map[string]any) map[string]any {
		item := map[string]any{"tool": "change_plan", "action": next, "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision}
		for key, value := range extra {
			item[key] = value
		}
		return item
	}
	switch {
	case code == workspacecore.CodeProvisionalNotAccepted && plan.PlanID != "":
		return []any{
			withPlan("apply", map[string]any{
				"prepared_revision": preparedRevisionOf(plan), "accept_provisional": true, "use_new_idempotency_key": true,
				"note": "Apply only if you explicitly accept the incomplete verification evidence for this exact prepared revision.",
			}),
			withPlan("discard", nil),
		}
	case code == workspacecore.CodePlanStateInvalid && plan.PlanID != "":
		return planStateNext(plan)
	case action == "apply" && plan.PlanID != "" && code != "workspace_epoch_changed":
		return []any{withPlan("prepare", map[string]any{"use_new_idempotency_key": true}), withPlan("discard", nil)}
	case action == "prepare" && plan.PlanID != "":
		return []any{withPlan("inspect", nil), withPlan("discard", nil)}
	case code == "undeclared_tool_write":
		return []any{map[string]any{
			"action": "update_pipeline_command", "scope": ".huyang.toml",
			"note": "Declare the reported cache/output path in the command's writes list, or configure that tool to disable or redirect its cache, then prepare again with a new idempotency key.",
		}}
	case action == "apply" && code == "workspace_epoch_changed" && plan.PlanID != "":
		return []any{withPlan("inspect", nil), withPlan("discard", nil)}
	}
	return nil
}

// classifyPlanError maps a workspace lifecycle error onto the tool outcome and
// stable code using the typed error code rather than the message text.
func classifyPlanError(err error, plan workspacecore.PlanRecord) (string, string) {
	switch code := workspacecore.ErrorCode(err); code {
	case workspacecore.CodePlanRevisionChanged, workspacecore.CodeWorkspaceBusy, workspacecore.CodePlanValidationConflicts,
		workspacecore.CodeWorkspaceEpochChanged, workspacecore.CodeProvisionalNotAccepted, workspacecore.CodePlanStateInvalid:
		return code, "conflict"
	case workspacecore.CodeCommitPreconditionChanged, workspacecore.CodePreparedRevisionChanged:
		return workspacecore.CodeCommitPreconditionChanged, "conflict"
	case workspacecore.CodeCommitRecoveryRequired:
		return code, "failed"
	case workspacecore.CodeProviderUnavailable:
		return "provider_prepare_failed", "failed"
	}
	switch {
	case plan.State == workspacecore.PlanRecoveryRequired:
		return workspacecore.CodeCommitRecoveryRequired, "failed"
	case strings.Contains(err.Error(), "undeclared_tool_write"):
		return "undeclared_tool_write", "failed"
	}
	return "plan_action_failed", "failed"
}

func preparedRevisionOf(plan workspacecore.PlanRecord) string {
	if plan.Preparation == nil {
		return ""
	}
	return plan.Preparation.PreparedRevision
}

// planStateNext lists the valid follow-up actions for a plan in its current
// state so an invalid transition answers with what is possible next.
func planStateNext(plan workspacecore.PlanRecord) []any {
	base := map[string]any{"tool": "change_plan", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision}
	with := func(action string, extra map[string]any) map[string]any {
		item := mcpapi.CloneEnvelope(base)
		item["action"] = action
		for key, value := range extra {
			item[key] = value
		}
		return item
	}
	switch plan.State {
	case workspacecore.PlanReady:
		return []any{with("apply", map[string]any{"prepared_revision": preparedRevisionOf(plan), "use_new_idempotency_key": true}), with("discard", nil)}
	case workspacecore.PlanProvisional:
		return []any{with("apply", map[string]any{"prepared_revision": preparedRevisionOf(plan), "accept_provisional": true, "use_new_idempotency_key": true}), with("discard", nil)}
	case workspacecore.PlanConflicted, workspacecore.PlanFailed:
		return []any{with("inspect", nil), with("discard", nil)}
	case workspacecore.PlanOpen, workspacecore.PlanPreviewed:
		return []any{with("prepare", map[string]any{"use_new_idempotency_key": true}), with("discard", nil)}
	case workspacecore.PlanRecoveryRequired:
		return []any{with("inspect", nil)}
	default:
		return []any{with("inspect", nil)}
	}
}
