package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const modernDebugTimeout = 5 * time.Minute

func (h *Handlers) debugWithoutTrace(ctx context.Context, requestID, name string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	action, _ := arguments["action"].(string)
	if name == "debug_inspect" && action == "evaluate" {
		policy, _ := arguments["policy"].(string)
		if policy == "" {
			policy = "read_only"
		}
		if policy != "allow_side_effects" {
			result := mcpapi.Envelope(requestID, workspace, "unavailable", "approval_required",
				"Evaluation was not executed because the debugger cannot enforce read-only expressions",
				map[string]any{"debug": map[string]any{"action": action, "executed": false, "policy": "read_only"},
					"coverage": map[string]any{"complete": false, "unavailable": []string{"enforceable_read_only_evaluation"}}})
			result["next"] = []any{map[string]any{"tool": "debug_inspect", "arguments": map[string]any{
				"workspace_id": string(workspace.Identity().ID), "action": "evaluate",
				"expression": arguments["expression"], "policy": "allow_side_effects",
			}}}
			return result
		}
	}
	backend, release, err := h.pool.Debug(ctx, workspace)
	defer release()
	if err != nil {
		result := debugUnavailable(requestID, workspace, "debug_provider_unavailable", err)
		result["next"] = debugFailureNext(name, action)
		return result
	}
	operation, providerArguments, err := h.debugOperation(workspace, name, action, arguments)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "debug_target_invalid", err)
	}
	if initial := mcpapi.AnySlice(arguments["initial_breakpoints"]); name == "debug_session" && (action == "start" || action == "attach") {
		for _, item := range initial {
			breakpoint, _ := item.(map[string]any)
			target, _ := breakpoint["target"].(map[string]any)
			resolved, resolveErr := h.debugTargetArguments(workspace, target, breakpoint)
			if resolveErr != nil {
				return mcpapi.Failure(requestID, workspace, "debug_target_invalid", resolveErr)
			}
			for _, key := range []string{"condition", "hit_condition", "log_message"} {
				if value, ok := breakpoint[key]; ok {
					resolved[key] = value
				}
			}
			if _, callErr := callModernDebugProvider(ctx, requestID, workspace, backend, "debug_breakpoint", resolved); callErr != nil {
				result := debugProviderFailure(requestID, workspace, callErr)
				result["next"] = debugFailureNext(name, action)
				return result
			}
		}
	}
	value, err := callModernDebugProvider(ctx, requestID, workspace, backend, operation, providerArguments)
	if err != nil {
		result := debugProviderFailure(requestID, workspace, err)
		result["next"] = debugFailureNext(name, action)
		return result
	}
	data, complete := enrichDebugResult(workspace, value)
	outcome := "ok"
	if !complete {
		outcome = "partial"
	}
	if name == "debug_inspect" && action == "evaluate" {
		data["evaluation"] = map[string]any{"executed": true, "policy": "allow_side_effects", "debuggee_state_may_have_changed": true}
	}
	summary := debugSummary(action, data)
	result := mcpapi.Envelope(requestID, workspace, outcome, "", summary, map[string]any{"debug": data, "coverage": debugCoverage(complete)})
	result["next"] = debugNext(workspace, name, action, data)
	return result
}

func callModernDebugProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, backend provider.Provider, operation string, arguments map[string]any) (any, error) {
	transactionID, _ := arguments["transaction_id"].(string)
	result, err := providerpool.Call(ctx, workspace, backend, providerpool.CallSpec{
		RequestID: requestID, TransactionID: transactionID, Timeout: modernDebugTimeout,
	}, operation, arguments)
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

func (h *Handlers) debugOperation(workspace *workspacecore.Workspace, name, action string, arguments map[string]any) (string, map[string]any, error) {
	copied := copyDebugArguments(arguments)
	switch name {
	case "debug_session":
		switch action {
		case "start":
			return "debug_launch", copied, nil
		case "attach":
			return "debug_attach", copied, nil
		case "restart":
			copied["again"] = true
			return "debug_launch", copied, nil
		case "stop":
			return "debug_stop", copied, nil
		}
	case "debug_breakpoints":
		switch action {
		case "watch":
			copied["watch_set"] = true
			return "debug_threads", copied, nil
		case "list":
			return "debug_breakpoints", copied, nil
		case "clear":
			copied["clear"] = true
			return "debug_breakpoints", copied, nil
		case "set", "remove":
			target, _ := arguments["target"].(map[string]any)
			resolved, err := h.debugTargetArguments(workspace, target, arguments)
			if err != nil {
				return "", nil, err
			}
			for _, key := range []string{"condition", "hit_condition", "log_message"} {
				if value, ok := arguments[key]; ok {
					resolved[key] = value
				}
			}
			if action == "remove" {
				resolved["remove"] = true
			}
			return "debug_breakpoint", resolved, nil
		}
	case "debug_control":
		switch action {
		case "continue":
			return "debug_continue", copied, nil
		case "pause":
			copied["wait_ms"] = 0
			copied["pause_after"] = true
			return "debug_wait", copied, nil
		case "step_over", "step_into", "step_out":
			copied["action"] = strings.TrimPrefix(action, "step_")
			return "debug_step", copied, nil
		case "run_to":
			target, _ := arguments["target"].(map[string]any)
			resolved, err := h.debugTargetArguments(workspace, target, arguments)
			if err != nil {
				return "", nil, err
			}
			copied["to"] = resolved
			return "debug_continue", copied, nil
		}
	case "debug_inspect":
		switch action {
		case "mutation_capabilities":
			copied["mutation_capabilities"] = true
			return "debug_threads", copied, nil
		case "threads":
			return "debug_threads", copied, nil
		case "stack":
			return "debug_stack", copied, nil
		case "scopes":
			return "debug_scopes", copied, nil
		case "variables":
			return "debug_variables", copied, nil
		case "evaluate":
			return "debug_evaluate", copied, nil
		}
	}
	return "", nil, fmt.Errorf("unsupported %s action %q", name, action)
}

func copyDebugArguments(arguments map[string]any) map[string]any {
	copied := make(map[string]any, len(arguments))
	for key, value := range arguments {
		switch key {
		case "workspace_id", "idempotency_key", "transaction_id", "initial_breakpoints", "target", "line_offset", "policy", "trace_policy":
		default:
			copied[key] = value
		}
	}
	delete(copied, "action")
	return copied
}

func (h *Handlers) debugTargetArguments(workspace *workspacecore.Workspace, target map[string]any, arguments map[string]any) (map[string]any, error) {
	if target == nil {
		return nil, errors.New("missing source target")
	}
	if locator, ok := target["symbol_locator"].(map[string]any); ok {
		result := map[string]any{"file": locator["path"], "name_path": locator["name_path"]}
		if offset := uintArgument(arguments["line_offset"]); offset > 0 {
			result["offset"] = offset
		}
		return result, nil
	}
	var handle workspacecore.RangeHandle
	if id, ok := target["handle"].(string); ok {
		resolution, err := workspace.ResolveHandle(workspacecore.HandleID(id))
		if err != nil {
			return nil, err
		}
		handle, err = resolution.RangeHandle()
		if err != nil {
			return nil, err
		}
	} else if value, ok := target["file_range"].(map[string]any); ok {
		decoded, err := decodeRangeHandle(value)
		if err != nil {
			return nil, err
		}
		record, err := workspace.RegisterRangeHandle(decoded, workspacecore.HandleRange, decoded.Path+" debugger target")
		if err != nil {
			return nil, err
		}
		resolution, err := workspace.ResolveHandle(record.Handle)
		if err != nil {
			return nil, err
		}
		handle, err = resolution.RangeHandle()
		if err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("target must contain handle, file_range, or symbol_locator")
	}
	read, err := workspace.Read(handle.Path)
	if err != nil {
		return nil, err
	}
	if handle.ByteStart < 0 || handle.ByteStart > len(read.Content) {
		return nil, errors.New("debug target byte offset is outside the document")
	}
	line := bytes.Count(read.Content[:handle.ByteStart], []byte{'\n'}) + 1
	return map[string]any{"file": handle.Path, "line": line}, nil
}

func debugFailureNext(tool, action string) []any {
	retry := map[string]any{"tool": tool, "action": action}
	if tool != "debug_inspect" {
		retry["use_new_idempotency_key"] = true
	}
	return []any{
		map[string]any{"tool": "language_server_status", "action": "inspect_language_and_debugger_availability"},
		retry,
	}
}

func debugProviderFailure(requestID string, workspace *workspacecore.Workspace, err error) map[string]any {
	switch provider.ErrorCode(err) {
	case "workspace_busy", "provider_cancelled":
		return modernProviderFailure(requestID, workspace, "debugger_failed", err)
	case "dap_runtime_unavailable":
		// The kernel found no nvim-dap on the host Neovim; its Detail names
		// the locations searched and the override variable.
		return debugUnavailable(requestID, workspace, "debugger_unavailable", err)
	}
	var failure *provider.Failure
	if errors.As(err, &failure) {
		if failure.Code == provider.FailureCancelled || failure.Code == provider.FailureDeadline {
			return mcpapi.Failure(requestID, workspace, string(failure.Code), err)
		}
		return debugUnavailable(requestID, workspace, string(failure.Code), err)
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "not installed") || strings.Contains(lower, "no debug adapter") ||
		strings.Contains(lower, "could not start") || strings.Contains(lower, "did not initialize") ||
		strings.Contains(lower, "couldn't connect") || strings.Contains(lower, "executable") ||
		strings.Contains(lower, "source map") || strings.Contains(lower, "runtime") {
		return debugUnavailable(requestID, workspace, "debugger_unavailable", err)
	}
	return mcpapi.Failure(requestID, workspace, "debugger_failed", err)
}

func debugUnavailable(requestID string, workspace *workspacecore.Workspace, code string, err error) map[string]any {
	detail := strings.ReplaceAll(err.Error(), "agent99", "Huyang")
	summary := "Debugger unavailable; install or configure the requested DAP adapter and language runtime, then retry"
	repair := map[string]any{
		"action": "install_or_configure_dap_adapter", "detail": detail,
		"supported_adapters": []string{"delve", "debugpy", "codelldb", "lldb-dap", "gdb", "js-debug", "java"},
	}
	var operation *provider.ProviderError
	if errors.As(err, &operation) && operation.Detail != "" {
		// The structured detail of a kernel error, for dap_runtime_unavailable
		// the locations searched for nvim-dap and the override variable.
		repair["searched"] = operation.Detail
	}
	result := mcpapi.Envelope(requestID, workspace, "unavailable", code, summary, map[string]any{
		"coverage": map[string]any{"complete": false, "unavailable": []string{"debug_adapter_or_runtime"}},
		"repair":   repair,
	})
	result["warnings"] = []string{detail}
	result["next"] = []any{
		map[string]any{"tool": "workspace_inspect", "action": "inspect_provider_status", "view": "status"},
		map[string]any{"tool": "debug_session", "action": "retry_after_adapter_install"},
	}
	return result
}

func enrichDebugResult(workspace *workspacecore.Workspace, value any) (map[string]any, bool) {
	raw, ok := value.(map[string]any)
	if !ok {
		return map[string]any{"result": value}, true
	}
	data := make(map[string]any, len(raw)+2)
	for key, item := range raw {
		data[key] = item
	}
	complete := true
	if frame, ok := data["frame"].(map[string]any); ok {
		if target, err := debugSourceTarget(workspace, frame); err == nil {
			data["top_location"] = target
		} else {
			complete = false
			data["source_mapping"] = map[string]any{"available": false, "reason": err.Error()}
		}
	}
	if file, ok := data["file"].(string); ok {
		location := map[string]any{"file": file, "line": data["line"]}
		if target, err := debugSourceTarget(workspace, location); err == nil {
			data["source_target"] = target
		} else {
			complete = false
		}
	}
	if locations, ok := data["locations"].([]any); ok {
		enriched := make([]any, 0, len(locations))
		for _, item := range locations {
			location, _ := item.(map[string]any)
			entry := map[string]any{"frame": location}
			if target, err := debugSourceTarget(workspace, location); err == nil {
				entry["target"] = target
			} else {
				entry["source_mapping"] = map[string]any{"available": false, "reason": err.Error()}
				complete = false
			}
			enriched = append(enriched, entry)
		}
		data["locations"] = enriched
	}
	if _, stopped := data["reason"]; stopped {
		data["changes_since_previous_stop"] = map[string]any{
			"output": data["output_new"], "tracked": data["tracked"], "stale_source": data["stale_source"],
		}
	}
	return data, complete
}

func debugSourceTarget(workspace *workspacecore.Workspace, location map[string]any) (map[string]any, error) {
	file := fmt.Sprint(location["file"])
	line := int(uintArgument(location["line"]))
	if file == "" || line < 1 {
		return nil, errors.New("debugger returned no source file or line")
	}
	read, err := workspace.Read(file)
	if err != nil {
		return nil, err
	}
	start, end, err := lineByteRange(read.Content, line)
	if err != nil {
		return nil, err
	}
	handle, err := workspace.NewRange(read.Path, start, end)
	if err != nil {
		return nil, err
	}
	record, err := workspace.RegisterRangeHandle(handle, workspacecore.HandleRange, fmt.Sprintf("%s:%d debugger source", read.Path, line))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"handle": record.Handle, "display": record.Display,
		"locator": map[string]any{"file_range": handle},
	}, nil
}

