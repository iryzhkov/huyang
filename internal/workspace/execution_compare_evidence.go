package workspace

func compareStopFrames(a, b ExecutionTraceEvent) string {
	if len(a.Frames) == 0 || len(a.Frames) != len(b.Frames) || len(a.Frames) > MaxExecutionTraceFrames {
		return "ambiguous"
	}
	alignment := "exact_source"
	mapped := false
	for i, x := range a.Frames {
		y := b.Frames[i]
		if x.Path == "" && y.Path == "" {
			if x.Name != y.Name {
				return "ambiguous"
			}
			continue
		}
		if x.Mapping != "launch_snapshot" || y.Mapping != "launch_snapshot" || x.SourceHash == "" || y.SourceHash == "" {
			return "ambiguous"
		}
		if x.Path != y.Path || x.Name != y.Name {
			return "different_location"
		}
		if x.SourceHash == y.SourceHash {
			if x.Line != y.Line {
				return "different_location"
			}
		} else {
			if x.FunctionHash == "" || x.FunctionHash != y.FunctionHash {
				return "ambiguous"
			}
			if x.TokenOffset == 0 || x.TokenOffset != y.TokenOffset {
				return "different_location"
			}
			alignment = "token_relocated"
		}
		mapped = true
	}
	if !mapped {
		return "ambiguous"
	}
	return alignment
}
func compareStopEvidence(out *ExecutionTraceComparison, a, b ExecutionTraceEvent) {
	add := func(kind, name, status, detail string) {
		out.addDifference(ExecutionTraceDifference{Kind: kind, Name: name, Status: status, Detail: detail, PassingSequence: a.Sequence, FailingSequence: b.Sequence})
	}
	if a.Reason != b.Reason {
		add("stop_reason", "", "observed", "Debugger stop reasons differ.")
	}
	left, right := comparisonValues(a.Values), comparisonValues(b.Values)
	names := []string{}
	for name := range left {
		names = append(names, name)
	}
	for name := range right {
		names = append(names, name)
	}
	for _, name := range uniqueSorted(names) {
		x, xok := left[name]
		y, yok := right[name]
		if !xok || !yok || x.Redacted || y.Redacted || x.Truncated || y.Truncated || x.Type != y.Type {
			add("value", name, "unknown", "Missing, redacted, truncated, ambiguous or incompatible operand; no difference inferred.")
		} else if x.Value != y.Value {
			add("value", name, "observed", "Aligned local samples differ; causal relevance is unknown without dependency and origin evidence.")
		}
	}
	if len(a.Mutations) != len(b.Mutations) {
		add("mutation", "", "observed", "Native watch-hit counts differ at aligned stops; watched address identities do not transfer between executions.")
	} else {
		for i, x := range a.Mutations {
			y := b.Mutations[i]
			if x.Name != y.Name || x.OldHash != y.OldHash || x.NewHash != y.NewHash {
				add("mutation", x.Name, "unknown", "Watch evidence differs; redaction, missing values and object identity prevent a causal equivalence claim.")
			}
		}
	}
}
func comparisonValues(values []ExecutionTraceValue) map[string]ExecutionTraceValue {
	result := map[string]ExecutionTraceValue{}
	for i, value := range values {
		if i >= MaxExecutionValues {
			break
		}
		if _, exists := result[value.Name]; exists {
			value.Redacted = true
		}
		result[value.Name] = value
	}
	return result
}
