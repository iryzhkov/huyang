package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const modernDebugTimeout = 5 * time.Minute

func debugActionSchema(properties map[string]any, required []string, actions ...string) map[string]any {
	names := append([]string(nil), actions...)
	sort.Strings(names)
	properties["action"] = enumSchema(names...)
	return schemaObject(properties, required...)
}

func debugStatefulProperties() map[string]any {
	return map[string]any{
		"workspace_id":    map[string]any{"type": "string"},
		"idempotency_key": map[string]any{"type": "string"},
		"transaction_id":  map[string]any{"type": "string"},
	}
}

func debugReadProperties() map[string]any {
	return map[string]any{
		"workspace_id":   map[string]any{"type": "string"},
		"transaction_id": map[string]any{"type": "string"},
	}
}

func modernDebugTargetSchema() map[string]any {
	schema := schemaObject(map[string]any{
		"handle": map[string]any{"type": "string"},
		"file_range": schemaObject(map[string]any{
			"path":            map[string]any{"type": "string"},
			"revision_id":     map[string]any{"type": "string"},
			"byte_start":      map[string]any{"type": "integer", "minimum": 0},
			"byte_end":        map[string]any{"type": "integer", "minimum": 0},
			"expected_sha256": map[string]any{"type": "string"},
			"before_sha256":   map[string]any{"type": "string"},
			"after_sha256":    map[string]any{"type": "string"},
			"anchor_bytes":    map[string]any{"type": "integer", "minimum": 0},
		}, "path", "revision_id", "byte_start", "byte_end", "expected_sha256", "before_sha256", "after_sha256", "anchor_bytes"),
		"symbol_locator": schemaObject(map[string]any{
			"path":      map[string]any{"type": "string"},
			"name_path": map[string]any{"type": "string"},
		}, "path", "name_path"),
	})
	schema["oneOf"] = []any{
		map[string]any{"type": "object", "additionalProperties": true, "required": []string{"handle"}},
		map[string]any{"type": "object", "additionalProperties": true, "required": []string{"file_range"}},
		map[string]any{"type": "object", "additionalProperties": true, "required": []string{"symbol_locator"}},
	}
	return schema
}

func debugTargetOptions() map[string]any {
	return map[string]any{
		"target":        modernDebugTargetSchema(),
		"line_offset":   map[string]any{"type": "integer", "minimum": 1},
		"condition":     map[string]any{"type": "string"},
		"hit_condition": map[string]any{"type": "string"},
		"log_message":   map[string]any{"type": "string"},
	}
}

func modernDebugSessionTool(profiles []mcpProfile) modernTool {
	properties := debugStatefulProperties()
	for key, value := range map[string]any{
		"file":          map[string]any{"type": "string"},
		"program":       map[string]any{"type": "string"},
		"args":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"cwd":           map[string]any{"type": "string"},
		"env":           map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"config":        map[string]any{"type": "string"},
		"adapter":       enumSchema("delve", "debugpy", "codelldb", "lldb-dap", "gdb", "js-debug", "java"),
		"stop_on_entry": map[string]any{"type": "boolean"},
		"variables":     enumSchema("summary", "names", "none"),
		"track":         map[string]any{"type": "array", "items": stringSchema("Expression tracked at each stop.")},
		"wait_ms":       map[string]any{"type": "integer", "minimum": 0, "maximum": 240000},
		"pid":           map[string]any{"type": "integer", "minimum": 1},
		"host":          map[string]any{"type": "string"},
		"port":          map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"force":         map[string]any{"type": "boolean"},
	} {
		properties[key] = value
	}
	properties["initial_breakpoints"] = map[string]any{
		"type":  "array",
		"items": schemaObject(debugTargetOptions(), "target"),
	}
	return modernTool{
		Name: "debug_session", Description: "Start, attach, restart, or stop a debugger session; start and attach may set initial breakpoints.",
		Profiles: profiles, Destructive: true,
		InputSchema: debugActionSchema(properties, []string{"workspace_id", "idempotency_key", "action"},
			"start", "attach", "restart", "stop"),
	}
}

