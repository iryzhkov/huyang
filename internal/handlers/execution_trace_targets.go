package handlers

import (
	"context"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *Handlers) installTraceTargets(ctx context.Context, requestID string, w *workspacecore.Workspace, id string, targets []any) error {
	if len(targets) == 0 {
		return nil
	}
	backend, err := h.pool.Debug(ctx, w)
	if err != nil {
		return workspacecore.Codedf("trace_provider_unavailable", "debugger provider unavailable")
	}
	installed := []workspacecore.ExecutionTraceTarget{}
	for _, raw := range targets {
		item, _ := raw.(map[string]any)
		target, _ := item["target"].(map[string]any)
		args, err := h.debugTargetArguments(w, target, item)
		if err != nil {
			return workspacecore.Codedf("trace_target_invalid", "trace target could not be resolved")
		}
		args["trace_owner"] = id
		value, err := callModernDebugProvider(ctx, requestID, w, backend, "debug_breakpoint", args)
		if err != nil {
			return workspacecore.Codedf("trace_target_unavailable", "temporary breakpoint could not be installed; existing breakpoints are preserved")
		}
		data, _ := value.(map[string]any)
		file, _ := data["file"].(string)
		path := relativeTracePath(w, file)
		line := argInt(data, "line", 0)
		if path == "" || line < 1 {
			return workspacecore.Codedf("trace_target_invalid", "debugger target is outside captured source")
		}
		installed = append(installed, workspacecore.ExecutionTraceTarget{Path: path, Line: line})
		if err = w.SetExecutionTraceTargets(installed); err != nil {
			return err
		}
	}
	return nil
}
func (h *Handlers) cleanupTraceTargets(w *workspacecore.Workspace, id string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backend, err := h.pool.Debug(ctx, w)
	if err != nil {
		return false
	}
	value, err := callModernDebugProvider(ctx, "trace_cleanup", w, backend, "debug_breakpoints", map[string]any{"clear_trace": true, "trace_owner": id})
	if err != nil {
		return false
	}
	data, _ := value.(map[string]any)
	return argInt(data, "changed", 0) == 0
}
func (h *Handlers) verifyTraceTargets(ctx context.Context, w *workspacecore.Workspace, id string) bool {
	trace, err := w.ExecutionTrace(id)
	if err != nil {
		return false
	}
	if len(trace.Targets) == 0 {
		return true
	}
	backend, err := h.pool.Debug(ctx, w)
	if err != nil {
		return false
	}
	value, err := callModernDebugProvider(ctx, "trace_targets", w, backend, "debug_breakpoints", map[string]any{})
	if err != nil {
		return false
	}
	data, _ := value.(map[string]any)
	entries, _ := data["breakpoints"].([]any)
	for i := range trace.Targets {
		for _, raw := range entries {
			item, _ := raw.(map[string]any)
			file, _ := item["file"].(string)
			if relativeTracePath(w, file) == trace.Targets[i].Path && argInt(item, "line", 0) == trace.Targets[i].Line && item["verified"] == true {
				trace.Targets[i].Verified = true
			}
		}
	}
	if w.SetExecutionTraceTargets(trace.Targets) != nil {
		return false
	}
	for _, target := range trace.Targets {
		if !target.Verified {
			return false
		}
	}
	return true
}
