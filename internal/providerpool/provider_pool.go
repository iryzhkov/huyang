package providerpool

import (
	"context"
	"fmt"
	"sync"
	"time"

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

	idleTimeout time.Duration
	// planIdleTTL is how long a plan may sit unused before the sweep expires
	// it and releases its stager; zero turns expiry off.
	planIdleTTL time.Duration
	// workspaces lists every open workspace, so the sweep also reaches plans
	// that hold no stager: intents never prepared, and preparations a
	// restart discarded. Without it only workspaces with a stager are swept.
	workspaces  func() []*workspacecore.Workspace
	now         func() time.Time
	rootMissing func(string) bool
	reaperMu    sync.Mutex
	reaperStop  chan struct{}
	reaperDone  chan struct{}
	closed      bool
}

func New(sandboxBase string, factory Factory) *Pool {
	return &Pool{
		factory:     factory,
		idleTimeout: DefaultProviderIdleTimeout,
		planIdleTTL: workspacecore.PlanIdleTTL(),
		now:         time.Now,
		rootMissing: providerRootMissing,
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
	mu       sync.Mutex
	backend  provider.Provider
	root     string
	debug    bool
	users    int
	lastUsed time.Time
	epoch    uint64 // last generation closed; replacement epochs must advance
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
	return p.openAfter(root, debug, 0)
}

func (p *Pool) openAfter(root string, debug bool, epoch uint64) (provider.Provider, error) {
	return p.factory.Open(OpenConfig{
		Root: root, InitFile: huyangHeadlessInit(),
		RuntimePath: ShippedRuntimePath(), Debug: debug, AfterEpoch: epoch,
	})
}

// Canonical leases the owned Neovim provider for a project workspace.
// The caller must invoke the returned release function after its last use,
// including background work. Release is safe to call on error and more than once.
// Workspaces are durable while provider processes are replaceable, so the
// provider is started lazily and recreated after a daemon or provider
// restart without changing the workspace ID.
func (p *Pool) Canonical(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, func(), error) {
	return p.workspaceProvider(ctx, workspace, false)
}

// debug returns the workspace provider, starting it in debug mode when no
// provider is running yet. A healthy canonical provider is reused.
func (p *Pool) Debug(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, func(), error) {
	return p.workspaceProvider(ctx, workspace, true)
}

func (p *Pool) workspaceProvider(ctx context.Context, workspace *workspacecore.Workspace, debug bool) (provider.Provider, func(), error) {
	identity := workspace.Identity()
	if identity.Kind != workspacecore.KindProject {
		return nil, func() {}, fmt.Errorf("semantic provider requires a project workspace")
	}
	slot := p.slotFor(identity.ID)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if existing := slot.backend; existing != nil {
		health := existing.Health(ctx)
		if health.State == provider.HealthHealthy || health.State == provider.HealthStarting {
			workspace.SyncProviderEpoch(existing.Descriptor().Epoch)
			slot.debug = slot.debug || debug
			return existing, p.leaseLocked(slot), nil
		}
		slot.epoch = existing.Descriptor().Epoch
		_ = existing.Close(context.Background())
		slot.backend = nil
	}
	backend, err := p.openAfter(identity.Root, debug, slot.epoch)
	if err != nil {
		return nil, func() {}, err
	}
	slot.backend = backend
	slot.root = identity.Root
	slot.debug = debug
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	return backend, p.leaseLocked(slot), nil
}

// Existing returns the workspace's running provider without starting one,
// or nil when none is running, with a release function (also safe on nil).
// Late-evidence collection uses it: a
// workspace with no provider has no server that could have published.
func (p *Pool) Existing(workspace *workspacecore.Workspace) (provider.Provider, func()) {
	if workspace.Identity().Kind != workspacecore.KindProject {
		return nil, func() {}
	}
	slot := p.slotFor(workspace.Identity().ID)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.backend == nil {
		return nil, func() {}
	}
	return slot.backend, p.leaseLocked(slot)
}

// restart closes the workspace's provider and starts a fresh one.
func (p *Pool) Restart(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, func(), error) {
	slot := p.slotFor(workspace.Identity().ID)
	slot.mu.Lock()
	if existing := slot.backend; existing != nil {
		slot.epoch = existing.Descriptor().Epoch
		_ = existing.Close(context.Background())
		slot.backend = nil
	}
	slot.mu.Unlock()
	return p.Canonical(ctx, workspace)
}

// resync asks the canonical provider to reload the workspace from disk and
// restarts it when the resync fails.
func (p *Pool) Resync(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, func(), error) {
	backend, release, err := p.Canonical(ctx, workspace)
	if err == nil {
		_, err = CallCanonical(ctx, "provider_resync", workspace, backend, "workspace_resync", map[string]any{"root": workspace.Identity().Root})
	}
	if err == nil {
		return backend, release, nil
	}
	release()
	return p.Restart(ctx, workspace)
}

// close shuts every provider and rolls back every sandbox stager. The maps
// are detached under mu and the slow work happens outside it so a stager
// still inside a long operation cannot stall the map.
func (p *Pool) Close() {
	p.reaperMu.Lock()
	p.closed = true
	p.stopReaperLocked()
	p.reaperMu.Unlock()
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

// Holds reports whether the pool keeps anything alive for the workspace: a
// running canonical or debug provider, a caller still leasing its slot, or a
// sandbox stager of one of its plans. The workspace registry never forgets a
// workspace the pool still holds. A slot whose lock is taken is in use, so it
// counts as held rather than being waited on.
func (p *Pool) Holds(id workspacecore.ID) bool {
	p.mu.Lock()
	slot := p.providers[id]
	for key := range p.stagers {
		if key.workspace == id {
			p.mu.Unlock()
			return true
		}
	}
	p.mu.Unlock()
	if slot == nil {
		return false
	}
	if !slot.mu.TryLock() {
		return true
	}
	defer slot.mu.Unlock()
	return slot.backend != nil || slot.users > 0
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
	stager := p.stagers[stagerKey{workspace: workspaceID, planID: planID}]
	p.mu.Unlock()
	if stager != nil {
		stager.MarkUsed()
	}
	return stager
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