func modernDebugBreakpointsTool(profiles []mcpProfile) modernTool {
	properties := debugStatefulProperties()
	for key, value := range debugTargetOptions() {
		properties[key] = value
	}
	return modernTool{
		Name: "debug_breakpoints", Description: "List, set, remove, or clear breakpoints using the shared revision-bound source target.",
		Profiles: profiles, Destructive: true,
		InputSchema: debugActionSchema(properties, []string{"workspace_id", "idempotency_key", "action"},
			"list", "set", "remove", "clear"),
	}
}

func modernDebugControlTool(profiles []mcpProfile) modernTool {
	properties := debugStatefulProperties()
	properties["wait_ms"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 240000}
	properties["count"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 50}
	properties["target"] = modernDebugTargetSchema()
	properties["line_offset"] = map[string]any{"type": "integer", "minimum": 1}
	return modernTool{
		Name: "debug_control", Description: "Continue, pause, step, or run to a revision-bound source target.",
		Profiles: profiles, Destructive: true,
		InputSchema: debugActionSchema(properties, []string{"workspace_id", "idempotency_key", "action"},
			"continue", "pause", "step_over", "step_into", "step_out", "run_to"),
	}
}

func modernDebugInspectTool(profiles []mcpProfile) modernTool {
	properties := debugReadProperties()
	for key, value := range map[string]any{
		"thread":     map[string]any{"type": "integer"},
		"depth":      map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		"all_frames": map[string]any{"type": "boolean"},
		"frame":      map[string]any{"type": "integer", "minimum": 0},
		"scope":      enumSchema("locals", "globals", "all"),
		"expand":     map[string]any{"type": "string"},
		"max":        map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		"expression": map[string]any{"type": "string"},
		"context":    enumSchema("repl", "watch", "hover"),
		"policy":     enumSchema("read_only", "allow_side_effects"),
	} {
		properties[key] = value
	}
	return modernTool{
		Name: "debug_inspect", Description: "Inspect threads, stacks, scopes, variables, or explicitly governed evaluation.",
		Profiles: profiles, ReadOnly: true,
		InputSchema: debugActionSchema(properties, []string{"workspace_id", "action"},
			"threads", "stack", "scopes", "variables", "evaluate"),
	}
}

func validateModernDebugArguments(name string, arguments map[string]any) error {
	action, _ := arguments["action"].(string)
	baseStateful := map[string]bool{"workspace_id": true, "idempotency_key": true, "transaction_id": true, "action": true}
	baseRead := map[string]bool{"workspace_id": true, "transaction_id": true, "action": true}
	allowed := map[string]bool{}
	for key, value := range baseStateful {
		allowed[key] = value
	}
	var extras []string
	switch name {
	case "debug_session":
		switch action {
		case "start":
			extras = []string{"file", "program", "args", "cwd", "env", "config", "adapter", "stop_on_entry", "variables", "track", "wait_ms", "initial_breakpoints"}
		case "attach":
			extras = []string{"pid", "host", "port", "file", "config", "adapter", "variables", "track", "wait_ms", "initial_breakpoints"}
			if arguments["pid"] == nil && arguments["port"] == nil {
				return errors.New("debug_session attach requires pid or port")
			}
		case "restart":
			extras = []string{"wait_ms"}
		case "stop":
			extras = []string{"force"}
		default:
			return fmt.Errorf("unknown debug_session action %q", action)
		}
	case "debug_breakpoints":
		switch action {
		case "set", "remove":
			extras = []string{"target", "line_offset", "condition", "hit_condition", "log_message"}
			if arguments["target"] == nil {
				return fmt.Errorf("debug_breakpoints %s requires target", action)
			}
		case "list", "clear":
		default:
			return fmt.Errorf("unknown debug_breakpoints action %q", action)
		}
	case "debug_control":
		extras = []string{"wait_ms"}
		switch action {
		case "step_over", "step_into", "step_out":
			extras = append(extras, "count")
		case "run_to":
			extras = append(extras, "target", "line_offset")
			if arguments["target"] == nil {
				return errors.New("debug_control run_to requires target")
			}
		case "continue", "pause":
		default:
			return fmt.Errorf("unknown debug_control action %q", action)
		}
	case "debug_inspect":
		allowed = baseRead
		switch action {
		case "threads":
		case "stack":
			extras = []string{"thread", "depth", "all_frames"}
		case "scopes":
			extras = []string{"thread", "frame"}
		case "variables":
			extras = []string{"thread", "frame", "scope", "expand", "depth", "max"}
		case "evaluate":
			extras = []string{"thread", "frame", "expression", "context", "policy"}
			if arguments["expression"] == nil {
				return errors.New("debug_inspect evaluate requires expression")
			}
		default:
			return fmt.Errorf("unknown debug_inspect action %q", action)
		}
	default:
		return nil
	}
	for _, key := range extras {
		allowed[key] = true
	}
	for key := range arguments {
		if !allowed[key] {
			return fmt.Errorf("%s action %q contains irrelevant property %q", name, action, key)
		}
	}
	return nil
}

