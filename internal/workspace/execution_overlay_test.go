package workspace

import (
	"context"
	"testing"
)

func TestExecutionOverlayEvidenceAndIsolation(t *testing.T) {
	graph := ExecutionGraph{ID: "g", Revision: "r", Nodes: []ExecutionNode{
		{ID: "a", Kind: "function", Name: "a", Path: "main.go", Line: 1}, {ID: "b", Kind: "function", Name: "b", Path: "main.go", Line: 5},
		{ID: "c", Kind: "function", Name: "c", Path: "main.go", Line: 9}},
		Edges: []ExecutionEdge{{ID: "ab", From: "a", To: "b", Kind: "calls", SitePath: "main.go", SiteLine: 2},
			{ID: "ac", From: "a", To: "c", Kind: "calls", SitePath: "main.go", SiteLine: 3}}}
	trace := ExecutionTrace{ID: "t", Revision: "r", SourceHashes: map[string]string{"main.go": "h"}, Events: []ExecutionTraceEvent{
		{Sequence: 1, Thread: 1, Kind: "stopped", Frames: []ExecutionTraceFrame{
			{Path: "main.go", Name: "main.b", Line: 6, SourceHash: "h", Mapping: "launch_snapshot"},
			{Path: "main.go", Name: "main.a", Line: 2, SourceHash: "h", Mapping: "launch_snapshot"}}}},
		Targets: []ExecutionTraceTarget{{Path: "main.go", Line: 9, Verified: true}}}
	before := executionJSON(graph)
	overlay, err := OverlayExecution(context.Background(), graph, trace)
	if err != nil {
		t.Fatal(err)
	}
	if overlay.Edges[0].Status != "observed" || overlay.Edges[1].Status != "not_observed" || overlay.Coverage.Complete {
		t.Fatalf("%+v", overlay)
	}
	if before != executionJSON(graph) {
		t.Fatal("historical graph changed")
	}
	trace.Events[0].Frames[1].Line = 99
	overlay, err = OverlayExecution(context.Background(), graph, trace)
	if err != nil || len(overlay.RuntimeOnly) != 1 || overlay.RuntimeOnly[0].Status != "adapter_model_gap" {
		t.Fatalf("%+v %v", overlay, err)
	}
	trace.Events[0].Frames[0].Mapping = "stale"
	overlay, err = OverlayExecution(context.Background(), graph, trace)
	if err != nil || overlay.Observations[0].Mapping != "source_mapping_ambiguous" || overlay.Edges[0].Status == "observed" {
		t.Fatalf("%+v %v", overlay, err)
	}
	trace.Revision = "different"
	if _, err = OverlayExecution(context.Background(), graph, trace); ErrorCode(err) != "trace_source_changed" {
		t.Fatal(err)
	}
	trace.Revision = "r"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = OverlayExecution(ctx, graph, trace); ErrorCode(err) != "analysis_cancelled" {
		t.Fatal(err)
	}
}