func debugCoverage(complete bool) map[string]any {
	if complete {
		return map[string]any{"complete": true, "unavailable": []string{}}
	}
	return map[string]any{"complete": false, "unavailable": []string{"source_mapping"}}
}

func debugSummary(action string, data map[string]any) string {
	if state, ok := data["state"].(string); ok {
		if reason, _ := data["reason"].(string); reason != "" {
			return fmt.Sprintf("Debugger %s: %s (%s)", action, state, reason)
		}
		return fmt.Sprintf("Debugger %s: %s", action, state)
	}
	return fmt.Sprintf("Debugger %s completed", action)
}

func debugNext(workspace *workspacecore.Workspace, name, action string, data map[string]any) []any {
	id := string(workspace.Identity().ID)
	state, _ := data["state"].(string)
	if state == "stopped" || data["reason"] != nil {
		return []any{
			map[string]any{"tool": "debug_control", "arguments": map[string]any{"workspace_id": id, "action": "continue"}},
			map[string]any{"tool": "debug_inspect", "arguments": map[string]any{"workspace_id": id, "action": "variables"}},
		}
	}
	if state == "running" {
		return []any{map[string]any{"tool": "debug_control", "arguments": map[string]any{"workspace_id": id, "action": "pause"}}}
	}
	if name != "debug_session" || action != "stop" {
		return []any{map[string]any{"tool": "debug_session", "arguments": map[string]any{"workspace_id": id, "action": "stop"}}}
	}
	return []any{}
}
