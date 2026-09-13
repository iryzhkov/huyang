package workspace

import "context"

const MaxExecutionExplanations = 128

type ExecutionConditionExplanation struct {
	Node           string                `json:"node"`
	Sequence       int                   `json:"sequence,omitempty"`
	Thread         int                   `json:"thread,omitempty"`
	Expression     string                `json:"expression"`
	Status         string                `json:"status"`
	Value          *bool                 `json:"value,omitempty"`
	Operands       []ExecutionTraceValue `json:"operands"`
	CandidateEdges []string              `json:"candidate_edges"`
	Reason         string                `json:"reason"`
}
type ExecutionExplanation struct {
	CauseStatus  string                          `json:"cause_status"`
	TraceID      string                          `json:"trace_id"`
	Target       string                          `json:"target"`
	TargetStatus string                          `json:"target_status"`
	Reason       string                          `json:"reason"`
	Conditions   []ExecutionConditionExplanation `json:"conditions"`
	Frontiers    []ExecutionPathFrontier         `json:"frontier_candidates"`
	Completion   string                          `json:"completion"`
	Coverage     ExecutionCoverage               `json:"coverage"`
}

func ExplainExecution(ctx context.Context, graph ExecutionGraph, trace ExecutionTrace, overlay ExecutionOverlay, paths []GraphPath, target string) (ExecutionExplanation, error) {
	out := ExecutionExplanation{CauseStatus: "unknown", TraceID: trace.ID, Target: target, TargetStatus: "not_observed",
		Reason:     "The target is absent from retained samples; uninstrumented intervals and alternative paths prevent a general absence claim.",
		Conditions: []ExecutionConditionExplanation{}, Frontiers: ExecutionOverlayFrontiers(paths, overlay), Completion: trace.Completion,
		Coverage: cloneExecution(overlay.Coverage)}
	if trace.Revision != graph.Revision || overlay.TraceID != trace.ID || overlay.GraphID != graph.ID {
		return out, Codedf("trace_source_changed", "explanation inputs do not share trace and graph identity")
	}
	if len(graph.Nodes) > MaxExecutionNodes || len(graph.Edges) > MaxExecutionEdges || len(trace.Events) > MaxExecutionTraceEvents {
		return out, Codedf("graph_budget_invalid", "explanation inputs exceed execution ceilings")
	}
	retainedBytes := 0
	for _, observation := range overlay.Observations {
		for _, id := range observation.Nodes {
			if id == target {
				out.TargetStatus = "observed"
				out.Reason = "The target has a mapped stack frame in this execution; the complete candidate path is not certified."
			}
		}
	}
	for _, node := range graph.Nodes {
		if ctx.Err() != nil {
			return out, Coded("analysis_cancelled", ctx.Err())
		}
		if node.Condition == nil {
			continue
		}
		if len(out.Conditions) >= MaxExecutionExplanations {
			out.Coverage.Capped = true
			out.Coverage.Limits = uniqueSorted(append(out.Coverage.Limits, "condition_explanations"))
			break
		}
		samples := explainConditionSamples(ctx, node, trace)
		if len(samples) == 0 {
			samples = []ExecutionConditionExplanation{{Node: node.ID, Expression: node.Condition.Text, Status: "unknown",
				Reason: "No exact condition-location sample with usable operands.", Operands: []ExecutionTraceValue{}, CandidateEdges: []string{}}}
		}
		for _, sample := range samples {
			if len(out.Conditions) >= MaxExecutionExplanations {
				out.Coverage.Capped = true
				out.Coverage.Limits = uniqueSorted(append(out.Coverage.Limits, "condition_explanations"))
				break
			}
			if sample.Value != nil {
				kind := "branch_false"
				if *sample.Value {
					kind = "branch_true"
				}
				for _, edge := range graph.Edges {
					if edge.From == node.ID && edge.Kind == kind {
						sample.CandidateEdges = append(sample.CandidateEdges, edge.ID)
					}
				}
			}
			retainedBytes += len(executionJSON(sample))
			if retainedBytes > 1<<20 {
				out.Coverage.Capped = true
				out.Coverage.Limits = uniqueSorted(append(out.Coverage.Limits, "explanation_bytes"))
				return out, nil
			}
			out.Conditions = append(out.Conditions, sample)
		}
	}
	if ctx.Err() != nil {
		return out, Coded("analysis_cancelled", ctx.Err())
	}
	if len(out.Conditions) >= MaxExecutionExplanations {
		out.Coverage.Capped = true
		out.Coverage.Limits = uniqueSorted(append(out.Coverage.Limits, "condition_explanations"))
	}
	return out, nil
}
