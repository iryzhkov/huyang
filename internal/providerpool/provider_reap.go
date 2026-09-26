package providerpool

import (
	"context"
	"log"
	"os"
	"sync"
	"time"
)

const (
	DefaultProviderIdleTimeout = 15 * time.Minute
	DefaultReapInterval        = time.Minute
	missingRootIdleTimeout     = time.Minute
)

// leaseLocked pins the slot until the caller's last use, not merely until
// its context expires. A cancelled call can still be unwinding or blocked
// in transport submission. Background work must hold a separate lease.
// slot.mu is held by the caller; all lease and reaper decisions use that lock.
func (p *Pool) leaseLocked(slot *providerSlot) func() {
	slot.users++
	slot.lastUsed = p.now()
	var once sync.Once
	return func() {
		once.Do(func() {
			slot.mu.Lock()
			defer slot.mu.Unlock()
			slot.users--
			slot.lastUsed = p.now()
		})
	}
}

func providerRootMissing(root string) bool {
	_, err := os.Stat(root)
	// Permission and transient I/O errors do not prove the root disappeared.
	return os.IsNotExist(err)
}

// ReapIdle closes unused canonical providers. Slots remain in the map:
// deleting one could orphan a provider opened by a caller holding its pointer.
// Debug providers are pinned, including canonical providers reused for debugging.
// Sandbox stagers have a separate plan lifetime and are never reaped here;
// ExpireIdlePlans releases them with their plan.
func (p *Pool) ReapIdle() int {
	p.mu.Lock()
	slots := make([]*providerSlot, 0, len(p.providers))
	for _, slot := range p.providers {
		slots = append(slots, slot)
	}
	p.mu.Unlock()
	reaped := 0
	for _, slot := range slots {
		if !slot.mu.TryLock() {
			continue
		}
		if p.reapSlotLocked(slot) {
			reaped++
		}
		slot.mu.Unlock()
	}
	return reaped
}

func (p *Pool) reapSlotLocked(slot *providerSlot) bool {
	if slot.backend == nil || slot.debug || slot.users != 0 {
		return false
	}
	idle := p.now().Sub(slot.lastUsed)
	reason := ""
	if idle >= p.idleTimeout {
		reason = "idle"
	} else if idle >= missingRootIdleTimeout && p.rootMissing(slot.root) {
		reason = "root removed"
	}
	if reason == "" {
		return false
	}
	slot.epoch = slot.backend.Descriptor().Epoch
	if err := slot.backend.Close(context.Background()); err != nil {
		log.Printf("huyang: close idle provider root=%q: %v", slot.root, err)
		return false
	}
	slot.backend = nil
	log.Printf("huyang: reaped provider root=%q idle=%s reason=%s", slot.root, idle, reason)
	return true
}

// StartReaper is idempotent. Close permanently prevents a new reaper;
// StopReaper permits a later restart. Invalid intervals use the default.
func (p *Pool) StartReaper(interval time.Duration) {
	p.reaperMu.Lock()
	defer p.reaperMu.Unlock()
	if p.closed || p.reaperStop != nil {
		return
	}
	if interval <= 0 {
		interval = DefaultReapInterval
	}
	stop, done := make(chan struct{}), make(chan struct{})
	p.reaperStop, p.reaperDone = stop, done
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				p.ReapIdle()
				p.ExpireIdlePlans()
			}
		}
	}()
}

// StopReaper joins the worker before returning.
func (p *Pool) StopReaper() {
	p.reaperMu.Lock()
	defer p.reaperMu.Unlock()
	p.stopReaperLocked()
}

func (p *Pool) stopReaperLocked() {
	if p.reaperStop == nil {
		return
	}
	close(p.reaperStop)
	<-p.reaperDone
	p.reaperStop, p.reaperDone = nil, nil
}
