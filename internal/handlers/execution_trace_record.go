package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *Handlers) recordDebugTrace(ctx context.Context, requestID string, w *workspacecore.Workspace, name, action, id string, result map[string]any) {
	if name != "debug_control" && name != "debug_session" {
		return
	}
	payload, _ := result["data"].(map[string]any)
	data, _ := payload["debug"].(map[string]any)
	kind, _ := data["state"].(string)
	completed := kind == "exited" || name == "debug_session" && action == "stop"
	reason := kind
	if name == "debug_session" && action == "stop" {
		reason = "stopped_by_request"
	}
	if result["outcome"] == "failed" || result["outcome"] == "unavailable" || ctx.Err() != nil {
		completed = true
		reason = "debug_operation_failed"
	}
	if completed || kind == "stopped" {
		event, gaps := debugTraceEvent(ctx, w, id, data, kind)
		if completed {
			event.Kind = "capture_finished"
			event.Reason = reason
			if reason != "exited" {
				gaps = append(gaps, "capture_interrupted")
			}
		}
		if name == "debug_session" && (action == "start" || action == "attach") {
			if !h.verifyTraceTargets(ctx, w, id) {
				gaps = append(gaps, "trace_targets_unverified")
			}
		}
		if completed && !h.cleanupTraceTargets(w, id) {
			gaps = append(gaps, "temporary_breakpoint_cleanup_incomplete")
		}
		err := w.AppendExecutionTrace(event, gaps...)
		if err == nil && completed {
			_, err = w.FinishExecutionTrace(reason)
		}
		if err != nil {
			if payload != nil {
				payload["trace_error"] = workspacecore.ErrorCode(err)
			}
			result["outcome"] = "partial"
		}
	}
	delete(data, "trace_values")
	delete(data, "trace_frames")
	delete(data, "trace_sequence")
	delete(data, "trace_capture")
	delete(data, "trace_mutation")
	if payload != nil {
		payload["trace_id"] = id
	}
}
func debugTraceEvent(ctx context.Context, w *workspacecore.Workspace, id string, data map[string]any, kind string) (workspacecore.ExecutionTraceEvent, []string) {
	event := workspacecore.ExecutionTraceEvent{Kind: kind, Sequence: argInt(data, "trace_sequence", 0), Thread: argInt(data, "thread", 0), Frames: []workspacecore.ExecutionTraceFrame{}}
	event.Reason, _ = data["reason"].(string)
	trace, err := w.ExecutionTrace(id)
	if err != nil {
		return event, []string{"trace_header_unavailable"}
	}
	captureCtx, cancel := context.WithTimeout(ctx, time.Duration(workspacecore.MaxExecutionAnalysisMillis)*time.Millisecond)
	defer cancel()
	sources, revision, _, err := w.ExecutionSources(captureCtx)
	known := map[string]workspacecore.ExecutionSource{}
	for _, source := range sources {
		known[source.Path] = source
	}
	gaps := []string{}
	stale := err != nil || revision != trace.Revision || w.Identity().Epoch != trace.Epoch
	if stale {
		gaps = append(gaps, "source_changed_since_launch")
	}
	frames, _ := data["trace_frames"].([]any)
	if len(frames) == 0 && kind == "stopped" {
		gaps = append(gaps, "stack_unavailable")
	}
	for _, raw := range frames {
		if len(event.Frames) >= workspacecore.MaxExecutionTraceFrames {
			gaps = append(gaps, "stack_truncated")
			break
		}
		frame, _ := raw.(map[string]any)
		file, _ := frame["file"].(string)
		name, _ := frame["name"].(string)
		if before, _, found := strings.Cut(name, "("); found {
			name = before
		}
		path := relativeTracePath(w, file)
		location := workspacecore.ExecutionTraceFrame{Path: path, Line: argInt(frame, "line", 0), Column: argInt(frame, "column", 1), Name: name, Mapping: "external"}
		if source, ok := known[path]; ok && trace.SourceHashes[path] == source.Hash && !stale {
			location.SourceHash = source.Hash
			handle, err := w.ExecutionSourceHandle(source, location.Line, 1)
			if err == nil {
				location.SourceHandle = handle
				location.Mapping = "launch_snapshot"
			} else {
				location.Mapping = "ambiguous"
				gaps = append(gaps, "source_location_ambiguous")
			}
		} else if path != "" {
			location.SourceHash = trace.SourceHashes[path]
			location.Mapping = "stale"
		}
		event.Frames = append(event.Frames, location)
	}
	if trace.Policy.CaptureValues {
		locals, _ := data["trace_values"].([]any)
		for _, raw := range locals {
			if len(event.Values) >= workspacecore.MaxExecutionValues {
				break
			}
			local, _ := raw.(map[string]any)
			name, _ := local["name"].(string)
			value, _ := local["value"].(string)
			event.Values = append(event.Values, workspacecore.ExecutionTraceValue{Name: name, Value: value, Type: fmt.Sprint(local["type"])})
		}
	}
	if raw, ok := data["trace_mutation"].(map[string]any); ok {
		event.Mutations = []workspacecore.ExecutionMutation{workspacecore.ExecutionWatchMutation(trace, raw)}
	}
	return event, gaps
}
