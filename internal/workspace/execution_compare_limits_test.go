package workspace

import (
	"context"
	"testing"
)

func TestExecutionComparisonCapsDifferencesAndRetainsMutationSuffix(t *testing.T) {
	a, b := comparisonTestTrace("a"), comparisonTestTrace("b")
	a.Events = nil
	b.Events = nil
	for i := 0; i < MaxExecutionComparisons+2; i++ {
		left, right := comparisonTestTrace("a").Events[0], comparisonTestTrace("b").Events[0]
		left.Sequence = i + 1
		right.Sequence = i + 1
		right.Values[0].Value = "2"
		a.Events = append(a.Events, left)
		b.Events = append(b.Events, right)
	}
	result, err := CompareExecutionTraces(context.Background(), a, b)
	if err != nil || !result.Coverage.Capped || len(result.Differences) != MaxExecutionComparisons {
		t.Fatalf("%+v %v", result, err)
	}
	b.Events[0].Frames[0].Name = "other"
	b.Events[1].Mutations = []ExecutionMutation{{Name: "n", Evidence: "native_data_breakpoint_hit"}}
	result, err = CompareExecutionTraces(context.Background(), a, b)
	if err != nil || result.UnalignedFailingMutations != 1 || result.CauseStatus != "unknown" {
		t.Fatalf("%+v %v", result, err)
	}
}
