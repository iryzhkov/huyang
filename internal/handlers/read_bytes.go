package handlers

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Bounds apply to each delivered source selection, including numbering.
// Existing clients remain uncapped unless they request a byte window.
const (
	DefaultReadBytes = 64 << 10
	MaxReadBytes     = 1 << 20
)

func (h *Handlers) readOne(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
	return readWithByteWindow(requestID, workspace, request, func(r readRequest) map[string]any {
		return h.readOneUnbounded(ctx, requestID, workspace, r)
	})
}

func readWithByteWindow(requestID string, workspace *workspacecore.Workspace, request readRequest, read func(readRequest) map[string]any) map[string]any {
	if request.Compact && request.MaxBytes == 0 && (request.View == "" || request.View == "source") {
		request.MaxBytes = DefaultReadBytes
	}
	if request.MaxBytes == 0 && request.ByteOffset == 0 && request.ExpectedRevision == "" {
		return read(request)
	}
	if request.MaxBytes == 0 {
		request.MaxBytes = DefaultReadBytes
	}
	if request.MaxBytes < 4 || request.MaxBytes > MaxReadBytes || request.ByteOffset < 0 {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_byte_window", "max_bytes must be 4..1048576 and byte_offset must be nonnegative", nil)
	}
	if request.View != "" && request.View != "source" {
		return mcpapi.Envelope(requestID, workspace, "failed", "byte_window_requires_source", "Byte windows apply to source reads; use view=source", nil)
	}
	result := read(request)
	if result["outcome"] != "ok" {
		return result
	}
	return boundReadResult(requestID, workspace, request, result)
}

func boundReadResult(requestID string, workspace *workspacecore.Workspace, request readRequest, result map[string]any) map[string]any {
	data, ok := result["data"].(map[string]any)
	if !ok {
		return result
	}
	content, ok := data["content"].(string)
	if !ok {
		return result
	}
	revision := fmt.Sprint(data["revision_id"])
	if request.ExpectedRevision != "" && request.ExpectedRevision != revision {
		return byteReadRefusal(requestID, workspace, "read_revision_changed", "Source changed; restart the byte window at offset zero with its current revision", revision)
	}
	if request.ByteOffset > 0 && request.ExpectedRevision == "" {
		return byteReadRefusal(requestID, workspace, "read_revision_required", "Continuing a byte window requires expected_revision_id from the preceding read", revision)
	}
	start := request.ByteOffset
	if start > len(content) || (start < len(content) && !utf8.RuneStart(content[start])) {
		return byteReadRefusal(requestID, workspace, "invalid_byte_offset", "byte_offset must be within the selected content at a UTF-8 character boundary", revision)
	}
	end := min(len(content), start+request.MaxBytes)
	for end < len(content) && end > start && !utf8.RuneStart(content[end]) {
		end--
	}
	data["content"] = content[start:end]
	data["selection_bytes"], data["delivered_bytes"] = len(content), end-start
	data["byte_offset"], data["byte_end"] = start, end
	data["max_bytes"] = request.MaxBytes
	if end < len(content) {
		data["truncated"], data["byte_truncated"] = true, true
		data["next_byte_offset"] = end
		data["continuation"] = map[string]any{"byte_offset": end, "expected_revision_id": revision, "max_bytes": request.MaxBytes}
		result["summary"] = fmt.Sprintf("%v; truncated at max_bytes (%d of %d selected bytes)", result["summary"], end-start, len(content))
		result["next"] = append([]any{map[string]any{
			"tool": "read", "action": "repeat_same_target_and_options_with_byte_offset",
			"byte_offset": end, "expected_revision_id": revision, "max_bytes": request.MaxBytes,
		}}, mcpapi.AnySlice(result["next"])...)
	}
	return result
}

func byteReadRefusal(requestID string, workspace *workspacecore.Workspace, code, summary, revision string) map[string]any {
	result := mcpapi.Envelope(requestID, workspace, "conflict", code, summary, map[string]any{"revision_id": revision})
	result["next"] = []any{map[string]any{"tool": "read", "action": "restart_same_target_at_byte_zero", "byte_offset": 0, "expected_revision_id": revision}}
	return result
}