func (d *directWorkspaces) debug(ctx context.Context, requestID, name string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	action, _ := arguments["action"].(string)
	if name == "debug_inspect" && action == "evaluate" {
		policy, _ := arguments["policy"].(string)
		if policy == "" {
			policy = "read_only"
		}
		if policy != "allow_side_effects" {
			result := modernEnvelope(requestID, workspace, "unavailable", "approval_required",
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
	backend, err := d.debugProvider(workspace)
	if err != nil {
		return debugUnavailable(requestID, workspace, "debug_provider_unavailable", err)
	}
	operation, providerArguments, err := d.debugOperation(workspace, name, action, arguments)
	if err != nil {
		return modernFailure(requestID, workspace, "debug_target_invalid", err)
	}
	if initial := anySlice(arguments["initial_breakpoints"]); name == "debug_session" && (action == "start" || action == "attach") {
		for _, item := range initial {
			breakpoint, _ := item.(map[string]any)
			target, _ := breakpoint["target"].(map[string]any)
			resolved, resolveErr := d.debugTargetArguments(workspace, target, breakpoint)
			if resolveErr != nil {
				return modernFailure(requestID, workspace, "debug_target_invalid", resolveErr)
			}
			for _, key := range []string{"condition", "hit_condition", "log_message"} {
				if value, ok := breakpoint[key]; ok {
					resolved[key] = value
				}
			}
			if _, callErr := callModernDebugProvider(ctx, requestID, workspace, backend, "debug_breakpoint", resolved); callErr != nil {
				return debugProviderFailure(requestID, workspace, callErr)
			}
		}
	}
	value, err := callModernDebugProvider(ctx, requestID, workspace, backend, operation, providerArguments)
	if err != nil {
		return debugProviderFailure(requestID, workspace, err)
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
	result := modernEnvelope(requestID, workspace, outcome, "", summary, map[string]any{"debug": data, "coverage": debugCoverage(complete)})
	result["next"] = debugNext(workspace, name, action, data)
	return result
}

func (d *directWorkspaces) debugProvider(workspace *workspacecore.Workspace) (provider.Provider, error) {
	identity := workspace.Identity()
	d.providerMu.Lock()
	defer d.providerMu.Unlock()
	if existing := d.providers[identity.ID]; existing != nil {
		return existing, nil
	}
	backend, err := referenceProviders.Open(providerOpenConfig{
		Root: identity.Root, InitFile: os.Getenv("AGENT99_HEADLESS_INIT"),
		RuntimePath: shippedRuntimePath(), Debug: true,
	})
	if err != nil {
		return nil, err
	}
	d.providers[identity.ID] = backend
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	return backend, nil
}

func callModernDebugProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, backend provider.Provider, operation string, arguments map[string]any) (any, error) {
	callContext := ctx
	if callContext == nil {
		callContext = context.Background()
	}
	var cancel context.CancelFunc
	if _, ok := callContext.Deadline(); !ok {
		callContext, cancel = context.WithTimeout(callContext, modernDebugTimeout)
		defer cancel()
	}
	deadline, _ := callContext.Deadline()
	descriptor := backend.Descriptor()
	result, err := backend.Call(callContext, provider.Request{
		Context: provider.RequestContext{
			RequestID: requestID, WorkspaceID: string(workspace.Identity().ID), Epoch: descriptor.Epoch,
			Deadline: deadline, Cancellation: descriptor.Cancellation,
		},
		Operation: operation, Arguments: arguments,
	})
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

func (d *directWorkspaces) debugOperation(workspace *workspacecore.Workspace, name, action string, arguments map[string]any) (string, map[string]any, error) {
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
		case "list":
			return "debug_breakpoints", copied, nil
		case "clear":
			copied["clear"] = true
			return "debug_breakpoints", copied, nil
		case "set", "remove":
			target, _ := arguments["target"].(map[string]any)
			resolved, err := d.debugTargetArguments(workspace, target, arguments)
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
			resolved, err := d.debugTargetArguments(workspace, target, arguments)
			if err != nil {
				return "", nil, err
			}
			copied["to"] = resolved
			return "debug_continue", copied, nil
		}
	case "debug_inspect":
		switch action {
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
		case "workspace_id", "idempotency_key", "transaction_id", "initial_breakpoints", "target", "line_offset", "policy":
		default:
			copied[key] = value
		}
	}
	delete(copied, "action")
	return copied
}

func (d *directWorkspaces) debugTargetArguments(workspace *workspacecore.Workspace, target map[string]any, arguments map[string]any) (map[string]any, error) {
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

func debugProviderFailure(requestID string, workspace *workspacecore.Workspace, err error) map[string]any {
	var failure *provider.Failure
	if errors.As(err, &failure) {
		if failure.Code == provider.FailureCancelled || failure.Code == provider.FailureDeadline {
			return modernFailure(requestID, workspace, string(failure.Code), err)
		}
		return debugUnavailable(requestID, workspace, string(failure.Code), err)
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "not installed") || strings.Contains(lower, "no debug adapter") ||
		strings.Contains(lower, "could not start") || strings.Contains(lower, "executable") ||
		strings.Contains(lower, "source map") || strings.Contains(lower, "runtime") {
		return debugUnavailable(requestID, workspace, "debugger_unavailable", err)
	}
	return modernFailure(requestID, workspace, "debugger_failed", err)
}

func debugUnavailable(requestID string, workspace *workspacecore.Workspace, code string, err error) map[string]any {
	detail := strings.ReplaceAll(err.Error(), "agent99", "Huyang")
	summary := "Debugger unavailable; install or configure the requested DAP adapter and language runtime, then retry"
	result := modernEnvelope(requestID, workspace, "unavailable", code, summary, map[string]any{
		"coverage": map[string]any{"complete": false, "unavailable": []string{"debug_adapter_or_runtime"}},
		"repair": map[string]any{
			"action": "install_or_configure_dap_adapter", "detail": detail,
			"supported_adapters": []string{"delve", "debugpy", "codelldb", "lldb-dap", "gdb", "js-debug", "java"},
		},
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

func lineByteRange(content []byte, line int) (int, int, error) {
	if line < 1 {
		return 0, 0, errors.New("line must be at least one")
	}
	start := 0
	for current := 1; current < line; current++ {
		index := bytes.IndexByte(content[start:], '\n')
		if index < 0 {
			return 0, 0, fmt.Errorf("line %d is outside the document", line)
		}
		start += index + 1
	}
	end := start
	if index := bytes.IndexByte(content[start:], '\n'); index >= 0 {
		end += index
	} else {
		end = len(content)
	}
	return start, end, nil
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
