package handlers

import (
	"context"
	"fmt"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *Handlers) resolvePlanSymbolLocators(ctx context.Context, requestID string, workspace *workspacecore.Workspace, operations []workspacecore.PlanOperation) error {
	for _, operation := range operations {
		if operation.Target == nil || operation.Target.SymbolLocator == nil {
			continue
		}
		locator := operation.Target.SymbolLocator
		if _, err := workspace.ResolveSymbolLocator(locator.Path, locator.NamePath); err == nil {
			continue
		}
		// Register the same durable provider-backed handles symbol_find exposes
		// before the workspace normalizes plan operations to exact ranges.
		h.symbolFind(ctx, requestID+"_resolve_"+operation.OpID, workspace, map[string]any{"query": locator.NamePath})
		if _, err := workspace.ResolveSymbolLocator(locator.Path, locator.NamePath); err != nil {
			return fmt.Errorf("%s: %w", operation.OpID, err)
		}
	}
	return nil
}
