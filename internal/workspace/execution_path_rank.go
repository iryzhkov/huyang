package workspace

import "sort"

// Projection replaces a selected function's summary by its expanded entry.
// It remains incomplete, so missing control slices can never prove absence.
func ExecutionControlProjection(graph ExecutionGraph) ExecutionGraph {
	projected := cloneExecution(graph)
	expanded := map[string]bool{}
	for _, edge := range graph.Edges {
		if edge.Kind == "enters" {
			expanded[edge.From] = true
		}
	}
	projected.Edges = nil
	for _, edge := range graph.Edges {
		if expanded[edge.From] && (edge.Kind == "calls" || edge.Kind == "dynamic_dispatch") {
			continue
		}
		projected.Edges = append(projected.Edges, edge)
	}
	projected.Coverage.Complete = false
	projected.Coverage.Gaps = uniqueSorted(append(projected.Coverage.Gaps, "control_projection_incomplete"))
	return projected
}
func ExecutionPathFunctions(graph ExecutionGraph, paths ExecutionPaths) []string {
	symbols := map[string]bool{}
	for _, node := range graph.Nodes {
		if node.Kind == "symbol" {
			symbols[node.ID] = true
		}
	}
	seen := map[string]bool{}
	selected := []string{}
	for _, path := range paths.Paths {
		for _, step := range path.Steps {
			if symbols[step.Node] && !seen[step.Node] && len(selected) < MaxExecutionExpandedFunctions {
				selected = append(selected, step.Node)
				seen[step.Node] = true
			}
		}
	}
	sort.Strings(selected)
	return selected
}
func RankExecutionPaths(graph ExecutionGraph, result *ExecutionPaths) {
	edges := map[string]ExecutionEdge{}
	nodes := map[string]ExecutionNode{}
	for _, edge := range graph.Edges {
		edges[edge.ID] = edge
	}
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	score := func(path GraphPath) (int, int) {
		uncertainty, unresolved := 0, 0
		for _, step := range path.Steps {
			if node := nodes[step.Node]; node.Kind == "unresolved" || node.Kind == "external" {
				unresolved++
			}
			edge, ok := edges[step.Edge]
			if !ok {
				continue
			}
			best := 3
			for _, fact := range edge.Evidence {
				value := 3
				switch fact.Confidence {
				case "semantic", "native":
					value = 0
				case "parser":
					value = 1
				case "heuristic":
					value = 2
				}
				if value < best {
					best = value
				}
			}
			uncertainty += best
		}
		return uncertainty, unresolved
	}
	sort.SliceStable(result.Paths, func(i, j int) bool {
		a, b := result.Paths[i], result.Paths[j]
		au, ar := score(a)
		bu, br := score(b)
		if au != bu {
			return au < bu
		}
		if ar != br {
			return ar < br
		}
		if len(a.Steps) != len(b.Steps) {
			return len(a.Steps) < len(b.Steps)
		}
		return a.ID < b.ID
	})
}
