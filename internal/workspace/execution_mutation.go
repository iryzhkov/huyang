package workspace

import "go/constant"

type ExecutionMutation struct {
	Name     string `json:"name"`
	ObjectID string `json:"object_id"`
	Access   string `json:"access"`
	Evidence string `json:"evidence"`
	OldHash  string `json:"old_hash,omitempty"`
	NewHash  string `json:"new_hash,omitempty"`
	Values   string `json:"values"`
	Source   string `json:"source"`
}

func ExecutionWatchMutation(trace ExecutionTrace, raw map[string]any) ExecutionMutation {
	name, _ := raw["name"].(string)
	dataID, _ := raw["data_id"].(string)
	result := ExecutionMutation{Name: sanitizeText(name, 128), Access: "write", Evidence: "native_data_breakpoint_hit", Values: "unavailable",
		Source: "Stopped stack location; the adapter may report the instruction after the write."}
	if len(dataID) <= 1024 && dataID != "" {
		result.ObjectID = "object_" + hashBytes([]byte(trace.ID+":"+dataID))
	}
	if !trace.Policy.CaptureValues || traceRedacted(name, trace.Policy) || len(name) > 128 {
		result.Values = "redacted"
		return result
	}
	kind, _ := raw["type"].(string)
	for _, key := range []string{"old", "new"} {
		value, _ := raw[key].(string)
		if raw[key+"_known"] != true || len(value) > MaxExecutionValueBytes {
			continue
		}
		scalar := ExecutionTraceValue{Type: kind, Value: value}
		if traceScalar(scalar).Kind() == constant.Unknown {
			continue
		}
		hash := hashBytes([]byte(kind + ":" + value))
		if key == "old" {
			result.OldHash = hash
		} else {
			result.NewHash = hash
		}
	}
	if result.OldHash != "" || result.NewHash != "" {
		result.Values = "bounded_scalar_hashes"
	}
	return result
}
func sanitizeTraceMutations(trace *ExecutionTrace, event *ExecutionTraceEvent) {
	retained := 0
	for _, previous := range trace.Events {
		retained += len(previous.Mutations)
	}
	remaining := MaxExecutionValues - retained
	if remaining < 0 {
		remaining = 0
	}
	if len(event.Mutations) > remaining {
		event.Mutations = event.Mutations[:remaining]
		trace.Coverage.Capped = true
		trace.Coverage.Limits = uniqueSorted(append(trace.Coverage.Limits, "trace_mutations"))
	}
	for i := range event.Mutations {
		mutation := &event.Mutations[i]
		mutation.Name = sanitizeText(mutation.Name, 128)
		mutation.ObjectID = sanitizeText(mutation.ObjectID, 80)
		mutation.Source = sanitizeText(mutation.Source, 256)
		mutation.Access = sanitizeText(mutation.Access, 16)
		mutation.Evidence = sanitizeText(mutation.Evidence, 64)
		mutation.Values = sanitizeText(mutation.Values, 64)
		if len(mutation.OldHash) > 64 {
			mutation.OldHash = ""
		}
		if len(mutation.NewHash) > 64 {
			mutation.NewHash = ""
		}
		if traceRedacted(mutation.Name, trace.Policy) || !trace.Policy.CaptureValues {
			mutation.OldHash = ""
			mutation.NewHash = ""
			mutation.Values = "redacted"
		}
	}
}
