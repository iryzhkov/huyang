package workspace

import (
	"context"
	"testing"
	"time"
)

func comparisonTestTrace(id string) ExecutionTrace {
	now := time.Now()
	return ExecutionTrace{ID: id, WorkspaceID: "w", Epoch: 1, Finished: &now, Events: []ExecutionTraceEvent{
		{Kind: "stopped", Sequence: 1, Thread: 1, Frames: []ExecutionTraceFrame{{Path: "x.go", Name: "main.f", Line: 3, SourceHash: "h", Mapping: "launch_snapshot"}},
			Values: []ExecutionTraceValue{{Name: "n", Type: "int", Value: "1"}}}}}
}
func TestExecutionComparisonSeparatesValuesControlAndScheduling(t *testing.T) {
	a, b := comparisonTestTrace("a"), comparisonTestTrace("b")
	b.Events[0].Values[0].Value = "2"
	a.Events = append(a.Events, cloneExecution(a.Events[0]))
	b.Events = append(b.Events, cloneExecution(b.Events[0]))
	a.Events[1].Sequence = 2
	b.Events[1].Sequence = 2
	b.Events[1].Thread = 2
	result, err := CompareExecutionTraces(context.Background(), a, b)
	if err != nil || result.CommonPrefix != 2 || result.CauseStatus != "unknown" {
		t.Fatalf("%+v %v", result, err)
	}
	kinds := map[string]bool{}
	for _, difference := range result.Differences {
		kinds[difference.Kind] = true
	}
	if !kinds["value"] || !kinds["thread_schedule"] {
		t.Fatalf("%+v", result)
	}
	b.Events[1].Frames[0].Line = 4
	result, err = CompareExecutionTraces(context.Background(), a, b)
	if err != nil || result.CommonPrefix != 1 || result.Alignment != "different_location" {
		t.Fatalf("%+v %v", result, err)
	}
	b.Events[0].Values[0].Redacted = true
	result, _ = CompareExecutionTraces(context.Background(), a, b)
	if result.Differences[0].Status != "unknown" {
		t.Fatal("redacted value inferred")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = CompareExecutionTraces(ctx, a, b); ErrorCode(err) != "analysis_cancelled" {
		t.Fatal(err)
	}
}
func TestExecutionComparisonRelocatesTokensButRefusesSignatureChanges(t *testing.T) {
	a, b := comparisonTestTrace("a"), comparisonTestTrace("b")
	source := ExecutionSource{Path: "x.go", Content: "package main\nfunc f(n int) {\n println(n)\n}\n"}
	a.Events[0].Frames[0].FunctionHash, a.Events[0].Frames[0].TokenOffset = ExecutionFrameFingerprint(source, a.Events[0].Frames[0])
	source.Content = "package main\n\nfunc f(n int) {\n\tprintln(n)\n}\n"
	b.Events[0].Frames[0].Line = 4
	b.Events[0].Frames[0].SourceHash = "different"
	b.Events[0].Frames[0].FunctionHash, b.Events[0].Frames[0].TokenOffset = ExecutionFrameFingerprint(source, b.Events[0].Frames[0])
	result, err := CompareExecutionTraces(context.Background(), a, b)
	if err != nil || result.Alignment != "token_relocated" || result.CommonPrefix != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	source.Content = "package main\n\nfunc f(n string) {\n\tprintln(n)\n}\n"
	b.Events[0].Frames[0].FunctionHash, b.Events[0].Frames[0].TokenOffset = ExecutionFrameFingerprint(source, b.Events[0].Frames[0])
	result, _ = CompareExecutionTraces(context.Background(), a, b)
	if result.Alignment != "ambiguous" || result.CommonPrefix != 0 {
		t.Fatalf("%+v", result)
	}
}
