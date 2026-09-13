package handlers

import (
	"context"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestExecutionCancelledValidationDoesNotClaimSourceChanged(t *testing.T) {
	h, id, _ := literalFixture(t, map[string]string{"main.go": "package p\nfunc F() {}\n"})
	w := h.registry.Lookup(workspacecore.ID(id))
	_, revision, _, err := w.ExecutionSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q := executionQuery{workspace: w, sourceWorkspace: w, epoch: w.Identity().Epoch, request: workspacecore.ExecutionRequest{Key: workspacecore.AnalysisKey{Revision: revision}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = q.validate(ctx, h)
	if workspacecore.ErrorCode(err) != "analysis_cancelled" {
		t.Fatalf("cancellation misclassified: %v", err)
	}
}
