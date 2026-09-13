package workspace

import (
	"testing"
	"time"
)

func TestTraceValueOriginDoesNotInventMutationIdentity(t *testing.T) {
	now := time.Now()
	trace := ExecutionTrace{ID: "t", Finished: &now, Policy: ExecutionTracePolicy{CaptureValues: true}}
	for i := 1; i <= 3; i++ {
		thread := 1
		if i == 3 {
			thread = 2
		}
		trace.Events = append(trace.Events, ExecutionTraceEvent{Kind: "stopped", Sequence: i, Thread: thread,
			Frames: []ExecutionTraceFrame{{Path: "x.go", Name: "main.f", SourceHash: "h", Mapping: "launch_snapshot"}},
			Values: []ExecutionTraceValue{{Name: "n", Type: "int", Value: string(rune('0' + i))}}})
	}
	origin, err := TraceValueOrigin(trace, "n", 0)
	if err != nil || origin.Status != "unknown" || origin.Identity != "unresolved" || len(origin.Samples) != 3 {
		t.Fatalf("%+v %v", origin, err)
	}
	if origin.Samples[1].Difference != "different_sampled_value" || origin.Samples[2].Difference != "unknown" {
		t.Fatalf("%+v", origin)
	}
	trace.Events[1].Values[0].Redacted = true
	origin, err = TraceValueOrigin(trace, "n", 1)
	if err != nil || len(origin.Samples) != 2 || origin.Samples[1].Difference != "unknown" {
		t.Fatalf("%+v %v", origin, err)
	}
	trace.Policy.CaptureValues = false
	origin, err = TraceValueOrigin(trace, "n", 0)
	if err != nil || len(origin.Samples) != 0 {
		t.Fatalf("%+v %v", origin, err)
	}
	trace.Finished = nil
	if _, err = TraceValueOrigin(trace, "n", 0); ErrorCode(err) != "trace_incomplete" {
		t.Fatal(err)
	}
}
