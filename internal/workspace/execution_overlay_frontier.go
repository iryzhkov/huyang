package workspace

type ExecutionPathFrontier struct {
	Path   string `json:"path"`
	Node   string `json:"node,omitempty"`
	Step   int    `json:"step"`
	Status string `json:"status"`
}
type ExecutionThreadTransition struct {
	Sequence       int    `json:"sequence"`
	PreviousThread int    `json:"previous_thread"`
	Thread         int    `json:"thread"`
	Relation       string `json:"relation"`
}

// Frontier candidates summarize matching observations, not a continuous run of
// a static path. Samples from different visits may support several candidates.
func ExecutionOverlayFrontiers(paths []GraphPath, overlay ExecutionOverlay) []ExecutionPathFrontier {
	seen := map[string]bool{}
	for _, observation := range overlay.Observations {
		for _, node := range observation.Nodes {
			seen[node] = true
		}
	}
	result := []ExecutionPathFrontier{}
	for i, path := range paths {
		if i >= MaxExecutionPaths {
			break
		}
		frontier := ExecutionPathFrontier{Path: path.ID, Step: -1, Status: "unknown"}
		for j, step := range path.Steps {
			if j > MaxExecutionDepth {
				break
			}
			if seen[step.Node] {
				frontier.Node = step.Node
				frontier.Step = j
				frontier.Status = "observed_candidate"
			}
		}
		result = append(result, frontier)
	}
	return result
}
func ExecutionOverlayThreads(overlay ExecutionOverlay) []ExecutionThreadTransition {
	result := []ExecutionThreadTransition{}
	previous := 0
	for _, observation := range overlay.Observations {
		if previous != 0 && observation.Thread != 0 && observation.Thread != previous {
			result = append(result, ExecutionThreadTransition{Sequence: observation.Sequence, PreviousThread: previous, Thread: observation.Thread, Relation: "sample_order_only"})
		}
		previous = observation.Thread
	}
	return result
}
