package providerpool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
)

const DefaultMaxProviders = 4
const DefaultMaxLanguageServers = 4

// Limits bound live processes independently of scheduler job concurrency.
// Nonpositive fields select the conservative defaults.
type Limits struct {
	MaxProviders       int
	MaxLanguageServers int // per provider; total clients <= the product of both limits
}

var ErrProviderCapacity = errors.New("provider capacity exhausted")
var ErrPoolClosed = errors.New("provider pool is closed")
var ErrProviderBusy = errors.New("provider is still leased")

// ownedProvider keeps its reservation through slow opens, closes and failures.
// A sandbox is pinned for its entire lifetime; only canonical slots are LRU candidates.
type ownedProvider struct {
	provider.Provider
	pool     *Pool
	mu       sync.Mutex
	released bool
}

func (b *ownedProvider) Close(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released {
		return nil
	}
	if err := b.Provider.Close(ctx); err != nil {
		return err
	}
	b.released = true
	b.pool.capacityMu.Lock()
	delete(b.pool.owned, b)
	b.pool.capacityMu.Unlock()
	return nil
}

func (p *Pool) isClosed() bool {
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	return p.capacityClosed
}

func (p *Pool) reserve() (*ownedProvider, error) {
	// Serialise admission/eviction, but never hold capacityMu across a slot or
	// provider operation. A locked slot is skipped, avoiding lock inversion.
	p.admitMu.Lock()
	defer p.admitMu.Unlock()
	for {
		p.capacityMu.Lock()
		if p.capacityClosed {
			p.capacityMu.Unlock()
			return nil, ErrPoolClosed
		}
		if len(p.owned) < p.limits.MaxProviders {
			b := &ownedProvider{pool: p}
			p.owned[b] = struct{}{}
			p.opening.Add(1)
			p.capacityMu.Unlock()
			return b, nil
		}
		p.capacityMu.Unlock()
		if !p.evictLRU() {
			return nil, fmt.Errorf("%w (limit %d): all providers are active, staged, opening or closing; release work or increase --max-providers", ErrProviderCapacity, p.limits.MaxProviders)
		}
	}
}

func (p *Pool) evictLRU() bool {
	p.mu.Lock()
	slots := make([]*providerSlot, 0, len(p.providers))
	for _, slot := range p.providers {
		slots = append(slots, slot)
	}
	p.mu.Unlock()
	type candidate struct {
		slot *providerSlot
		used time.Time
	}
	candidates := make([]candidate, 0, len(slots))
	for _, slot := range slots {
		if !slot.mu.TryLock() {
			continue
		}
		if slot.backend != nil && slot.users == 0 {
			candidates = append(candidates, candidate{slot, slot.lastUsed})
		}
		slot.mu.Unlock()
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].used.Before(candidates[j].used) })
	for _, candidate := range candidates {
		slot := candidate.slot
		if !slot.mu.TryLock() {
			continue
		}
		// A caller may have leased this slot since the snapshot. Recheck under
		// its lock, and never block lookups of other idle providers while closing.
		evicted := false
		if slot.backend != nil && slot.users == 0 && slot.lastUsed.Equal(candidate.used) {
			evicted = p.closeSlotLocked(slot) == nil
		}
		slot.mu.Unlock()
		if evicted {
			return true
		}
	}
	return false
}

// closeSlotLocked preserves both the generation and reservation on close failure.
func (p *Pool) closeSlotLocked(slot *providerSlot) error {
	if slot.backend == nil {
		return nil
	}
	slot.epoch = slot.backend.Descriptor().Epoch
	if err := slot.backend.Close(context.Background()); err != nil {
		return err
	}
	slot.backend = nil
	return nil
}

func (p *Pool) openOwned(root string, epoch uint64, pinned bool) (provider.Provider, error) {
	b, err := p.reserve()
	if err != nil {
		return nil, err
	}
	defer p.opening.Done()
	backend, err := p.factory.Open(OpenConfig{
		Root: root, InitFile: huyangHeadlessInit(), RuntimePath: ShippedRuntimePath(),
		AfterEpoch: epoch, MaxLanguageServers: p.limits.MaxLanguageServers,
		PinLanguageServers: pinned,
	})
	if err != nil {
		p.capacityMu.Lock()
		delete(p.owned, b)
		p.capacityMu.Unlock()
		return nil, err
	}
	b.Provider = backend
	return b, nil
}

// NewWithLimits constructs a pool with fixed process limits.
func NewWithLimits(sandboxBase string, factory Factory, limits Limits) *Pool {
	if limits.MaxProviders <= 0 {
		limits.MaxProviders = DefaultMaxProviders
	}
	if limits.MaxLanguageServers <= 0 {
		limits.MaxLanguageServers = DefaultMaxLanguageServers
	}
	p := New(sandboxBase, factory)
	p.limits = limits
	return p
}
