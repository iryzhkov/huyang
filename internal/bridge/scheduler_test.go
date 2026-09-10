package bridge

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerSerializesOneWorkspaceAndSeparatesOthers(t *testing.T) {
	scheduler := newWorkspaceScheduler(2, 1)
	releaseFirst, err := scheduler.acquire(context.Background(), "ws_a", scheduleCanonicalWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()

	blocked := make(chan struct{})
	go func() {
		release, err := scheduler.acquire(context.Background(), "ws_a", scheduleCanonicalWrite)
		if err == nil {
			release()
		}
		close(blocked)
	}()

	select {
	case <-blocked:
		t.Fatal("same-workspace write was not serialized")
	case <-time.After(20 * time.Millisecond):
	}

	releaseOther, err := scheduler.acquire(context.Background(), "ws_b", scheduleCanonicalWrite)
	if err != nil {
		t.Fatal(err)
	}
	releaseOther()
}

func TestSchedulerProviderQuotaAndCancellation(t *testing.T) {
	scheduler := newWorkspaceScheduler(1, 1)
	releaseFirst, err := scheduler.acquire(context.Background(), "ws_a", scheduleProviderRead)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := scheduler.acquire(ctx, "ws_b", scheduleProviderRead); err == nil {
		t.Fatal("provider quota wait ignored cancellation")
	}
}

func TestSchedulerPureReadsDoNotQueueBehindWrites(t *testing.T) {
	scheduler := newWorkspaceScheduler(1, 1)
	releaseWrite, err := scheduler.acquire(context.Background(), "ws_a", scheduleCanonicalWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseWrite()

	var acquired atomic.Bool
	releaseRead, err := scheduler.acquire(context.Background(), "ws_a", schedulePureRead)
	if err != nil {
		t.Fatal(err)
	}
	acquired.Store(true)
	releaseRead()
	if !acquired.Load() {
		t.Fatal("pure read did not run independently")
	}
}
