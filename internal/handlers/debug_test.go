package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type scriptedDebugProvider struct {
	descriptor provider.Descriptor
	requests   []provider.Request
	call       func(provider.Request) (any, error)
	done       chan struct{}
}

func (p *scriptedDebugProvider) Descriptor() provider.Descriptor { return p.descriptor }

func (p *scriptedDebugProvider) Health(context.Context) provider.Health {
	return provider.Health{State: provider.HealthHealthy, Epoch: p.descriptor.Epoch}
}

func (p *scriptedDebugProvider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	p.requests = append(p.requests, request)
	if p.call == nil {
		return provider.Result{}, nil
	}
	value, err := p.call(request)
	return provider.Result{Value: value}, err
}

func (p *scriptedDebugProvider) Close(context.Context) error {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}

func (p *scriptedDebugProvider) Done() <-chan struct{} { return p.done }

func modernDebugFixture(t *testing.T, call func(provider.Request) (any, error)) (*Handlers, *workspacecore.Workspace, *scriptedDebugProvider, workspacecore.HandleRecord) {
	t.Helper()
	root := t.TempDir()
	source := []byte("package main\nfunc main() {}\n")
	if err := os.WriteFile(filepath.Join(root, "main.go"), source, 0o644); err != nil {
		t.Fatal(err)
	}
	// The workspace starts at the scripted provider's epoch: reusing a live
	// provider synchronises the workspace epoch, so handles registered here
	// must already belong to epoch 7.
	workspace, err := workspacecore.New(workspacecore.KindProject, root, 7)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := workspace.NewRange("main.go", len("package main\n"), len(source)-1)
	if err != nil {
		t.Fatal(err)
	}
	record, err := workspace.RegisterRangeHandle(handle, workspacecore.HandleRange, "main.go:2 debugger target")
	if err != nil {
		t.Fatal(err)
	}
	backend := &scriptedDebugProvider{
		descriptor: provider.Descriptor{
			ID: "debug-script", Backend: "test", Root: root, Epoch: 7,
			Cancellation: provider.CancellationCooperative,
			Capabilities: []provider.Capability{provider.CapabilityExecute},
		},
		call: call,
		done: make(chan struct{}),
	}
	return newTestHandlers(t, fixedFactory{backend: backend}), workspace, backend, record
}

func TestModernDebugSessionInitialBreakpointAndStopContext(t *testing.T) {
	direct, workspace, backend, record := modernDebugFixture(t, func(request provider.Request) (any, error) {
		switch request.Operation {
		case "debug_breakpoint":
			return map[string]any{"file": "main.go", "line": float64(2), "verified": true}, nil
		case "debug_launch":
			return map[string]any{
				"state": "stopped", "reason": "breakpoint",
				"frame":  map[string]any{"file": "main.go", "line": float64(2), "name": "main"},
				"locals": []any{"value: int = 1"}, "output_new": []any{"ready"}, "tracked": map[string]any{"value": "1"},
			}, nil
		default:
			return nil, errors.New("unexpected operation " + request.Operation)
		}
	})
	result := direct.debug(context.Background(), "req_debug", "debug_session", workspace, map[string]any{
		"action": "start", "file": "main.go",
		"initial_breakpoints": []any{map[string]any{"target": map[string]any{"handle": string(record.Handle)}}},
	})
	if result["outcome"] != "ok" {
		t.Fatalf("result = %#v", result)
	}
	if len(backend.requests) != 2 || backend.requests[0].Operation != "debug_breakpoint" || backend.requests[1].Operation != "debug_launch" {
		t.Fatalf("provider requests = %#v", backend.requests)
	}
	if backend.requests[0].Arguments["line"] != 2 {
		t.Fatalf("breakpoint arguments = %#v", backend.requests[0].Arguments)
	}
	data := result["data"].(map[string]any)["debug"].(map[string]any)
	location := data["top_location"].(map[string]any)
	if location["handle"] == "" || location["locator"] == nil {
		t.Fatalf("top location = %#v", location)
	}
	changes := data["changes_since_previous_stop"].(map[string]any)
	if changes["output"] == nil || changes["tracked"] == nil {
		t.Fatalf("changes = %#v", changes)
	}
}

