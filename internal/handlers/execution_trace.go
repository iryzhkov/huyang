package handlers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *Handlers) debug(ctx context.Context, requestID, name string, w *workspacecore.Workspace, args map[string]any) map[string]any {
	action, _ := args["action"].(string)
	if name == "debug_inspect" && action == "compare_traces" {
		return h.compareTraces(ctx, requestID, w, args)
	}
	if name == "debug_inspect" && action == "value_origin" {
		return h.traceValueOrigin(requestID, w, args)
	}
	if name == "debug_inspect" && action == "trace" {
		id, _ := args["trace_id"].(string)
		trace, err := w.ExecutionTrace(id)
		if err != nil {
			return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
		}
		result := mcpapi.Envelope(requestID, w, "partial", "one_execution", "Bounded observations from one execution; absence is not unreachability", map[string]any{"trace": trace})
		result["api_version"] = mcpapi.APIVersionExperimental
		return result
	}
	active := w.ActiveExecutionTrace()
	if name == "debug_breakpoints" && action == "watch" {
		if active == "" {
			return mcpapi.Failure(requestID, w, "trace_incomplete", workspacecore.Codedf("trace_incomplete", "watch requires an active value-enabled trace"))
		}
		args = cloneDebugMap(args)
		args["trace_owner"] = active
	}
	if name == "debug_session" && (action == "start" || action == "attach" || action == "restart") && active != "" {
		return mcpapi.Failure(requestID, w, "trace_active", workspacecore.Codedf("trace_active", "stop the traced session before starting or restarting"))
	}
	if name == "debug_session" && args["trace_policy"] != nil {
		id, err := h.beginDebugTrace(ctx, requestID, w, args)
		if err != nil {
			return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
		}
		active = id
		args = cloneDebugMap(args)
		args["trace_enabled"] = true
		policy, _ := args["trace_policy"].(map[string]any)
		args["trace_values"] = policy["capture_values"] == true
	}
	result := h.debugWithoutTrace(ctx, requestID, name, w, args)
	if active != "" {
		h.recordDebugTrace(ctx, requestID, w, name, action, active, result)
	}
	return result
}
func cloneDebugMap(args map[string]any) map[string]any {
	copied := map[string]any{}
	for key, value := range args {
		copied[key] = value
	}
	return copied
}
func (h *Handlers) beginDebugTrace(ctx context.Context, requestID string, w *workspacecore.Workspace, args map[string]any) (string, error) {
	encoded, err := json.Marshal(args["trace_policy"])
	var policy workspacecore.ExecutionTracePolicy
	if err != nil || json.Unmarshal(encoded, &policy) != nil {
		return "", workspacecore.Codedf("trace_policy_invalid", "invalid trace policy")
	}
	policy, err = workspacecore.NormalizeExecutionTracePolicy(policy)
	if err != nil {
		return "", err
	}
	raw, _ := args["trace_policy"].(map[string]any)
	targets := mcpapi.AnySlice(raw["targets"])
	if policy.Mode != "stops" && len(targets) == 0 {
		return "", workspacecore.Codedf("trace_policy_invalid", "path/conditions tracing requires selected source targets")
	}
	if len(targets) > workspacecore.MaxExecutionTraceTargets {
		return "", workspacecore.Codedf("trace_policy_invalid", "at most 128 trace targets")
	}
	captureCtx, cancel := context.WithTimeout(ctx, time.Duration(workspacecore.MaxExecutionAnalysisMillis)*time.Millisecond)
	defer cancel()
	sources, revision, coverage, err := w.ExecutionSources(captureCtx)
	if err != nil {
		return "", err
	}
	hashes := map[string]string{}
	for _, source := range sources {
		if len(hashes) >= workspacecore.MaxExecutionNodes {
			coverage.Capped = true
			break
		}
		hashes[source.Path] = source.Hash
	}
	launch := traceLaunchIdentity(captureCtx, w, args)
	id, err := w.BeginExecutionTrace(workspacecore.ExecutionTrace{Revision: revision, Policy: policy, Launch: launch, SourceHashes: hashes, Targets: []workspacecore.ExecutionTraceTarget{}})
	if err != nil {
		return "", err
	}
	if err = h.installTraceTargets(captureCtx, requestID, w, id, targets); err != nil {
		h.cleanupTraceTargets(w, id)
		_, _ = w.FinishExecutionTrace("trace_setup_failed")
		return "", err
	}
	gaps := []string{"executable_identity_unverified"}
	if !coverage.Complete || coverage.Capped {
		gaps = append(gaps, "source_inventory_incomplete")
	}
	if err = w.AppendExecutionTrace(workspacecore.ExecutionTraceEvent{Kind: "capture_started", Frames: []workspacecore.ExecutionTraceFrame{}}, gaps...); err != nil {
		h.cleanupTraceTargets(w, id)
		_, _ = w.FinishExecutionTrace("trace_storage_failed")
		return "", err
	}
	return id, nil
}
func relativeTracePath(w *workspacecore.Workspace, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.Identity().Root, path)
	}
	relative, err := filepath.Rel(w.Identity().Root, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return ""
	}
	return filepath.ToSlash(relative)
}
