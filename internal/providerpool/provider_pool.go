package providerpool

import (
	"context"
	"fmt"
	"sync"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Pool owns every provider the service spawns: the canonical (or
// debug) provider of each workspace and the sandbox stagers that prepare
// plans in isolation. It is the single place a provider is opened, so the
// runtime path and init file cannot drift between call sites.
type Pool struct {
	factory     Factory
	sandboxBase string

	// mu guards only the two maps below; provider and stager operations run
	// outside it (see providerSlot and SandboxStager).
	mu        sync.Mutex
	providers map[workspacecore.ID]*providerSlot
	stagers   map[stagerKey]*SandboxStager
}

func New(sandboxBase string, factory Factory) *Pool {
	return &Pool{
		factory:     factory,
		sandboxBase: sandboxBase,
		providers:   make(map[workspacecore.ID]*providerSlot),
		stagers:     make(map[stagerKey]*SandboxStager),
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

func (p *Pool) slotFor(id workspacecore.ID) *providerSlot {
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
func (p *Pool) Open(root string, debug bool) (provider.Provider, error) {
	return p.factory.Open(OpenConfig{
		Root: root, InitFile: huyangHeadlessInit(),
		RuntimePath: ShippedRuntimePath(), Debug: debug,
	})
}

// canonical returns the owned Neovim provider for a project workspace.
// Workspaces are durable while provider processes are replaceable, so the
// provider is started lazily and recreated after a daemon or provider
// restart without changing the workspace ID.
func (p *Pool) Canonical(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	return p.workspaceProvider(ctx, workspace, false)
}

// debug returns the workspace provider, starting it in debug mode when no
// provider is running yet. A healthy canonical provider is reused.
func (p *Pool) Debug(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	return p.workspaceProvider(ctx, workspace, true)
}

func (p *Pool) workspaceProvider(ctx context.Context, workspace *workspacecore.Workspace, debug bool) (provider.Provider, error) {
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
	backend, err := p.Open(identity.Root, debug)
	if err != nil {
		return nil, err
	}
	slot.backend = backend
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	return backend, nil
}

// restart closes the workspace's provider and starts a fresh one.
func (p *Pool) Restart(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	slot := p.slotFor(workspace.Identity().ID)
	slot.mu.Lock()
	if existing := slot.backend; existing != nil {
		_ = existing.Close(context.Background())
		slot.backend = nil
	}
	slot.mu.Unlock()
	return p.Canonical(ctx, workspace)
}

// resync asks the canonical provider to reload the workspace from disk and
// restarts it when the resync fails.
func (p *Pool) Resync(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	backend, err := p.Canonical(ctx, workspace)
	if err == nil {
		_, err = CallCanonical(ctx, "provider_resync", workspace, backend, "workspace_resync", map[string]any{"root": workspace.Identity().Root})
	}
	if err == nil {
		return backend, nil
	}
	return p.Restart(ctx, workspace)
}

// close shuts every provider and rolls back every sandbox stager. The maps
// are detached under mu and the slow work happens outside it so a stager
// still inside a long operation cannot stall the map.
func (p *Pool) Close() {
	p.mu.Lock()
	slots := p.providers
	stagers := p.stagers
	p.providers = make(map[workspacecore.ID]*providerSlot)
	p.stagers = make(map[stagerKey]*SandboxStager)
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

// sandboxBaseDir is the directory under which plan and verification
// sandboxes are materialised.
func (p *Pool) SandboxBaseDir() string {
	return p.sandboxBase
}

// stager returns the sandbox stager registered for one plan of one
// workspace, or nil.
func (p *Pool) Stager(workspaceID workspacecore.ID, planID string) *SandboxStager {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stagers[stagerKey{workspace: workspaceID, planID: planID}]
}

// stagersFor lists the sandbox stagers registered for one workspace, in no
// particular order.
func (p *Pool) StagersFor(workspaceID workspacecore.ID) []*SandboxStager {
	p.mu.Lock()
	defer p.mu.Unlock()
	var found []*SandboxStager
	for key, candidate := range p.stagers {
		if key.workspace == workspaceID {
			found = append(found, candidate)
		}
	}
	return found
}
