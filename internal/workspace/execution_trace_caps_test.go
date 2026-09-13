package workspace

import (
	"strings"
	"testing"
)

func TestExecutionTraceValueBoundsAndStopLoss(t *testing.T) {
	for _, size := range []int{1, MaxExecutionValueBytes} {
		w := traceTestWorkspace(t, t.TempDir(), t.TempDir())
		id, err := w.BeginExecutionTrace(ExecutionTrace{Policy: ExecutionTracePolicy{CaptureValues: true, MaxEvents: 1}})
		if err != nil {
			t.Fatal(err)
		}
		event := ExecutionTraceEvent{Kind: "stopped", Sequence: 3, Frames: []ExecutionTraceFrame{{Path: "main.go"}}}
		for i := 0; i < MaxExecutionValues+1; i++ {
			event.Values = append(event.Values, ExecutionTraceValue{Name: "n", Value: strings.Repeat("v", size)})
		}
		if err = w.AppendExecutionTrace(event); err != nil {
			t.Fatal(err)
		}
		event.Frames[0].Path = "changed.go"
		event.Sequence = 4
		if err = w.AppendExecutionTrace(event); err != nil {
			t.Fatal(err)
		}
		if err = w.AppendExecutionTrace(event); err != nil {
			t.Fatal(err)
		}
		trace, err := w.ExecutionTrace(id)
		if err != nil {
			t.Fatal(err)
		}
		if trace.Dropped != 3 || trace.LastStopSequence != 4 || trace.Events[0].Frames[0].Path != "main.go" {
			t.Fatalf("stop accounting or input alias: %+v", trace)
		}
		expected := min(MaxExecutionValues, MaxExecutionRetainedValueBytes/size)
		if len(trace.Events[0].Values) != expected || trace.DroppedValues == 0 {
			t.Fatalf("value bounds: retained=%d dropped=%d", len(trace.Events[0].Values), trace.DroppedValues)
		}
	}
}
