package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *toolHandlers) changePlan(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	action, _ := arguments["action"].(string)
	decode := func(value any, target any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	}
	planResult := func(summary string, plan workspacecore.PlanRecord) map[string]any {
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
	var plan workspacecore.PlanRecord
	var err error
	switch action {
	case "create":
		var operations []workspacecore.PlanOperation
		if err = decode(arguments["operations"], &operations); err == nil {
			err = h.resolvePlanSymbolLocators(ctx, requestID, workspace, operations)
		}
		if err == nil {
			plan, err = workspace.CreatePlan(operations)
		}
		if err == nil {
			return planResult("Plan intent created; canonical workspace unchanged", plan)
		}
	case "edit":
		var edit workspacecore.PlanEdit
		if err = decode(arguments["edit"], &edit); err == nil {
			err = h.resolvePlanSymbolLocators(ctx, requestID, workspace, edit.Operations)
		}
		if err == nil {
			plan, err = workspace.EditPlan(
				fmt.Sprint(arguments["plan_id"]), uintArgument(arguments["plan_revision"]), edit,
			)
		}
		if err == nil {
			return planResult("Plan intent updated; canonical workspace unchanged", plan)
		}
	case "preview":
		plan, err = workspace.PreviewPlan(
			fmt.Sprint(arguments["plan_id"]), uintArgument(arguments["plan_revision"]),
		)
		if err == nil {
			return planResult("Deterministic plan preview recorded; canonical workspace unchanged", plan)
		}
	case "inspect":
		plan, err = workspace.InspectPlan(
			fmt.Sprint(arguments["plan_id"]), uintArgument(arguments["plan_revision"]),
		)
		if err == nil {
			return planResult("Plan inspection loaded from durable intent", plan)
		}
	case "prepare":
		planID := fmt.Sprint(arguments["plan_id"])
		revision := uintArgument(arguments["plan_revision"])
		if raw, inline := arguments["operations"]; inline {
			var operations []workspacecore.PlanOperation
			if err = decode(raw, &operations); err == nil {
				err = h.resolvePlanSymbolLocators(ctx, requestID, workspace, operations)
			}
			if err == nil {
				plan, err = workspace.CreatePlan(operations)
				if err == nil {
					planID, revision = plan.PlanID, plan.PlanRevision
				}
			}
		}
		var stager workspacecore.PlanStager
		if err == nil {
			stager, err = h.pool.planStager(workspace, planID, revision, true)
		}
		if err == nil {
			plan, err = workspace.PreparePlan(ctx, planID, revision, stager)
		}
		if err != nil && planID != "" {
			if failed, inspectErr := workspace.InspectPlan(planID, revision); inspectErr == nil {
				plan = failed
			}
		}
		if err == nil {
			return planResult("Plan prepared in an isolated sandbox; canonical workspace unchanged", plan)
		}
	case "discard":
		planID := fmt.Sprint(arguments["plan_id"])
		revision := uintArgument(arguments["plan_revision"])
		current, inspectErr := workspace.InspectPlan(planID, revision)
		if inspectErr != nil {
			err = inspectErr
			break
		}
		switch current.State {
		case workspacecore.PlanReady, workspacecore.PlanProvisional, workspacecore.PlanFailed, workspacecore.PlanConflicted:
			stager, stagerErr := h.pool.planStager(workspace, planID, revision, false)
			if stagerErr != nil {
				if (current.State == workspacecore.PlanFailed || current.State == workspacecore.PlanConflicted) && workspacecore.ErrorCode(stagerErr) == workspacecore.CodeProviderUnavailable {
					plan, err = workspace.DiscardPlan(planID, revision)
				} else {
					err = stagerErr
				}
			} else {
				plan, err = workspace.RollbackPlan(ctx, planID, revision, stager)
			}
		default:
			plan, err = workspace.DiscardPlan(planID, revision)
		}
		if err == nil {
			return planResult("Plan discarded and isolated sandbox removed; canonical workspace unchanged", plan)
		}
	case "apply":
		planID := fmt.Sprint(arguments["plan_id"])
		revision := uintArgument(arguments["plan_revision"])
		preparedRevision := fmt.Sprint(arguments["prepared_revision"])
		wasProvisional := false
		if current, inspectErr := workspace.InspectPlan(planID, revision); inspectErr == nil {
			plan = current
			wasProvisional = current.State == workspacecore.PlanProvisional
		}
		var stager workspacecore.PlanStager
		stager, err = h.pool.planStager(workspace, planID, revision, false)
		if err != nil {
			recoveredStager, recoveredPlan, recoverable, recoveryErr := h.recoverPreparedStager(ctx, workspace, preparedRevision)
			switch {
			case recoveryErr != nil:
				err = recoveryErr
			case recoverable:
				stager, plan, err = recoveredStager, recoveredPlan, nil
				wasProvisional = recoveredPlan.State == workspacecore.PlanProvisional
			}
		}
		acceptProvisional, _ := arguments["accept_provisional"].(bool)
		if err == nil {
			var options []workspacecore.CommitOption
			if acceptProvisional {
				options = append(options, workspacecore.AcceptProvisional("diagnostics"))
			}
			committed, commitErr := workspace.CommitPlan(ctx, planID, revision, preparedRevision, stager, options...)
			if commitErr == nil || committed.PlanID != "" {
				plan = committed
			}
			err = commitErr
		}
		if err == nil {
			result := planResult("Prepared plan applied through the durable commit journal; canonical provider resynced", plan)
			if wasProvisional {
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
			if _, resyncErr := h.pool.resync(ctx, workspace); resyncErr != nil {
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
	default:
		err = fmt.Errorf("unknown change_plan action %q", action)
	}
	if err != nil {
		code, outcome := classifyPlanError(err, plan)
		data := map[string]any{"action": action, "canonical_changed": false}
		if plan.PlanID != "" {
			data["plan"] = plan
			if plan.Preparation != nil {
				data["canonical_changed"] = plan.Preparation.CanonicalChanged
			}
		}
		result := mcpapi.Envelope(requestID, workspace, outcome, code, err.Error(), data)
		if code == workspacecore.CodeProvisionalNotAccepted && plan.PlanID != "" {
			gaps := []workspacecore.VerificationGap{}
			if plan.Preparation != nil {
				gaps = plan.Preparation.MissingCoverage
			}
			data["missing_coverage"] = gaps
			result["next"] = []any{
				map[string]any{
					"tool": "change_plan", "action": "apply", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision,
					"prepared_revision": preparedRevisionOf(plan), "accept_provisional": true, "use_new_idempotency_key": true,
					"note": "Apply only if you explicitly accept the incomplete verification evidence for this exact prepared revision.",
				},
				map[string]any{"tool": "change_plan", "action": "discard", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision},
			}
		} else if code == workspacecore.CodePlanStateInvalid && plan.PlanID != "" {
			data["state"] = plan.State
			result["next"] = planStateNext(plan)
		} else if action == "apply" && plan.PlanID != "" && code != "workspace_epoch_changed" {
			result["next"] = []any{
				map[string]any{"tool": "change_plan", "action": "prepare", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision, "use_new_idempotency_key": true},
				map[string]any{"tool": "change_plan", "action": "discard", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision},
			}
		} else if action == "prepare" && plan.PlanID != "" {
			result["next"] = []any{
				map[string]any{"tool": "change_plan", "action": "inspect", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision},
				map[string]any{"tool": "change_plan", "action": "discard", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision},
			}
		} else if code == "undeclared_tool_write" {
			result["next"] = []any{map[string]any{
				"action": "update_pipeline_command", "scope": ".huyang.toml",
				"note": "Declare the reported cache/output path in the command's writes list, or configure that tool to disable or redirect its cache, then prepare again with a new idempotency key.",
			}}
		} else if action == "apply" && code == "workspace_epoch_changed" && plan.PlanID != "" {
			result["next"] = []any{
				map[string]any{"tool": "change_plan", "action": "inspect", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision},
				map[string]any{"tool": "change_plan", "action": "discard", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision},
			}
		}
		return result
	}
	panic("unreachable")
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
