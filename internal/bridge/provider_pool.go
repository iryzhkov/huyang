package bridge

import (
	"context"
	"fmt"
	"sync"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// providerSlot owns the canonical provider of one workspace. Spawning and
// health-checking a provider can take seconds, so each slot has its own lock:
// d.providerMu only guards the slot map and is never held across a provider
// operation, which keeps one workspace's slow spawn from stalling every other
// workspace's provider lookups.
type providerSlot struct {
	mu      sync.Mutex
	backend provider.Provider
}

func (d *directWorkspaces) providerSlotFor(id workspacecore.ID) *providerSlot {
	d.providerMu.Lock()
	defer d.providerMu.Unlock()
	slot := d.providers[id]
	if slot == nil {
		slot = &providerSlot{}
		d.providers[id] = slot
	}
	return slot
}

// canonicalProvider returns the owned Neovim provider for a modern workspace.
// Modern workspaces are durable while provider processes are replaceable, so
// the provider is started lazily and recreated after a daemon or provider
// restart without changing the workspace ID.
func (d *directWorkspaces) canonicalProvider(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	return d.workspaceProvider(ctx, workspace, false)
}

func (d *directWorkspaces) workspaceProvider(ctx context.Context, workspace *workspacecore.Workspace, debug bool) (provider.Provider, error) {
	identity := workspace.Identity()
	if identity.Kind != workspacecore.KindProject {
		return nil, fmt.Errorf("semantic provider requires a project workspace")
	}
	slot := d.providerSlotFor(identity.ID)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if existing := slot.backend; existing != nil {
		health := existing.Health(ctx)
		if health.State == provider.HealthHealthy || health.State == provider.HealthStarting {
			workspace.SyncProviderEpoch(existing.Descriptor().Epoch)
			return existing, nil
		}
		_ = existing.Close(context.Background())
		slot.backend = nil
	}
	backend, err := openReferenceProvider(identity.Root, debug)
	if err != nil {
		return nil, err
	}
	slot.backend = backend
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	return backend, nil
}

func (d *directWorkspaces) restartCanonicalProvider(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	slot := d.providerSlotFor(workspace.Identity().ID)
	slot.mu.Lock()
	if existing := slot.backend; existing != nil {
		_ = existing.Close(context.Background())
		slot.backend = nil
	}
	slot.mu.Unlock()
	return d.canonicalProvider(ctx, workspace)
}

// closeProviders shuts every provider and rolls back every sandbox stager.
// The maps are detached under d.providerMu and the slow work happens outside
// it so a stager still inside a long operation cannot stall the map.
func (d *directWorkspaces) closeProviders() {
	d.providerMu.Lock()
	slots := d.providers
	stagers := d.sandboxStagers
	d.providers = make(map[workspacecore.ID]*providerSlot)
	d.sandboxStagers = make(map[stagerKey]*sandboxPlanStager)
	d.providerMu.Unlock()
	for _, slot := range slots {
		slot.mu.Lock()
		if slot.backend != nil {
			_ = slot.backend.Close(context.Background())
			slot.backend = nil
		}
		slot.mu.Unlock()
	}
	for key, stager := range stagers {
		_ = stager.Rollback(context.Background(), key.planID)
	}
}

// openReferenceProvider starts one owned Neovim provider rooted at root with
// the shipped runtime and the configured headless init file. Every provider
// the bridge spawns, canonical, debug, sandbox or verification, goes through
// this constructor so the configuration cannot drift between call sites.
func openReferenceProvider(root string, debug bool) (provider.Provider, error) {
	return referenceProviders.Open(providerOpenConfig{
		Root: root, InitFile: huyangHeadlessInit(),
		RuntimePath: shippedRuntimePath(), Debug: debug,
	})
}

func (d *directWorkspaces) resyncCanonicalProvider(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	backend, err := d.canonicalProvider(ctx, workspace)
	if err == nil {
		_, err = callCanonicalProvider(ctx, "provider_resync", workspace, backend, "workspace_resync", map[string]any{"root": workspace.Identity().Root})
	}
	if err == nil {
		return backend, nil
	}
	return d.restartCanonicalProvider(ctx, workspace)
}

// debugProvider returns the workspace provider, starting it in debug mode
// when no provider is running yet. A healthy canonical provider is reused.
func (d *directWorkspaces) debugProvider(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	return d.workspaceProvider(ctx, workspace, true)
}
