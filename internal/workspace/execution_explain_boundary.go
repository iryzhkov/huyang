package workspace

type ExecutionExplanationBoundary struct {
	Kind     string `json:"kind"`
	Node     string `json:"node,omitempty"`
	Sequence int    `json:"sequence,omitempty"`
	Thread   int    `json:"thread,omitempty"`
	Reason   string `json:"reason"`
	Status   string `json:"status"`
}

// Candidate static exits stay separate from observed debugger boundaries.
func ExecutionExplanationBoundaries(graph ExecutionGraph, trace ExecutionTrace) []ExecutionExplanationBoundary {
	result := []ExecutionExplanationBoundary{}
	for _, event := range trace.Events {
		if len(result) >= MaxExecutionExplanations {
			return result
		}
		if event.Kind == "capture_finished" || event.Reason == "exception" || event.Reason == "pause" {
			result = append(result, ExecutionExplanationBoundary{Kind: event.Kind, Sequence: event.Sequence, Thread: event.Thread, Reason: event.Reason, Status: "observed"})
		}
	}
	for _, node := range graph.Nodes {
		if len(result) >= MaxExecutionExplanations {
			break
		}
		if node.Kind == "exit" || node.Kind == "async_boundary" {
			result = append(result, ExecutionExplanationBoundary{Kind: node.Kind, Node: node.ID, Reason: "Static candidate boundary; execution and causal relevance are unknown.", Status: "static_possible"})
		}
	}
	return result
}
