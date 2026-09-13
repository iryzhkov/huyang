package mcpapi

import (
	"errors"
	"fmt"
	"sort"
)

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

func modernDebugSessionTool(profiles []Profile) ToolDescriptor {
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
	return ToolDescriptor{
		ExperimentalProperties: map[string]any{"trace_policy": executionTracePolicySchema()},
		Class:                  ClassProviderRead, Name: "debug_session", Description: "Start, attach, restart, or stop a debugger session; start and attach may set initial breakpoints.",
		Profiles: profiles, Destructive: true,
		InputSchema: debugActionSchema(properties, []string{"workspace_id", "idempotency_key", "action"},
			"start", "attach", "restart", "stop"),
	}
}

func modernDebugBreakpointsTool(profiles []Profile) ToolDescriptor {
	properties := debugStatefulProperties()
	for key, value := range debugTargetOptions() {
		properties[key] = value
	}
	return ToolDescriptor{
		ExperimentalProperties: map[string]any{"action": enumSchema("list", "set", "remove", "clear", "watch"), "value_name": stringSchema("Exact scalar local name for a native write watchpoint in an active value-enabled trace.")},
		Class:                  ClassProviderRead, Name: "debug_breakpoints", Description: "List, set, remove, or clear breakpoints using the shared revision-bound source target.",
		Profiles: profiles, Destructive: true,
		InputSchema: debugActionSchema(properties, []string{"workspace_id", "idempotency_key", "action"},
			"list", "set", "remove", "clear"),
	}
}

func modernDebugControlTool(profiles []Profile) ToolDescriptor {
	properties := debugStatefulProperties()
	properties["wait_ms"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 240000}
	properties["count"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 50}
	properties["target"] = modernDebugTargetSchema()
	properties["line_offset"] = map[string]any{"type": "integer", "minimum": 1}
	return ToolDescriptor{
		Class: ClassProviderRead, Name: "debug_control", Description: "Continue, pause, step, or run to a revision-bound source target.",
		Profiles: profiles, Destructive: true,
		InputSchema: debugActionSchema(properties, []string{"workspace_id", "idempotency_key", "action"},
			"continue", "pause", "step_over", "step_into", "step_out", "run_to"),
	}
}

func modernDebugInspectTool(profiles []Profile) ToolDescriptor {
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
	return ToolDescriptor{
		ExperimentalProperties: map[string]any{"action": enumSchema("threads", "stack", "scopes", "variables", "evaluate", "trace", "mutation_capabilities", "value_origin", "compare_traces"), "passing_trace_id": stringSchema("Completed reference trace; passing is a caller-supplied role."), "failing_trace_id": stringSchema("Completed comparison trace; failing is a caller-supplied role."), "value_name": stringSchema("Exact captured local name; no evaluation is performed."), "trace_id": stringSchema("A completed or active trace; omit for the current/latest trace.")},
		Class:                  ClassProviderRead, Name: "debug_inspect", Description: "Inspect threads, stacks, scopes, variables, or explicitly governed evaluation.",
		Profiles: profiles, ReadOnly: true,
		InputSchema: debugActionSchema(properties, []string{"workspace_id", "action"},
			"threads", "stack", "scopes", "variables", "evaluate"),
	}
}

// ValidateDebugArguments refuses properties that do not belong to the
// action of a debugger tool, and the actions that lack their one required
// property. Every other tool passes.
func ValidateDebugArguments(name string, arguments map[string]any) error {
	action, _ := arguments["action"].(string)
	allowed := map[string]bool{"workspace_id": true, "idempotency_key": true, "transaction_id": true, "action": true}
	var extras []string
	var err error
	switch name {
	case "debug_session":
		extras, err = debugSessionExtras(action, arguments)
	case "debug_breakpoints":
		extras, err = debugBreakpointsExtras(action, arguments)
	case "debug_control":
		extras, err = debugControlExtras(action, arguments)
	case "debug_inspect":
		allowed = map[string]bool{"workspace_id": true, "transaction_id": true, "action": true}
		extras, err = debugInspectExtras(action, arguments)
	default:
		return nil
	}
	if err != nil {
		return err
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

func debugSessionExtras(action string, arguments map[string]any) ([]string, error) {
	switch action {
	case "start":
		return []string{"file", "program", "args", "cwd", "env", "config", "adapter", "stop_on_entry", "variables", "track", "wait_ms", "initial_breakpoints", "trace_policy"}, nil
	case "attach":
		if arguments["pid"] == nil && arguments["port"] == nil {
			return nil, errors.New("debug_session attach requires pid or port")
		}
		return []string{"pid", "host", "port", "file", "config", "adapter", "variables", "track", "wait_ms", "initial_breakpoints", "trace_policy"}, nil
	case "restart":
		return []string{"wait_ms"}, nil
	case "stop":
		return []string{"force"}, nil
	default:
		return nil, fmt.Errorf("unknown debug_session action %q", action)
	}
}

func debugBreakpointsExtras(action string, arguments map[string]any) ([]string, error) {
	switch action {
	case "watch":
		if arguments["value_name"] == nil {
			return nil, errors.New("watch requires value_name")
		}
		return []string{"value_name"}, nil
	case "set", "remove":
		if arguments["target"] == nil {
			return nil, fmt.Errorf("debug_breakpoints %s requires target", action)
		}
		return []string{"target", "line_offset", "condition", "hit_condition", "log_message"}, nil
	case "list", "clear":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown debug_breakpoints action %q", action)
	}
}

func debugControlExtras(action string, arguments map[string]any) ([]string, error) {
	switch action {
	case "step_over", "step_into", "step_out":
		return []string{"wait_ms", "count"}, nil
	case "run_to":
		if arguments["target"] == nil {
			return nil, errors.New("debug_control run_to requires target")
		}
		return []string{"wait_ms", "target", "line_offset"}, nil
	case "continue", "pause":
		return []string{"wait_ms"}, nil
	default:
		return nil, fmt.Errorf("unknown debug_control action %q", action)
	}
}

func debugInspectExtras(action string, arguments map[string]any) ([]string, error) {
	switch action {
	case "compare_traces":
		if arguments["passing_trace_id"] == nil || arguments["failing_trace_id"] == nil {
			return nil, errors.New("compare_traces requires passing_trace_id and failing_trace_id")
		}
		return []string{"passing_trace_id", "failing_trace_id"}, nil
	case "value_origin":
		if arguments["trace_id"] == nil || arguments["value_name"] == nil {
			return nil, errors.New("value_origin requires trace_id and value_name")
		}
		return []string{"trace_id", "value_name", "thread"}, nil
	case "mutation_capabilities":
		return nil, nil
	case "trace":
		return []string{"trace_id"}, nil
	case "threads":
		return nil, nil
	case "stack":
		return []string{"thread", "depth", "all_frames"}, nil
	case "scopes":
		return []string{"thread", "frame"}, nil
	case "variables":
		return []string{"thread", "frame", "scope", "expand", "depth", "max"}, nil
	case "evaluate":
		if arguments["expression"] == nil {
			return nil, errors.New("debug_inspect evaluate requires expression")
		}
		return []string{"thread", "frame", "expression", "context", "policy"}, nil
	default:
		return nil, fmt.Errorf("unknown debug_inspect action %q", action)
	}
}
