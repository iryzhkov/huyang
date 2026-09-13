//go:build live

package livetest

import (
	"encoding/json"
	"strings"
	"testing"
)

// The friction spool records the shape of a call and never its content. That
// is a promise about somebody's source code, so it is checked against a real
// session rather than against the writer's intent: a distinctive string is
// edited into the fixture, a plan is prepared so a sandbox exists, and the
// spool must contain neither the string nor the path of the sandbox.
func TestTheSpoolRecordsNoSourceAndNoSandboxPath(t *testing.T) {
	const secret = "quaternionSalary"
	instance := start(t)
	session := instance.connect("full")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	applied := call(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "telemetry-edit",
		"operation": map[string]any{
			"kind": "replace_literal", "path": "ledger.go",
			"old": "return 7", "new": "return 7 // " + secret,
		},
	})
	if outcome(applied) != "ok" && outcome(applied) != "provisional" {
		t.Fatalf("edit = %#v", applied)
	}
	// A prepare materialises a sandbox, which is the path that must never
	// reach a public field or the spool.
	prepared := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "telemetry-plan",
		"operations": []any{map[string]any{
			"op_id": "touch", "kind": "create_file", "path": "telemetry.go",
			"content": "package huyangfixture // " + secret + "\n",
		}},
	})
	if outcome(prepared) == "failed" {
		t.Fatalf("prepare = %#v", prepared)
	}

	lines := instance.spoolLines(t)
	if len(lines) == 0 {
		t.Fatal("the session recorded no friction events, so this proves nothing")
	}
	for _, line := range lines {
		if strings.Contains(line, secret) {
			t.Fatalf("the spool recorded source content: %s", line)
		}
		if strings.Contains(line, "sandbox") || strings.Contains(line, instance.stateDir) {
			t.Fatalf("the spool recorded a sandbox or state path: %s", line)
		}
		// The spool is one JSON object per line, holding argument key names
		// rather than argument values.
		var event struct {
			Tool    string   `json:"tool"`
			Args    []string `json:"args"`
			Session string   `json:"session"`
			Root    string   `json:"root"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("spool line is not JSON: %v\n%s", err, line)
		}
		if event.Tool == "" || event.Session != "livetest" {
			t.Fatalf("spool line is not attributed to this session: %s", line)
		}
		if strings.Contains(event.Root, "/") {
			t.Fatalf("the spool recorded a path rather than a project name: %s", event.Root)
		}
	}
}

// Sandbox paths are the service's own business. A prepared plan names its
// revision and the files it touched, in workspace-relative form, and never
// where the copy of the tree lived.
func TestPreparedPlansNeverExposeSandboxPaths(t *testing.T) {
	instance := start(t)
	session := instance.connect("edit")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)

	prepared := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "sandbox-paths",
		"operations": []any{map[string]any{
			"op_id": "add", "kind": "create_file", "path": "added.go",
			"content": "package huyangfixture\n",
		}},
	})
	rendered, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{instance.stateDir, "sandboxes", "sandbox-"} {
		if strings.Contains(string(rendered), forbidden) {
			t.Fatalf("a prepared plan exposed %q:\n%s", forbidden, rendered)
		}
	}
}
