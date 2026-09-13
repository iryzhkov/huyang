package handlers

import (
	"encoding/json"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"strings"
	"testing"
)

func TestPrepareWriteFailureExplainsEnvironmentRecovery(t *testing.T) {
	plan := workspacecore.PlanRecord{PlanID: "plan_test", PlanRevision: 1}
	encoded, err := json.Marshal(planFailureNext("prepare", "undeclared_tool_write", plan))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"update_pipeline_command", "environment", "new idempotency key", "inspect", "discard"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %q in %s", want, encoded)
		}
	}
}
