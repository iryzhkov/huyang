package workspace

type ExecutionValueSample struct {
	Sequence   int                 `json:"sequence"`
	Thread     int                 `json:"thread"`
	Frame      ExecutionTraceFrame `json:"frame"`
	Value      ExecutionTraceValue `json:"value"`
	Difference string              `json:"difference"`
}
type ExecutionMutationObservation struct {
	Sequence int                 `json:"sequence"`
	Thread   int                 `json:"thread"`
	Frame    ExecutionTraceFrame `json:"frame"`
	Mutation ExecutionMutation   `json:"mutation"`
}
type ExecutionValueOrigin struct {
	Mutations   []ExecutionMutationObservation `json:"mutations"`
	TraceID     string                         `json:"trace_id"`
	TraceDigest string                         `json:"trace_digest"`
	Name        string                         `json:"name"`
	Status      string                         `json:"status"`
	Identity    string                         `json:"identity"`
	Samples     []ExecutionValueSample         `json:"samples"`
	Coverage    ExecutionCoverage              `json:"coverage"`
	Reason      string                         `json:"reason"`
}

// A local name and stack location are not an object identity. Repeated
// activations, aliases and unsampled writes prevent an exact backward slice.
func TraceValueOrigin(trace ExecutionTrace, name string, thread int) (ExecutionValueOrigin, error) {
	out := ExecutionValueOrigin{TraceID: trace.ID, TraceDigest: trace.Digest, Name: name, Status: "unknown", Identity: "unresolved",
		Samples: []ExecutionValueSample{}, Mutations: []ExecutionMutationObservation{}, Coverage: cloneExecution(trace.Coverage),
		Reason: "Retained local samples do not identify an object, assignment, final write, argument/return transfer or causal origin."}
	if name == "" || len(name) > 128 || thread < 0 {
		return out, Codedf("trace_value_invalid", "use an exact local name of 1..128 bytes and an optional nonnegative thread")
	}
	if trace.Finished == nil {
		return out, Codedf("trace_incomplete", "finish or stop the trace before inspecting value origin")
	}
	if len(trace.Events) > MaxExecutionTraceEvents {
		return out, Codedf("graph_budget_invalid", "trace exceeds event ceiling")
	}
	out.Coverage.Complete = false
	out.Coverage.Gaps = uniqueSorted(append(out.Coverage.Gaps, "object_identity_unavailable", "unobserved_intervals", "activation_and_alias_uncertainty"))
	if !trace.Policy.CaptureValues {
		out.Reason = "This trace did not retain values. Start an explicitly value-enabled trace; exact mutation capture also requires a validated adapter."
		return out, nil
	}
	for _, event := range trace.Events {
		if event.Kind != "stopped" || len(event.Frames) == 0 || thread != 0 && event.Thread != thread {
			continue
		}
		for _, mutation := range event.Mutations {
			if mutation.Name == name && len(out.Mutations) < MaxExecutionValues {
				out.Mutations = append(out.Mutations, ExecutionMutationObservation{Sequence: event.Sequence, Thread: event.Thread, Frame: event.Frames[0], Mutation: mutation})
			}
		}
		for _, value := range event.Values {
			if value.Name != name {
				continue
			}
			if len(out.Samples) >= MaxExecutionValues {
				out.Coverage.Capped = true
				out.Coverage.Limits = uniqueSorted(append(out.Coverage.Limits, "value_origin_samples"))
				return out, nil
			}
			sample := ExecutionValueSample{Sequence: event.Sequence, Thread: event.Thread, Frame: event.Frames[0], Value: value, Difference: "unknown"}
			if len(out.Samples) > 0 {
				sample.Difference = sampleValueDifference(out.Samples[len(out.Samples)-1], sample)
			}
			out.Samples = append(out.Samples, sample)
		}
	}
	return out, nil
}
func sampleValueDifference(previous, current ExecutionValueSample) string {
	if previous.Thread != current.Thread || previous.Frame.Path != current.Frame.Path || previous.Frame.Name != current.Frame.Name ||
		previous.Frame.SourceHash != current.Frame.SourceHash || previous.Frame.Mapping != "launch_snapshot" || current.Frame.Mapping != "launch_snapshot" ||
		previous.Value.Type != current.Value.Type || previous.Value.Redacted || current.Value.Redacted || previous.Value.Truncated || current.Value.Truncated {
		return "unknown"
	}
	if previous.Value.Value == current.Value.Value {
		return "same_sampled_value"
	}
	return "different_sampled_value"
}
