package workspace

import (
	"context"
	"testing"
)

func TestExecutionOverlayAmbiguityAndBounds(t *testing.T) {
	graph := ExecutionGraph{Revision: "r", Nodes: []ExecutionNode{{ID: "function:a", Name: "a", Path: "x.go"}, {ID: "function:b", Name: "b", Path: "x.go"}},
		Edges: []ExecutionEdge{{ID: "one", From: "function:a", To: "function:b", Kind: "calls", SitePath: "x.go", SiteLine: 2},
			{ID: "two", From: "function:a", To: "function:b", Kind: "calls", SitePath: "x.go", SiteLine: 2}}}
	trace := ExecutionTrace{Revision: "r", SourceHashes: map[string]string{"x.go": "h"}, Events: []ExecutionTraceEvent{{Kind: "stopped", Sequence: 1, Frames: []ExecutionTraceFrame{
		{Name: "b", Path: "x.go", SourceHash: "h", Mapping: "launch_snapshot"}, {Name: "a", Path: "x.go", Line: 2, SourceHash: "h", Mapping: "launch_snapshot"}}}}}
	overlay, err := OverlayExecution(context.Background(), graph, trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range overlay.Edges {
		if edge.Status != "source_mapping_ambiguous" {
			t.Fatalf("ambiguous site claimed: %+v", edge)
		}
	}
	trace.Events[0].Frames = make([]ExecutionTraceFrame, MaxExecutionTraceFrames+1)
	if _, err = OverlayExecution(context.Background(), graph, trace); ErrorCode(err) != "graph_budget_invalid" {
		t.Fatal(err)
	}
	graph.Nodes = make([]ExecutionNode, MaxExecutionNodes+1)
	if _, err = OverlayExecution(context.Background(), graph, trace); ErrorCode(err) != "graph_budget_invalid" {
		t.Fatal(err)
	}
}
func TestExecutionOverlayFrontiersPreserveSampleUncertainty(t *testing.T) {
	overlay := ExecutionOverlay{Observations: []ExecutionObservation{{Sequence: 1, Thread: 1, Nodes: []string{"a"}}, {Sequence: 2, Thread: 2, Nodes: []string{"b"}}}}
	paths := []GraphPath{{ID: "ab", Steps: []GraphStep{{Node: "a"}, {Node: "b"}}}, {ID: "ac", Steps: []GraphStep{{Node: "a"}, {Node: "c"}}}}
	frontier := ExecutionOverlayFrontiers(paths, overlay)
	if len(frontier) != 2 || frontier[0].Node != "b" || frontier[1].Node != "a" || frontier[0].Status != "observed_candidate" {
		t.Fatalf("%+v", frontier)
	}
	transitions := ExecutionOverlayThreads(overlay)
	if len(transitions) != 1 || transitions[0].Relation != "sample_order_only" {
		t.Fatalf("%+v", transitions)
	}
}
