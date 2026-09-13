package workspace

import "strings"

// A stack pair records a caller/callee relation at a stopped instant.
// It does not certify an async spawn, branch edge, or happens-before relation.
func overlayStackEdges(graph ExecutionGraph, event ExecutionTraceEvent, mapped []string, observed map[string][]int, ambiguous map[string]bool, out *ExecutionOverlay) {
	for i := 1; i < len(mapped) && len(out.RuntimeOnly) < MaxExecutionEdges; i++ {
		from, to := mapped[i], mapped[i-1]
		if from == "" || to == "" {
			continue
		}
		matches := []string{}
		caller := event.Frames[i]
		for _, edge := range graph.Edges {
			if edge.Kind != "calls" && edge.Kind != "call" {
				continue
			}
			if edge.From == from && edge.To == to && edge.SitePath == caller.Path && edge.SiteLine == caller.Line {
				matches = append(matches, edge.ID)
			}
		}
		if len(matches) == 1 {
			observed[matches[0]] = append(observed[matches[0]], event.Sequence)
		} else {
			status := "adapter_model_gap"
			if len(matches) > 1 {
				status = "source_mapping_ambiguous"
				for _, id := range matches {
					ambiguous[id] = true
				}
			}
			out.RuntimeOnly = append(out.RuntimeOnly, ExecutionRuntimeEdge{From: from, To: to, Sequence: event.Sequence, Thread: event.Thread, Kind: "stack_call", Status: status})
		}
	}
}

func overlayTargetedEdge(graph ExecutionGraph, trace ExecutionTrace, edge ExecutionEdge) bool {
	// A breakpoint at a call site only observes arriving there; it does not
	// establish which callee ran. Only callee coverage can qualify a call absence.
	if edge.Kind != "calls" && edge.Kind != "call" {
		return false
	}
	var target ExecutionNode
	for _, node := range graph.Nodes {
		if node.ID == edge.To {
			target = node
			break
		}
	}
	if target.Kind != "function" && !strings.HasPrefix(target.ID, "function:") {
		return false
	}
	for _, point := range trace.Targets {
		if point.Verified && point.Path == target.Path && point.Line == target.Line {
			return true
		}
	}
	return false
}
