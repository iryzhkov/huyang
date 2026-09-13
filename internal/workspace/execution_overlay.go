package workspace

import (
	"context"
	"strings"
)

type ExecutionOverlayEdge struct {
	Edge      string `json:"edge"`
	Status    string `json:"status"`
	Sequences []int  `json:"sequences"`
}
type ExecutionObservation struct {
	Sequence int      `json:"sequence"`
	Thread   int      `json:"thread"`
	Nodes    []string `json:"nodes"`
	Mapping  string   `json:"mapping"`
}
type ExecutionRuntimeEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Sequence int    `json:"sequence"`
	Thread   int    `json:"thread"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
}
type ExecutionOverlay struct {
	TraceID      string                 `json:"trace_id"`
	TraceDigest  string                 `json:"trace_digest"`
	GraphID      string                 `json:"graph_id"`
	Revision     string                 `json:"revision"`
	Edges        []ExecutionOverlayEdge `json:"edges"`
	Observations []ExecutionObservation `json:"observations"`
	RuntimeOnly  []ExecutionRuntimeEdge `json:"runtime_only_edges"`
	Frontiers    []string               `json:"observed_frontiers"`
	Coverage     ExecutionCoverage      `json:"coverage"`
}

// OverlayExecution preserves the static graph and never treats sampled absence
// as proof. Stack adjacency supports call observations, not temporal causality.
func OverlayExecution(ctx context.Context, graph ExecutionGraph, trace ExecutionTrace) (ExecutionOverlay, error) {
	out := ExecutionOverlay{TraceID: trace.ID, TraceDigest: trace.Digest, GraphID: graph.ID, Revision: graph.Revision,
		Edges: []ExecutionOverlayEdge{}, Observations: []ExecutionObservation{}, RuntimeOnly: []ExecutionRuntimeEdge{}, Frontiers: []string{},
		Coverage: ExecutionCoverage{Gaps: []string{"one_execution", "sampled_stops", "binary_source_correspondence_unverified"}, Limits: []string{}}}
	if trace.Revision != graph.Revision {
		return out, Codedf("trace_source_changed", "trace and static graph have different content revisions")
	}
	if len(graph.Nodes) > MaxExecutionNodes || len(graph.Edges) > MaxExecutionEdges || len(trace.Events) > MaxExecutionTraceEvents {
		return out, Codedf("graph_budget_invalid", "overlay inputs exceed execution ceilings")
	}
	out.Coverage.Capped = trace.Coverage.Capped || graph.Coverage.Capped
	out.Coverage.Gaps = uniqueSorted(append(out.Coverage.Gaps, trace.Coverage.Gaps...))
	out.Coverage.Limits = uniqueSorted(append(append(out.Coverage.Limits, trace.Coverage.Limits...), graph.Coverage.Limits...))
	observed := map[string][]int{}
	ambiguous := map[string]bool{}
	retainedBytes := 0
	for _, event := range trace.Events {
		if ctx.Err() != nil {
			return out, Coded("analysis_cancelled", ctx.Err())
		}
		if event.Kind != "stopped" {
			continue
		}
		observation := ExecutionObservation{Sequence: event.Sequence, Thread: event.Thread, Nodes: []string{}, Mapping: "launch_snapshot"}
		if len(event.Frames) > MaxExecutionTraceFrames {
			return out, Codedf("graph_budget_invalid", "trace stack exceeds frame ceiling")
		}
		mapped := make([]string, len(event.Frames))
		for i, frame := range event.Frames {
			mapped[i] = overlayFrameNode(graph, trace, frame)
			if mapped[i] == "" {
				observation.Mapping = "source_mapping_ambiguous"
				continue
			}
			observation.Nodes = append(observation.Nodes, mapped[i])
		}
		out.Observations = append(out.Observations, observation)
		previous := len(out.RuntimeOnly)
		overlayStackEdges(graph, event, mapped, observed, ambiguous, &out)
		retainedBytes += len(executionJSON(observation)) + len(executionJSON(out.RuntimeOnly[previous:]))
		if len(mapped) > 0 && mapped[0] != "" {
			out.Frontiers = append(out.Frontiers, mapped[0])
		}
		if len(out.RuntimeOnly) >= MaxExecutionEdges || retainedBytes > MaxExecutionTraceBytes/2 {
			out.Coverage.Capped = true
			out.Coverage.Limits = uniqueSorted(append(out.Coverage.Limits, "overlay_events_or_bytes"))
			break
		}
	}
	out.Frontiers = uniqueSorted(out.Frontiers)
	for _, edge := range graph.Edges {
		if ctx.Err() != nil {
			return out, Coded("analysis_cancelled", ctx.Err())
		}
		status := "uninstrumented"
		if overlayTargetedEdge(graph, trace, edge) {
			status = "not_observed"
		}
		if ambiguous[edge.ID] {
			status = "source_mapping_ambiguous"
		}
		if seq := observed[edge.ID]; len(seq) > 0 {
			status = "observed"
		}
		out.Edges = append(out.Edges, ExecutionOverlayEdge{Edge: edge.ID, Status: status, Sequences: observed[edge.ID]})
	}
	if len(executionJSON(out)) > MaxExecutionTraceBytes {
		return out, Codedf("graph_budget_invalid", "overlay exceeds encoded byte ceiling")
	}
	return out, nil
}

func overlayFrameNode(graph ExecutionGraph, trace ExecutionTrace, frame ExecutionTraceFrame) string {
	if frame.Mapping != "launch_snapshot" || frame.SourceHash == "" || trace.SourceHashes[frame.Path] != frame.SourceHash {
		return ""
	}
	found := ""
	for _, node := range graph.Nodes {
		if !strings.HasPrefix(node.ID, "function:") && node.Kind != "function" || node.Path != frame.Path {
			continue
		}
		if node.Name != frame.Name && !strings.HasSuffix(frame.Name, "."+node.Name) {
			continue
		}
		if found != "" && found != node.ID {
			return ""
		}
		found = node.ID
	}
	return found
}
