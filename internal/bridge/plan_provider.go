package bridge

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sync/atomic"

	"agent99/internal/provider"
	workspacecore "agent99/internal/workspace"
)

type providerPlanStager struct {
	workspaceID workspacecore.ID
	provider    provider.Provider
	epoch       uint64
	requests    atomic.Uint64
}

func (s *providerPlanStager) Epoch() uint64 {
	return s.epoch
}

func (s *providerPlanStager) Stage(ctx context.Context, request workspacecore.PlanStageRequest) error {
	files := make([]map[string]any, 0, len(request.Files))
	for _, file := range request.Files {
		files = append(files, map[string]any{
			"path": file.Path, "before_b64": base64.StdEncoding.EncodeToString(file.Before),
			"after_b64":     base64.StdEncoding.EncodeToString(file.After),
			"before_exists": file.BeforeExists, "after_exists": file.AfterExists,
		})
	}
	_, err := s.call(ctx, "huyang_prepare", request.PlanID, map[string]any{
		"plan_id": request.PlanID, "plan_revision": request.PlanRevision, "files": files,
	})
	return err
}

func (s *providerPlanStager) Rollback(ctx context.Context, planID string) error {
	_, err := s.call(ctx, "huyang_rollback", planID, map[string]any{"plan_id": planID})
	return err
}

func (s *providerPlanStager) call(ctx context.Context, operation, planID string, arguments map[string]any) (provider.Result, error) {
	deadline, _ := ctx.Deadline()
	return s.provider.Call(ctx, provider.Request{
		Context: provider.RequestContext{
			RequestID: fmt.Sprintf("huyang-plan-%d", s.requests.Add(1)), WorkspaceID: string(s.workspaceID),
			Epoch: s.provider.Descriptor().Epoch, TransactionID: planID, Deadline: deadline,
			Cancellation: s.provider.Descriptor().Cancellation,
		},
		Operation: operation, Arguments: arguments,
	})
}

func (d *directWorkspaces) planStager(workspace *workspacecore.Workspace) (workspacecore.PlanStager, error) {
	id := workspace.Identity().ID
	d.providerMu.Lock()
	defer d.providerMu.Unlock()
	if stager := d.stagers[id]; stager != nil {
		if backend := d.providers[id]; backend != nil && backend.Health(context.Background()).State == provider.HealthHealthy {
			return stager, nil
		}
		if backend := d.providers[id]; backend != nil {
			_ = backend.Close(context.Background())
		}
		delete(d.providers, id)
		delete(d.stagers, id)
		workspace.SyncProviderEpoch(workspace.Identity().Epoch + 1)
	}
	backend, err := referenceProviders.Open(providerOpenConfig{
		Root: workspace.Identity().Root, InitFile: os.Getenv("AGENT99_HEADLESS_INIT"),
		RuntimePath: shippedRuntimePath(), Debug: false,
	})
	if err != nil {
		return nil, err
	}
	epoch := workspace.Identity().Epoch
	if backend.Descriptor().Epoch > epoch {
		epoch = backend.Descriptor().Epoch
		workspace.SyncProviderEpoch(epoch)
	}
	stager := &providerPlanStager{workspaceID: id, provider: backend, epoch: epoch}
	d.stagers[id] = stager
	d.providers[id] = backend
	return stager, nil
}

func (d *directWorkspaces) closeProviders() {
	d.providerMu.Lock()
	defer d.providerMu.Unlock()
	for id, backend := range d.providers {
		_ = backend.Close(context.Background())
		delete(d.providers, id)
		delete(d.stagers, id)
	}
}