func TestModernDebugActionMappingAndEvaluatePolicy(t *testing.T) {
	direct, workspace, backend, record := modernDebugFixture(t, func(request provider.Request) (any, error) {
		return map[string]any{"state": "stopped", "reason": "step"}, nil
	})
	target := map[string]any{"handle": string(record.Handle)}
	cases := []struct {
		tool, action, operation string
	}{
		{"debug_session", "start", "debug_launch"},
		{"debug_session", "attach", "debug_attach"},
		{"debug_session", "restart", "debug_launch"},
		{"debug_session", "stop", "debug_stop"},
		{"debug_breakpoints", "list", "debug_breakpoints"},
		{"debug_breakpoints", "set", "debug_breakpoint"},
		{"debug_breakpoints", "remove", "debug_breakpoint"},
		{"debug_breakpoints", "clear", "debug_breakpoints"},
		{"debug_control", "continue", "debug_continue"},
		{"debug_control", "pause", "debug_wait"},
		{"debug_control", "step_over", "debug_step"},
		{"debug_control", "step_into", "debug_step"},
		{"debug_control", "step_out", "debug_step"},
		{"debug_control", "run_to", "debug_continue"},
		{"debug_inspect", "threads", "debug_threads"},
		{"debug_inspect", "stack", "debug_stack"},
		{"debug_inspect", "scopes", "debug_scopes"},
		{"debug_inspect", "variables", "debug_variables"},
		{"debug_inspect", "evaluate", "debug_evaluate"},
	}
	for _, test := range cases {
		arguments := map[string]any{"action": test.action}
		if test.action == "set" || test.action == "remove" || test.action == "run_to" {
			arguments["target"] = target
		}
		operation, _, err := direct.debugOperation(workspace, test.tool, test.action, arguments)
		if err != nil || operation != test.operation {
			t.Fatalf("%s/%s operation = %q, %v", test.tool, test.action, operation, err)
		}
	}
	runTo := direct.debug(context.Background(), "req_run_to", "debug_control", workspace, map[string]any{
		"action": "run_to", "target": map[string]any{"handle": string(record.Handle)},
	})
	if runTo["outcome"] != "ok" || backend.requests[0].Operation != "debug_continue" {
		t.Fatalf("run-to result=%#v requests=%#v", runTo, backend.requests)
	}
	to := backend.requests[0].Arguments["to"].(map[string]any)
	if to["file"] != "main.go" || to["line"] != 2 {
		t.Fatalf("run-to target = %#v", to)
	}

	before := len(backend.requests)
	refused := direct.debug(context.Background(), "req_read_only", "debug_inspect", workspace, map[string]any{
		"action": "evaluate", "expression": "counter++",
	})
	if refused["outcome"] != "unavailable" || refused["code"] != "approval_required" || len(backend.requests) != before {
		t.Fatalf("read-only evaluate = %#v requests=%d", refused, len(backend.requests))
	}
	allowed := direct.debug(context.Background(), "req_effectful", "debug_inspect", workspace, map[string]any{
		"action": "evaluate", "expression": "counter++", "policy": "allow_side_effects",
	})
	if allowed["outcome"] != "ok" || backend.requests[len(backend.requests)-1].Operation != "debug_evaluate" {
		t.Fatalf("effectful evaluate = %#v", allowed)
	}
	debug := allowed["data"].(map[string]any)["debug"].(map[string]any)
	if debug["evaluation"].(map[string]any)["debuggee_state_may_have_changed"] != true {
		t.Fatalf("evaluation disclosure = %#v", debug)
	}
}

