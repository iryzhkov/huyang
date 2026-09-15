package providerpool

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type reapProvider struct {
	stubProvider
	closes       atomic.Int32
	closeStarted chan struct{}
	closeAllowed chan struct{}
}

func (p *reapProvider) Close(context.Context) error {
	p.closes.Add(1)
	if p.closeStarted != nil {
		close(p.closeStarted)
		<-p.closeAllowed
	}
	return nil
}

type reapFactory struct{ opened []*reapProvider }

func (f *reapFactory) Open(c OpenConfig) (provider.Provider, error) {
	p := &reapProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{Root: c.Root, Epoch: c.AfterEpoch + 1}}}
	f.opened = append(f.opened, p)
	return p, nil
}
func reapFixture(t *testing.T) (*Pool, *workspacecore.Workspace, *reapFactory, func(time.Duration)) {
	t.Helper()
	w, err := workspacecore.New(workspacecore.KindProject, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	f := &reapFactory{}
	p := New(t.TempDir(), f)
	var clock atomic.Int64
	p.now = func() time.Time { return time.Unix(0, clock.Load()) }
	t.Cleanup(p.Close)
	return p, w, f, func(d time.Duration) { clock.Add(int64(d)) }
}
func TestReapIdleReopensSameWorkspace(t *testing.T) {
	p, w, f, advance := reapFixture(t)
	id := w.Identity().ID
	first, release, err := p.Canonical(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	release()
	advance(DefaultProviderIdleTimeout)
	if n := p.ReapIdle(); n != 1 {
		t.Fatalf("reaped %d", n)
	}
	if f.opened[0].closes.Load() != 1 {
		t.Fatal("provider not closed exactly once")
	}
	if n := p.ReapIdle(); n != 0 {
		t.Fatalf("reaped empty slot: %d", n)
	}
	next, release, err := p.Canonical(context.Background(), w)
	defer release()
	if err != nil || next == first || w.Identity().ID != id || len(f.opened) != 2 {
		t.Fatalf("reopen: backend=%v error=%v id=%s", next, err, w.Identity().ID)
	}
	if next.Descriptor().Epoch <= first.Descriptor().Epoch {
		t.Fatal("replacement reused old provider epoch")
	}
}
func TestReapWaitsForEveryLeaseAfterCancellation(t *testing.T) {
	p, w, f, advance := reapFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, release, err := p.Canonical(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	_, otherRelease := p.Existing(w)
	cancel()
	if err := os.Remove(w.Identity().Root); err != nil {
		t.Fatal(err)
	}
	advance(2 * DefaultProviderIdleTimeout)
	if p.ReapIdle() != 0 {
		t.Fatal("closed a cancelled but unreleased caller")
	}
	release()
	release() // release is idempotent, not a second decrement
	advance(2 * DefaultProviderIdleTimeout)
	if p.ReapIdle() != 0 {
		t.Fatal("closed the other caller's lease")
	}
	otherRelease()
	if p.ReapIdle() != 0 {
		t.Fatal("idle period did not start at last release")
	}
	advance(missingRootIdleTimeout)
	if p.ReapIdle() != 1 || f.opened[0].closes.Load() != 1 {
		t.Fatal("unused missing root not reaped")
	}
}
func TestReapExistingRefreshesIdleTime(t *testing.T) {
	p, w, _, advance := reapFixture(t)
	_, release, err := p.Canonical(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	release()
	advance(DefaultProviderIdleTimeout - time.Second)
	backend, release := p.Existing(w)
	if backend == nil {
		t.Fatal("missing provider")
	}
	release()
	advance(time.Second)
	if p.ReapIdle() != 0 {
		t.Fatal("recently used provider reaped")
	}
	advance(DefaultProviderIdleTimeout)
	if p.ReapIdle() != 1 {
		t.Fatal("idle provider retained")
	}
	backend, release = p.Existing(w)
	release()
	if backend != nil {
		t.Fatal("Existing spawned a provider")
	}
}
func TestReapDebugAndBusySlots(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh_debug", true: "canonical_reused_for_debug"}[reuse], func(t *testing.T) {
			p, w, _, advance := reapFixture(t)
			if reuse {
				_, release, err := p.Canonical(context.Background(), w)
				release()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, release, err := p.Debug(context.Background(), w)
			release()
			if err != nil {
				t.Fatal(err)
			}
			advance(2 * DefaultProviderIdleTimeout)
			if p.ReapIdle() != 0 {
				t.Fatal("debug provider reaped")
			}
		})
	}
	t.Run("slot_lock", func(t *testing.T) {
		p, w, _, advance := reapFixture(t)
		_, release, err := p.Canonical(context.Background(), w)
		release()
		if err != nil {
			t.Fatal(err)
		}
		advance(DefaultProviderIdleTimeout)
		slot := p.slotFor(w.Identity().ID)
		slot.mu.Lock()
		n := p.ReapIdle()
		slot.mu.Unlock()
		if n != 0 {
			t.Fatal("busy slot reaped")
		}
		if p.ReapIdle() != 1 {
			t.Fatal("unlocked idle slot retained")
		}
	})
}
func TestReaperLifecycleJoinsAndDoesNotRestartAfterClose(t *testing.T) {
	p, w, f, advance := reapFixture(t)
	_, release, err := p.Canonical(context.Background(), w)
	release()
	if err != nil {
		t.Fatal(err)
	}
	backend := f.opened[0]
	backend.closeStarted = make(chan struct{})
	backend.closeAllowed = make(chan struct{})
	advance(DefaultProviderIdleTimeout)
	p.StartReaper(time.Millisecond)
	p.reaperMu.Lock()
	done := p.reaperDone
	p.reaperMu.Unlock()
	p.StartReaper(time.Millisecond)
	p.reaperMu.Lock()
	same := p.reaperDone == done
	p.reaperMu.Unlock()
	if !same {
		t.Fatal("started duplicate reaper")
	}
	select {
	case <-backend.closeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("ticker did not reap")
	}
	stopped := make(chan struct{})
	go func() { p.StopReaper(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("StopReaper returned during Close")
	default:
	}
	close(backend.closeAllowed)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("StopReaper did not join")
	}
	p.StartReaper(time.Hour)
	p.Close()
	p.StartReaper(time.Millisecond)
	p.reaperMu.Lock()
	running := p.reaperStop != nil
	p.reaperMu.Unlock()
	if running {
		t.Fatal("reaper restarted after pool close")
	}
}
func TestConcurrentLeaseAndReap(t *testing.T) {
	p, w, _, advance := reapFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				backend, release, err := p.Canonical(context.Background(), w)
				if err != nil {
					t.Error(err)
					return
				}
				advance(DefaultProviderIdleTimeout)
				p.ReapIdle()
				if backend.(*reapProvider).closes.Load() != 0 {
					t.Error("leased provider closed")
				}
				release()
			}
		}()
	}
	wg.Wait()
	advance(DefaultProviderIdleTimeout)
	if p.ReapIdle() != 1 {
		t.Fatal("final idle provider retained")
	}
}
