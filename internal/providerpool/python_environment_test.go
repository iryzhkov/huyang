package providerpool

import (
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestProjectCheckCannotCorroborateUnresolvedPythonEnvironment(t *testing.T) {
	workspace, err := workspacecore.New(workspacecore.KindProject, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	before := workspacecore.DiagnosticReport{
		Confidence:         workspacecore.ConfidenceProvisional,
		ProvisionalReasons: []string{"push_missing_current_document_proof", "python_environment_unresolved"},
	}
	after, err := CorroborateWithProjectCheck(workspace, "wsrev_1", "edit_python", []workspacecore.VerificationStage{
		{Stage: "check", Status: workspacecore.VerificationPassed},
	}, before)
	if err != nil {
		t.Fatal(err)
	}
	if after.Confidence != workspacecore.ConfidenceProvisional || len(after.ProvisionalReasons) != 2 {
		t.Fatalf("environment failure was promoted: %#v", after)
	}
	stage := DiagnosticVerificationStage("wsrev_1", after)
	if stage.Coverage.Complete || stage.Status != workspacecore.VerificationSkipped {
		t.Fatalf("environment gap claimed a complete check: %#v", stage)
	}
}
