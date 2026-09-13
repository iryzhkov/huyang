package workspace

import (
	"path/filepath"
	"strings"
	"time"
)

func traceRedacted(name string, policy ExecutionTracePolicy) bool {
	name = strings.ToLower(name)
	for _, part := range append([]string{"password", "passwd", "secret", "token", "credential", "private_key", "api_key", "authorization", "cookie"}, policy.RedactNames...) {
		if part != "" && strings.Contains(name, strings.ToLower(part)) {
			return true
		}
	}
	return false
}
func sanitizeTraceEvent(trace *ExecutionTrace, event *ExecutionTraceEvent) {
	event.At = time.Now().UTC()
	event.Kind = sanitizeText(event.Kind, 32)
	event.Reason = sanitizeText(event.Reason, 128)
	if len(event.Frames) > MaxExecutionTraceFrames {
		event.Frames = event.Frames[:MaxExecutionTraceFrames]
		trace.Coverage.Gaps = uniqueSorted(append(trace.Coverage.Gaps, "stack_truncated"))
	}
	for i := range event.Frames {
		frame := &event.Frames[i]
		if filepath.IsAbs(frame.Path) || strings.HasPrefix(filepath.Clean(frame.Path), "..") {
			frame.Path = ""
			frame.Mapping = "external"
		}
		frame.Path = sanitizeText(frame.Path, 512)
		frame.Name = sanitizeText(frame.Name, 256)
		frame.Mapping = sanitizeText(frame.Mapping, 64)
		if len(frame.SourceHandle) > 128 {
			frame.SourceHandle = ""
		}
	}
	if !trace.Policy.CaptureValues {
		event.Values = nil
		return
	}
	count, bytes := 0, 0
	for _, previous := range trace.Events {
		for _, value := range previous.Values {
			count++
			bytes += len(value.Value)
		}
	}
	kept := []ExecutionTraceValue{}
	for _, value := range event.Values {
		redacted := traceRedacted(value.Name, trace.Policy) || len(value.Name) > 128
		value.Name = sanitizeText(value.Name, 128)
		value.Type = sanitizeText(value.Type, 128)
		if redacted {
			value.Value = "[redacted]"
			value.Redacted = true
		}
		if len(value.Value) > MaxExecutionValueBytes {
			value.Value = sanitizeText(value.Value, MaxExecutionValueBytes)
			value.Truncated = true
		}
		if count >= MaxExecutionValues || bytes+len(value.Value) > MaxExecutionRetainedValueBytes {
			trace.DroppedValues++
			trace.Coverage.Capped = true
			trace.Coverage.Limits = uniqueSorted(append(trace.Coverage.Limits, "trace_values"))
			continue
		}
		count++
		bytes += len(value.Value)
		kept = append(kept, value)
	}
	event.Values = kept
}
