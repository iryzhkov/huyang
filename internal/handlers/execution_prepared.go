package handlers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func bindPreparedExecutionFacts(ctx context.Context, w *workspacecore.Workspace, view providerpool.PreparedView, sources []workspacecore.ExecutionSource, facts *workspacecore.ExecutionCallFacts) {
	known := map[string]workspacecore.ExecutionSource{}
	for _, source := range sources {
		known[source.Path] = source
	}
	bind := func(path string, line, column int, evidence []workspacecore.ExecutionEvidence) {
		if ctx.Err() != nil {
			facts.Coverage.Capped = true
			facts.Coverage.Complete = false
			facts.Coverage.Limits = []string{"analysis_time"}
			return
		}
		source, ok := known[path]
		if !ok {
			return
		}
		handle, err := w.PreparedExecutionSourceHandle(source, view.PreparedRevision, line, column)
		if err != nil {
			facts.Coverage.Gaps = append(facts.Coverage.Gaps, "prepared_source_handle_unavailable")
			return
		}
		for i := range evidence {
			evidence[i].SourceHandles = []string{handle}
		}
	}
	for i := range facts.Nodes {
		n := &facts.Nodes[i]
		bind(n.Path, n.Line, n.Column, n.Evidence)
	}
	for i := range facts.Edges {
		e := &facts.Edges[i]
		bind(e.SitePath, e.SiteLine, e.SiteColumn, e.Evidence)
	}
}
func (h *Handlers) executionPrepared(ctx context.Context, requestID, name string, w *workspacecore.Workspace, view providerpool.PreparedView, args map[string]any) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(workspacecore.MaxExecutionAnalysisMillis)*time.Millisecond)
	defer cancel()
	if name == "path_explain" {
		return h.pathExplain(ctx, requestID, w, args, &view)
	}
	q, err := h.captureExecution(ctx, w, args, &view)
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	return executionGraphEnvelope(requestID, w, q)
}
func (h *Handlers) readPreparedExecutionHandle(requestID string, w *workspacecore.Workspace, view providerpool.PreparedView, request readRequest) map[string]any {
	record, err := w.InspectHandle(workspacecore.HandleID(request.Handle))
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	if record.PreparedRevision != view.PreparedRevision {
		return mcpapi.Failure(requestID, w, "handle_revision_mismatch", workspacecore.Codedf("handle_revision_mismatch", "source handle belongs to another revision"))
	}
	path, err := preparedPath(view, record.Locator.Path)
	if err != nil {
		return mcpapi.Failure(requestID, w, "graph_source_invalid", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(content)) != record.Locator.ContentSHA256 {
		return mcpapi.Failure(requestID, w, "graph_source_changed", workspacecore.Codedf("graph_source_changed", "prepared source no longer matches the handle"))
	}
	if record.Locator.ByteStart < 0 || record.Locator.ByteEnd > len(content) {
		return mcpapi.Failure(requestID, w, "graph_source_invalid", workspacecore.Codedf("graph_source_invalid", "source range outside file"))
	}
	request.Path = record.Locator.Path
	request.StartLine = strings.Count(string(content[:record.Locator.ByteStart]), "\n") + 1
	request.EndLine = request.StartLine
	request.Handle = ""
	return h.readPrepared(requestID, w, view, request)
}