func TestModernDebugUnavailableAndClosedActionSchemas(t *testing.T) {
	direct, workspace, _, _ := modernDebugFixture(t, func(request provider.Request) (any, error) {
		return nil, errors.New("nvim-dap is not installed; add it to the runtime path")
	})
	result := direct.debug(context.Background(), "req_missing", "debug_session", workspace, map[string]any{
		"action": "start", "file": "main.go",
	})
	if result["outcome"] != "unavailable" || result["code"] != "debugger_unavailable" {
		t.Fatalf("missing adapter result = %#v", result)
	}
	var session mcpapi.ToolDescriptor
	for _, descriptor := range mcpapi.Tools {
		if descriptor.Name == "debug_session" {
			session = descriptor
		}
	}
	arguments := map[string]any{
		"workspace_id": "ws_test", "idempotency_key": "stop", "action": "stop", "file": "main.go",
	}
	if err := mcpapi.ValidateToolArguments(session.InputSchema, arguments); err != nil {
		t.Fatalf("valid compact stop schema rejected: %v", err)
	}
	if err := mcpapi.ValidateDebugArguments(session.Name, arguments); err == nil {
		t.Fatal("stop action accepted a start-only file argument")
	}
	if err := mcpapi.ValidateDebugArguments("debug_breakpoints", map[string]any{
		"workspace_id": "ws_test", "idempotency_key": "list", "action": "list",
		"target": map[string]any{"handle": "h_test"},
	}); err == nil {
		t.Fatal("list action accepted a set-only target")
	}
}

func TestModernDebugInitializationFailureIsActionable(t *testing.T) {
	workspace, err := workspacecore.Open(workspacecore.OpenOptions{
		Kind: workspacecore.KindProject, Root: t.TempDir(), StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := debugProviderFailure("req_init", workspace,
		errors.New("the js-debug adapter did not initialize within 3 s; nvim-dap: Couldn't connect to ::1:<allocated-port>: ECONNREFUSED"))
	if result["outcome"] != "unavailable" || result["code"] != "debugger_unavailable" {
		t.Fatalf("initialization failure was not classified as unavailable: %#v", result)
	}
	if next, ok := result["next"].([]any); !ok || len(next) != 2 {
		t.Fatalf("initialization failure omitted actionable recovery: %#v", result)
	}
	repair := result["data"].(map[string]any)["repair"].(map[string]any)
	if repair["action"] != "install_or_configure_dap_adapter" ||
		!strings.Contains(repair["detail"].(string), "js-debug") {
		t.Fatalf("initialization recovery did not retain adapter detail: %#v", result)
	}
}

// A debugger failure points at tools the debug profile exposes and retries
// the original action under a fresh idempotency key.
func TestDebugFailureRecoveryUsesExposedToolsAndOriginalAction(t *testing.T) {
	next := debugFailureNext("debug_session", "start")
	if len(next) != 2 {
		t.Fatalf("debug failure recovery is incomplete: %#v", next)
	}
	status, _ := next[0].(map[string]any)
	retry, _ := next[1].(map[string]any)
	if status["tool"] != "language_server_status" ||
		retry["tool"] != "debug_session" || retry["action"] != "start" ||
		retry["use_new_idempotency_key"] != true {
		t.Fatalf("debug failure recovery names unavailable operations: %#v", next)
	}
}

// An unavailable debugger names Huyang, never the retired agent99 name, and
// carries a concrete repair step.
func TestDebuggerUnavailableNamesHuyangAndGivesRepairStep(t *testing.T) {
	workspace, err := workspacecore.Open(workspacecore.OpenOptions{
		Kind: workspacecore.KindProject, Root: t.TempDir(), StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := debugUnavailable("req_test", workspace, "debugger_unavailable", errors.New("agent99 adapter not installed"))
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "agent99") || len(result["next"].([]any)) != 2 ||
		result["data"].(map[string]any)["repair"].(map[string]any)["action"] != "install_or_configure_dap_adapter" {
		t.Fatalf("debugger recovery is obsolete or not actionable: %#v", result)
	}
}
