package bridge

import (
	"context"
	"fmt"
	"sync"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// providerPool owns every provider the service spawns: the canonical (or
// debug) provider of each workspace and the sandbox stagers that prepare
// plans in isolation. It is the single place a provider is opened, so the
// runtime path and init file cannot drift between call sites.
type providerPool struct {
	factory     providerFactory
	sandboxBase string

	// mu guards only the two maps below; provider and stager operations run
	// outside it (see providerSlot and sandboxPlanStager).
	mu        sync.Mutex
	providers map[workspacecore.ID]*providerSlot
	stagers   map[stagerKey]*sandboxPlanStager
}

func newProviderPool(sandboxBase string, factory providerFactory) *providerPool {
	return &providerPool{
		factory:     factory,
		sandboxBase: sandboxBase,
		providers:   make(map[workspacecore.ID]*providerSlot),
		stagers:     make(map[stagerKey]*sandboxPlanStager),
	}
}

// providerSlot owns the canonical provider of one workspace. Spawning and
// health-checking a provider can take seconds, so each slot has its own lock:
// the pool's mu only guards the slot map and is never held across a provider
// operation, which keeps one workspace's slow spawn from stalling every other
// workspace's provider lookups.
type providerSlot struct {
	mu      sync.Mutex
	backend provider.Provider
}

func (p *providerPool) slotFor(id workspacecore.ID) *providerSlot {
	p.mu.Lock()
	defer p.mu.Unlock()
	slot := p.providers[id]
	if slot == nil {
		slot = &providerSlot{}
		p.providers[id] = slot
	}
	return slot
}

// open starts one owned Neovim provider rooted at root with the shipped
// runtime and the configured headless init file. Every provider the pool
// spawns, canonical, debug, sandbox or verification, goes through this
// constructor.
func (p *providerPool) open(root string, debug bool) (provider.Provider, error) {
	return p.factory.Open(providerOpenConfig{
		Root: root, InitFile: huyangHeadlessInit(),
		RuntimePath: shippedRuntimePath(), Debug: debug,
	})
}

// canonical returns the owned Neovim provider for a project workspace.
// Workspaces are durable while provider processes are replaceable, so the
// provider is started lazily and recreated after a daemon or provider
// restart without changing the workspace ID.
func (p *providerPool) canonical(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	return p.workspaceProvider(ctx, workspace, false)
}

// debug returns the workspace provider, starting it in debug mode when no
// provider is running yet. A healthy canonical provider is reused.
func (p *providerPool) debug(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	return p.workspaceProvider(ctx, workspace, true)
}

func (p *providerPool) workspaceProvider(ctx context.Context, workspace *workspacecore.Workspace, debug bool) (provider.Provider, error) {
	identity := workspace.Identity()
	if identity.Kind != workspacecore.KindProject {
		return nil, fmt.Errorf("semantic provider requires a project workspace")
	}
	slot := p.slotFor(identity.ID)
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
	backend, err := p.open(identity.Root, debug)
	if err != nil {
		return nil, err
	}
	slot.backend = backend
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	return backend, nil
}

// restart closes the workspace's provider and starts a fresh one.
func (p *providerPool) restart(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	slot := p.slotFor(workspace.Identity().ID)
	slot.mu.Lock()
	if existing := slot.backend; existing != nil {
		_ = existing.Close(context.Background())
		slot.backend = nil
	}
	slot.mu.Unlock()
	return p.canonical(ctx, workspace)
}

// resync asks the canonical provider to reload the workspace from disk and
// restarts it when the resync fails.
func (p *providerPool) resync(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	backend, err := p.canonical(ctx, workspace)
	if err == nil {
		_, err = callCanonicalProvider(ctx, "provider_resync", workspace, backend, "workspace_resync", map[string]any{"root": workspace.Identity().Root})
	}
	if err == nil {
		return backend, nil
	}
	return p.restart(ctx, workspace)
}

// close shuts every provider and rolls back every sandbox stager. The maps
// are detached under mu and the slow work happens outside it so a stager
// still inside a long operation cannot stall the map.
func (p *providerPool) close() {
	p.mu.Lock()
	slots := p.providers
	stagers := p.stagers
	p.providers = make(map[workspacecore.ID]*providerSlot)
	p.stagers = make(map[stagerKey]*sandboxPlanStager)
	p.mu.Unlock()
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
