package workspace

import (
	"context"
	"sort"
	"time"
)

// ExecutionGraphPaths enumerates simple paths. Repeating a cycle cannot make
// an unreachable node reachable, so the graph retains cycle edges while path
// enumeration visits each node once per candidate.
func ExecutionGraphPaths(ctx context.Context, graph ExecutionGraph, from, to string, maxPaths, maxDepth int) ExecutionPaths {
	result := ExecutionPaths{Status: ExecutionUnknown, Paths: []GraphPath{}, Coverage: cloneExecution(graph.Coverage)}
	if maxPaths <= 0 || maxPaths > graph.Budget.Paths {
		maxPaths = graph.Budget.Paths
	}
	if maxDepth <= 0 || maxDepth > graph.Budget.Depth {
		maxDepth = graph.Budget.Depth
	}
	known := map[string]bool{}
	for _, node := range graph.Nodes {
		known[node.ID] = true
	}
	if !known[from] || !known[to] {
		result.Coverage.Complete = false
		result.Coverage.Gaps = append(result.Coverage.Gaps, "endpoint_not_in_graph")
		return result
	}
	adjacent := map[string][]ExecutionEdge{}
	for _, edge := range graph.Edges {
		if edge.Kind == "data_dependency" || edge.Kind == "mutates" || edge.Kind == "syntax_next" {
			continue
		}
		adjacent[edge.From] = append(adjacent[edge.From], edge)
	}
	for node := range adjacent {
		sort.Slice(adjacent[node], func(i, j int) bool { return adjacent[node][i].ID < adjacent[node][j].ID })
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(graph.Budget.AnalysisMillis)*time.Millisecond)
	defer cancel()
	walk := executionWalk{ctx: ctx, graph: graph, result: &result, adjacent: adjacent,
		seen: map[string]bool{}, to: to, paths: maxPaths, depth: maxDepth}
	walk.visit(from, []GraphStep{{Node: from}})
	if len(result.Paths) > 0 {
		result.Status = ExecutionStaticPossible
	} else if result.Coverage.Complete && !result.Coverage.Capped && len(result.Coverage.Gaps) == 0 && len(result.Coverage.Limits) == 0 {
		result.Status = ExecutionStaticallyUnreachable
	}
	return result
}

type executionWalk struct {
	ctx                  context.Context
	graph                ExecutionGraph
	result               *ExecutionPaths
	adjacent             map[string][]ExecutionEdge
	seen                 map[string]bool
	to                   string
	paths, depth, visits int
}

func (w *executionWalk) capped(limit string) {
	w.result.Coverage.Complete = false
	w.result.Coverage.Capped = true
	w.result.Coverage.Limits = uniqueSorted(append(w.result.Coverage.Limits, limit))
}

func (w *executionWalk) visit(node string, steps []GraphStep) {
	if w.ctx.Err() != nil {
		w.capped("analysis_time")
		return
	}
	if len(w.result.Paths) >= w.paths {
		w.capped("paths")
		return
	}
	if w.visits >= w.graph.Budget.Edges {
		w.capped("traversal")
		return
	}
	w.visits++
	if node == w.to {
		fingerprint := w.graph.ID + executionJSON(steps)
		w.result.Paths = append(w.result.Paths, GraphPath{ID: "path_" + hashBytes([]byte(fingerprint))[:32], Steps: append([]GraphStep(nil), steps...)})
		return
	}
	w.seen[node] = true
	defer delete(w.seen, node)
	for _, edge := range w.adjacent[node] {
		if w.seen[edge.To] {
			continue
		}
		if len(steps)-1 >= w.depth {
			w.capped("depth")
			continue
		}
		next := append(append([]GraphStep(nil), steps...), GraphStep{Node: edge.To, Edge: edge.ID})
		w.visit(edge.To, next)
		if w.ctx.Err() != nil || w.visits >= w.graph.Budget.Edges {
			w.capped("traversal")
			return
		}
	}
}
