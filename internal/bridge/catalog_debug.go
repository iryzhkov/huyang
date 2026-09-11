package bridge

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
		Class: scheduleProviderRead, Name: "debug_session", Description: "Start, attach, restart, or stop a debugger session; start and attach may set initial breakpoints.",
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
		Class: scheduleProviderRead, Name: "debug_breakpoints", Description: "List, set, remove, or clear breakpoints using the shared revision-bound source target.",
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
		Class: scheduleProviderRead, Name: "debug_control", Description: "Continue, pause, step, or run to a revision-bound source target.",
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
		Class: scheduleProviderRead, Name: "debug_inspect", Description: "Inspect threads, stacks, scopes, variables, or explicitly governed evaluation.",
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
