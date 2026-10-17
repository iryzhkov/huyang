package providerpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type capacityFactory struct {
	mu               sync.Mutex
	opened           []*capacityProvider
	configs          []OpenConfig
	live, peak       atomic.Int32
	fail             atomic.Bool
	started, proceed chan struct{}
}
type capacityProvider struct {
	stubProvider
	factory                    *capacityFactory
	closed                     atomic.Bool
	failClose                  atomic.Bool
	unhealthy                  atomic.Bool
	closeStarted, closeProceed chan struct{}
}

func (f *capacityFactory) Open(c OpenConfig) (provider.Provider, error) {
	if f.started != nil {
		f.started <- struct{}{}
		<-f.proceed
	}
	if f.fail.Load() {
		return nil, errors.New("launch failed")
	}
	b := &capacityProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{Root: c.Root, Epoch: c.AfterEpoch + 1}}, factory: f}
	f.mu.Lock()
	f.opened = append(f.opened, b)
	f.configs = append(f.configs, c)
	f.mu.Unlock()
	live := f.live.Add(1)
	for peak := f.peak.Load(); live > peak; peak = f.peak.Load() {
		if f.peak.CompareAndSwap(peak, live) {
			break
		}
	}
	return b, nil
}
func (b *capacityProvider) Close(context.Context) error {
	if b.failClose.Load() {
		return errors.New("close failed")
	}
	if b.closed.CompareAndSwap(false, true) {
		if b.closeStarted != nil {
			close(b.closeStarted)
			<-b.closeProceed
		}
		b.factory.live.Add(-1)
	}
	return nil
}
func (b *capacityProvider) Health(ctx context.Context) provider.Health {
	if b.unhealthy.Load() {
		return provider.Health{State: provider.HealthFailed}
	}
	return b.stubProvider.Health(ctx)
}
func capWorkspace(t *testing.T) *workspacecore.Workspace {
	t.Helper()
	w, err := workspacecore.New(workspacecore.KindProject, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func capPool(t *testing.T, n int) (*Pool, *capacityFactory) {
	t.Helper()
	f := &capacityFactory{}
	p := NewWithLimits(t.TempDir(), f, Limits{MaxProviders: n, MaxLanguageServers: 2})
	t.Cleanup(p.Close)
	return p, f
}
func capCanonical(t *testing.T, p *Pool, w *workspacecore.Workspace) (provider.Provider, func()) {
	t.Helper()
	b, release, err := p.Canonical(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	return b, release
}
func TestCapacityEvictsLeastRecentlyUsedAndAdvancesEpoch(t *testing.T) {
	p, f := capPool(t, 2)
	var clock atomic.Int64
	p.now = func() time.Time { return time.Unix(clock.Add(1), 0) }
	a, b, c := capWorkspace(t), capWorkspace(t), capWorkspace(t)
	first, release := capCanonical(t, p, a)
	release()
	_, release = capCanonical(t, p, b)
	release()
	_, release = p.Existing(a)
	release() // a becomes the newest
	_, release = capCanonical(t, p, c)
	release()
	if f.opened[0].closed.Load() || !f.opened[1].closed.Load() {
		t.Fatal("did not evict least-recently-used b")
	}
	next, release := capCanonical(t, p, b)
	release()
	if next.Descriptor().Epoch <= f.opened[1].Descriptor().Epoch {
		t.Fatal("evicted generation was reused")
	}
	if first.Descriptor().Epoch == 0 || f.peak.Load() > 2 {
		t.Fatal("capacity exceeded")
	}
}
func TestCapacityProtectsLeasesDebugAndSandbox(t *testing.T) {
	for _, kind := range []string{"canonical", "debug", "sandbox", "slot_lock"} {
		t.Run(kind, func(t *testing.T) {
			p, f := capPool(t, 1)
			a, b := capWorkspace(t), capWorkspace(t)
			var release func()
			switch kind {
			case "sandbox":
				backend, err := p.Open(a.Identity().Root, false)
				if err != nil {
					t.Fatal(err)
				}
				release = func() { _ = backend.Close(context.Background()) }
			case "debug":
				_, r, err := p.Debug(context.Background(), a)
				r()
				if err != nil {
					t.Fatal(err)
				}
				release = func() {}
			default:
				_, release = capCanonical(t, p, a)
				if kind == "slot_lock" {
					release()
					slot := p.slotFor(a.Identity().ID)
					slot.mu.Lock()
					release = slot.mu.Unlock
				}
			}
			_, r, err := p.Canonical(context.Background(), b)
			r()
			if !errors.Is(err, ErrProviderCapacity) || f.opened[0].closed.Load() {
				t.Fatalf("held provider evicted: %v", err)
			}
			release()
		})
	}
}
func TestCapacityCountsOpeningAndFailedLaunchReleasesReservation(t *testing.T) {
	p, f := capPool(t, 1)
	f.started, f.proceed = make(chan struct{}, 1), make(chan struct{})
	opened := make(chan error, 1)
	go func() { _, err := p.Open("first", false); opened <- err }()
	<-f.started
	if _, err := p.Open("second", false); !errors.Is(err, ErrProviderCapacity) {
		t.Fatalf("opening not counted: %v", err)
	}
	close(f.proceed)
	if err := <-opened; err != nil {
		t.Fatal(err)
	}
	p.Close()
	if f.live.Load() != 0 {
		t.Fatal("open leaked through shutdown")
	}

	q, g := capPool(t, 1)
	g.fail.Store(true)
	if _, err := q.Open("failed", false); err == nil {
		t.Fatal("expected failure")
	}
	g.fail.Store(false)
	backend, err := q.Open("ok", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = backend.Close(context.Background())
	_ = backend.Close(context.Background())
	if g.live.Load() != 0 {
		t.Fatal("close did not release reservation")
	}
}
func TestCapacityRetainsClosingAndCloseFailureReservations(t *testing.T) {
	p, f := capPool(t, 1)
	backend, err := p.Open("sandbox", false)
	if err != nil {
		t.Fatal(err)
	}
	raw := f.opened[0]
	raw.failClose.Store(true)
	if err := backend.Close(context.Background()); err == nil {
		t.Fatal("expected close failure")
	}
	if _, err := p.Open("overflow", false); !errors.Is(err, ErrProviderCapacity) {
		t.Fatalf("failed close freed slot: %v", err)
	}
	raw.failClose.Store(false)
	raw.closeStarted, raw.closeProceed = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- backend.Close(context.Background()) }()
	<-raw.closeStarted
	if _, err := p.Open("overflow", false); !errors.Is(err, ErrProviderCapacity) {
		t.Fatalf("closing freed slot early: %v", err)
	}
	close(raw.closeProceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := p.Open("replacement", false); err != nil {
		t.Fatal(err)
	}
}
func TestCapacitySlowEvictionDoesNotLockOtherIdleSlots(t *testing.T) {
	p, f := capPool(t, 2)
	var clock atomic.Int64
	p.now = func() time.Time { return time.Unix(clock.Add(1), 0) }
	a, b, c := capWorkspace(t), capWorkspace(t), capWorkspace(t)
	_, r := capCanonical(t, p, a)
	r()
	_, r = capCanonical(t, p, b)
	r()
	victim := f.opened[0]
	victim.closeStarted, victim.closeProceed = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { _, r, err := p.Canonical(context.Background(), c); r(); done <- err }()
	<-victim.closeStarted
	other := make(chan error, 1)
	go func() { _, r, err := p.Canonical(context.Background(), b); r(); other <- err }()
	select {
	case err := <-other:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("eviction held another workspace's slot")
	}
	close(victim.closeProceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.peak.Load() > 2 {
		t.Fatal("eviction exceeded capacity")
	}
}

func TestCapacityFailedEvictionRetainsProviderAndReservation(t *testing.T) {
	p, f := capPool(t, 1)
	a, b := capWorkspace(t), capWorkspace(t)
	_, release := capCanonical(t, p, a)
	release()
	f.opened[0].failClose.Store(true)
	if _, r, err := p.Canonical(context.Background(), b); !errors.Is(err, ErrProviderCapacity) {
		r()
		t.Fatalf("failed eviction admitted overflow: %v", err)
	}
	if f.live.Load() != 1 || !p.Holds(a.Identity().ID) {
		t.Fatal("lost failed-close provider")
	}
	f.opened[0].failClose.Store(false)
	_, release = capCanonical(t, p, b)
	release()
	if f.peak.Load() > 1 {
		t.Fatal("exceeded cap after close retry")
	}
}

func TestCapacityConcurrentOpeningsNeverOversubscribe(t *testing.T) {
	p, f := capPool(t, 3)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			backend, err := p.Open(fmt.Sprintf("root-%d", i), false)
			if err != nil && !errors.Is(err, ErrProviderCapacity) {
				t.Error(err)
			}
			if backend != nil {
				_ = backend.Close(context.Background())
			}
		}(i)
	}
	close(gate)
	wg.Wait()
	if f.peak.Load() > 3 || f.live.Load() != 0 {
		t.Fatalf("live=%d peak=%d", f.live.Load(), f.peak.Load())
	}
}
func TestCapacitySameWorkspaceCoalescesAndRestartHonoursLeases(t *testing.T) {
	p, f := capPool(t, 1)
	w := capWorkspace(t)
	var wg sync.WaitGroup
	releases := make(chan func(), 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, err := p.Canonical(context.Background(), w)
			if err != nil {
				t.Error(err)
			}
			releases <- release
		}()
	}
	wg.Wait()
	close(releases)
	if len(f.opened) != 1 {
		t.Fatalf("opened %d providers", len(f.opened))
	}
	if _, r, err := p.Restart(context.Background(), w); !errors.Is(err, ErrProviderBusy) {
		r()
		t.Fatalf("restart broke leases: %v", err)
	}
	f.opened[0].unhealthy.Store(true)
	if _, r, err := p.Canonical(context.Background(), w); !errors.Is(err, ErrProviderBusy) {
		r()
		t.Fatalf("health replacement broke leases: %v", err)
	}
	for release := range releases {
		release()
	}
	next, r, err := p.Restart(context.Background(), w)
	r()
	if err != nil || next.Descriptor().Epoch <= 1 || f.peak.Load() > 1 {
		t.Fatalf("restart: %v", err)
	}
	p.Close()
	if _, err := p.Open("after-close", false); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("closed pool reopened: %v", err)
	}
	if _, r, err := p.Canonical(context.Background(), w); !errors.Is(err, ErrPoolClosed) {
		r()
		t.Fatalf("closed slot reopened: %v", err)
	}
}
func TestCapacityReaperAndPreparedPlanRelease(t *testing.T) {
	p, w, f, advance := expiryFixture(t)
	p.limits = Limits{MaxProviders: 1, MaxLanguageServers: 2}
	prepared, stager := prepareThroughPool(t, p, w)
	if _, r, err := p.Canonical(context.Background(), w); !errors.Is(err, ErrProviderCapacity) {
		r()
		t.Fatalf("staged provider evicted: %v", err)
	}
	if _, ok := stager.Prepared(); !ok {
		t.Fatal("capacity loss destroyed preparation")
	}
	if err := stager.Rollback(context.Background(), prepared.PlanID); err != nil {
		t.Fatal(err)
	}
	_, release := capCanonical(t, p, w)
	release()
	advance(DefaultProviderIdleTimeout)
	if p.ReapIdle() != 1 {
		t.Fatal("idle provider retained")
	}
	_, release = capCanonical(t, p, w)
	release()
	if len(f.opened) != 4 {
		t.Fatalf("expected staged pair and two canonical generations: %d", len(f.opened))
	}
}
func TestCapacityDebugHoldSurvivesRestartAndHealthReplacement(t *testing.T) {
	p, f := capPool(t, 1)
	w, other := capWorkspace(t), capWorkspace(t)
	_, r, err := p.Debug(context.Background(), w)
	r()
	if err != nil {
		t.Fatal(err)
	}
	_, r, err = p.Restart(context.Background(), w)
	r()
	if err != nil {
		t.Fatal(err)
	}
	f.opened[1].unhealthy.Store(true)
	_, r = capCanonical(t, p, w)
	r()
	for i, c := range f.configs {
		if !c.Debug || !c.PinLanguageServers {
			t.Fatalf("generation %d lost debug hold: %+v", i, c)
		}
	}
	if _, r, err := p.Canonical(context.Background(), other); !errors.Is(err, ErrProviderCapacity) {
		r()
		t.Fatalf("replacement was evictable: %v", err)
	}
}

func TestCapacityPassesLanguageServerLimitAndPinsOwnedProviders(t *testing.T) {
	p, f := capPool(t, 3)
	_, release := capCanonical(t, p, capWorkspace(t))
	release()
	_, r, err := p.Debug(context.Background(), capWorkspace(t))
	r()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Open("staged", false); err != nil {
		t.Fatal(err)
	}
	for i, c := range f.configs {
		if c.MaxLanguageServers != 2 || c.PinLanguageServers != (i != 0) {
			t.Fatalf("config[%d]=%+v", i, c)
		}
	}
}
