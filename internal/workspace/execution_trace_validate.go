package workspace

import "path/filepath"

func (s *executionTraceStore) validRecoveredTrace(trace ExecutionTrace) bool {
	if !validExecutionTraceMetadata(trace) {
		return false
	}
	if string(trace.WorkspaceID) != filepath.Base(s.directory) || len(trace.SourceHashes) > MaxExecutionNodes || len(trace.Targets) > MaxExecutionTraceTargets {
		return false
	}
	if _, err := NormalizeExecutionTracePolicy(trace.Policy); err != nil {
		return false
	}
	values, bytes, mutations := 0, 0, 0
	for _, event := range trace.Events {
		if len(event.Frames) > MaxExecutionTraceFrames {
			return false
		}
		mutations += len(event.Mutations)
		for _, mutation := range event.Mutations {
			if len(mutation.Name) > 128 || len(mutation.ObjectID) > 80 || len(mutation.OldHash) > 64 || len(mutation.NewHash) > 64 {
				return false
			}
		}
		for _, value := range event.Values {
			values++
			bytes += len(value.Value)
			if len(value.Value) > MaxExecutionValueBytes {
				return false
			}
		}
	}
	if values > MaxExecutionValues || mutations > MaxExecutionValues || bytes > MaxExecutionRetainedValueBytes {
		return false
	}
	if trace.Finished != nil {
		digest := trace.Digest
		trace.Digest = ""
		return digest != "" && hashBytes([]byte(executionJSON(trace))) == digest
	}
	return trace.Digest == ""
}
