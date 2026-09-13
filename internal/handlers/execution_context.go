package handlers

import (
	"context"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Interrupted acquisition is not evidence that source bytes changed.
func executionContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return workspacecore.Codedf("analysis_cancelled", "execution analysis interrupted (%v); retry in a smaller fixture/module workspace to stay within the analysis budget", err)
	}
	return nil
}
