package workspace

import "context"

const MaxExecutionComparisons = 128

type ExecutionTraceDifference struct {
	Kind            string `json:"kind"`
	PassingSequence int    `json:"passing_sequence"`
	FailingSequence int    `json:"failing_sequence"`
	Name            string `json:"name,omitempty"`
	Status          string `json:"status"`
	Detail          string `json:"detail"`
}
type ExecutionTraceComparison struct {
	UnalignedPassingStops     int                        `json:"unaligned_passing_stops"`
	UnalignedFailingStops     int                        `json:"unaligned_failing_stops"`
	UnalignedPassingMutations int                        `json:"unaligned_passing_mutations"`
	UnalignedFailingMutations int                        `json:"unaligned_failing_mutations"`
	PassingTrace              string                     `json:"passing_trace_id"`
	FailingTrace              string                     `json:"failing_trace_id"`
	PassingDigest             string                     `json:"passing_digest"`
	FailingDigest             string                     `json:"failing_digest"`
	CommonPrefix              int                        `json:"common_prefix_stops"`
	Alignment                 string                     `json:"alignment"`
	Differences               []ExecutionTraceDifference `json:"differences"`
	Coverage                  ExecutionCoverage          `json:"coverage"`
	CauseStatus               string                     `json:"cause_status"`
	Roles                     string                     `json:"roles"`
}

func CompareExecutionTraces(ctx context.Context, passing, failing ExecutionTrace) (ExecutionTraceComparison, error) {
	out := ExecutionTraceComparison{PassingTrace: passing.ID, FailingTrace: failing.ID, PassingDigest: passing.Digest, FailingDigest: failing.Digest,
		Alignment: "exact_source", Differences: []ExecutionTraceDifference{}, CauseStatus: "unknown", Roles: "passing/failing roles are supplied by the caller",
		Coverage: ExecutionCoverage{Gaps: []string{"one_execution_each", "sampled_alignment", "thread_identity_not_portable", "causal_origin_unproven"}, Limits: []string{}}}
	if passing.Finished == nil || failing.Finished == nil {
		return out, Codedf("trace_incomplete", "comparison requires two completed traces")
	}
	if passing.WorkspaceID != failing.WorkspaceID || passing.Epoch != failing.Epoch {
		return out, Codedf("trace_source_changed", "comparison requires the same workspace epoch")
	}
	if len(passing.Events) > MaxExecutionTraceEvents || len(failing.Events) > MaxExecutionTraceEvents {
		return out, Codedf("graph_budget_invalid", "comparison exceeds event ceilings")
	}
	out.Coverage.Capped = passing.Coverage.Capped || failing.Coverage.Capped
	out.Coverage.Gaps = uniqueSorted(append(append(out.Coverage.Gaps, passing.Coverage.Gaps...), failing.Coverage.Gaps...))
	out.Coverage.Limits = uniqueSorted(append(append([]string{}, passing.Coverage.Limits...), failing.Coverage.Limits...))
	a, b := comparisonStops(passing), comparisonStops(failing)
	for i := 0; i < min(len(a), len(b)); i++ {
		if ctx.Err() != nil {
			return out, Coded("analysis_cancelled", ctx.Err())
		}
		alignment := compareStopFrames(a[i], b[i])
		if alignment == "ambiguous" || alignment == "different_location" {
			out.Alignment = alignment
			out.addDifference(ExecutionTraceDifference{Kind: "control_frontier", PassingSequence: a[i].Sequence, FailingSequence: b[i].Sequence, Status: "unknown", Detail: "Earliest unaligned sampled stack; omitted intervals prevent exact control-flow or causal attribution."})
			break
		}
		if alignment == "token_relocated" {
			out.Alignment = alignment
		}
		out.CommonPrefix++
		if i > 0 && (a[i].Thread != a[i-1].Thread) != (b[i].Thread != b[i-1].Thread) {
			out.addDifference(ExecutionTraceDifference{Kind: "thread_schedule", PassingSequence: a[i].Sequence, FailingSequence: b[i].Sequence, Status: "unknown", Detail: "Thread-transition patterns differ; cross-run goroutine identities and causal ordering are unknown."})
		}
		compareStopEvidence(&out, a[i], b[i])
	}
	if out.CommonPrefix == min(len(a), len(b)) && len(a) != len(b) {
		out.addDifference(ExecutionTraceDifference{Kind: "one_sided_stops", Status: "observed", Detail: "One recording has additional retained stops; uninstrumented regions remain unknown."})
	}
	out.UnalignedPassingStops = len(a) - out.CommonPrefix
	out.UnalignedFailingStops = len(b) - out.CommonPrefix
	for _, event := range a[out.CommonPrefix:] {
		out.UnalignedPassingMutations += len(event.Mutations)
	}
	for _, event := range b[out.CommonPrefix:] {
		out.UnalignedFailingMutations += len(event.Mutations)
	}
	if passing.Completion != failing.Completion {
		out.addDifference(ExecutionTraceDifference{Kind: "completion", Status: "observed", Detail: "Capture completion reasons differ; passing/failing program outcome is not inferred."})
	}
	if passing.Dropped != 0 || failing.Dropped != 0 {
		out.Coverage.Gaps = uniqueSorted(append(out.Coverage.Gaps, "dropped_regions"))
	}
	return out, nil
}
func (out *ExecutionTraceComparison) addDifference(difference ExecutionTraceDifference) {
	if len(out.Differences) >= MaxExecutionComparisons {
		out.Coverage.Capped = true
		out.Coverage.Limits = uniqueSorted(append(out.Coverage.Limits, "trace_comparison_differences"))
		return
	}
	out.Differences = append(out.Differences, difference)
}
func comparisonStops(trace ExecutionTrace) []ExecutionTraceEvent {
	result := []ExecutionTraceEvent{}
	for _, event := range trace.Events {
		if event.Kind == "stopped" {
			result = append(result, event)
		}
	}
	return result
}
