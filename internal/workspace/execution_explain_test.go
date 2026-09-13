package workspace

import (
	"context"
	"testing"
)

func TestExecutionConditionEvaluationRefusesUnsafeOrMissingValues(t *testing.T) {
	values := []ExecutionTraceValue{{Name: "n", Type: "int", Value: "-1"}}
	for _, expression := range []string{"n < 0", "(n == -1) && (n <= 3)"} {
		value, ok := evaluateTraceCondition(expression, values)
		if !ok || !value {
			t.Fatalf("%s: %v %v", expression, value, ok)
		}
	}
	for _, expression := range []string{"f(n)", "n.member == 1", "n[0] == 1", "missing < 0", "true", "n + 1 == 0", "n / 0 == 1"} {
		if _, ok := evaluateTraceCondition(expression, values); ok {
			t.Fatalf("unsafe or unsupported expression accepted: %s", expression)
		}
	}
	for _, bad := range []ExecutionTraceValue{{Name: "n", Type: "int", Value: "-1", Redacted: true}, {Name: "n", Type: "int", Value: "-1", Truncated: true}, {Name: "n", Type: "float64", Value: "-1"}} {
		if _, ok := evaluateTraceCondition("n < 0", []ExecutionTraceValue{bad}); ok {
			t.Fatal("unusable operand accepted")
		}
	}
	if _, ok := evaluateTraceCondition("n < 0", append(values, values...)); ok {
		t.Fatal("duplicate names accepted")
	}
}

func TestExecutionExplanationDoesNotGeneralizeAbsence(t *testing.T) {
	graph := ExecutionGraph{ID: "g", Revision: "r", Nodes: []ExecutionNode{{ID: "condition", Path: "x.go", Line: 3, Condition: &ExecutionCondition{Text: "n < 0", Variables: []string{"n"}}}},
		Edges: []ExecutionEdge{{ID: "yes", From: "condition", Kind: "branch_true"}, {ID: "no", From: "condition", Kind: "branch_false"}}}
	trace := ExecutionTrace{ID: "t", Revision: "r", Completion: "stopped_by_request", SourceHashes: map[string]string{"x.go": "h"},
		Events: []ExecutionTraceEvent{{Kind: "stopped", Sequence: 1, Thread: 2, Frames: []ExecutionTraceFrame{{Path: "x.go", Line: 3, Mapping: "launch_snapshot", SourceHash: "h"}},
			Values: []ExecutionTraceValue{{Name: "n", Type: "int", Value: "-1"}}}}}
	overlay := ExecutionOverlay{TraceID: "t", GraphID: "g", Coverage: ExecutionCoverage{Gaps: []string{"capture_interrupted"}}}
	result, err := ExplainExecution(context.Background(), graph, trace, overlay, nil, "target")
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetStatus != "not_observed" || len(result.Conditions) != 1 || result.Conditions[0].Status != "inferred" || result.Conditions[0].CandidateEdges[0] != "yes" {
		t.Fatalf("%+v", result)
	}
	trace.Events[0].Frames[0].Mapping = "stale"
	result, err = ExplainExecution(context.Background(), graph, trace, overlay, nil, "target")
	if err != nil || result.Conditions[0].Status != "unknown" {
		t.Fatalf("%+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ExplainExecution(ctx, graph, trace, overlay, nil, "target"); ErrorCode(err) != "analysis_cancelled" {
		t.Fatal(err)
	}
}
