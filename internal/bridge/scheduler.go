package bridge

import (
	"context"
	"fmt"
	"sync"
)

type schedulerClass string

const (
	schedulePureRead       schedulerClass = "pure_read"
	scheduleProviderRead   schedulerClass = "provider_read"
	scheduleCanonicalWrite schedulerClass = "canonical_write"
	scheduleSandboxWrite   schedulerClass = "sandbox_write"
	scheduleExternalJob    schedulerClass = "external_job"
)

type workspaceScheduler struct {
	mu            sync.Mutex
	lanes         map[string]chan struct{}
	sandboxLanes  map[string]chan struct{}
	providerSlots chan struct{}
	sandboxSlots  chan struct{}
	externalSlots chan struct{}
	providerQuota int
	externalQuota int
}

func newWorkspaceScheduler(providerQuota, externalJobQuota int) *workspaceScheduler {
	if providerQuota < 1 {
		providerQuota = 1
	}
	if externalJobQuota < 1 {
		externalJobQuota = 1
	}
	return &workspaceScheduler{
		lanes:         make(map[string]chan struct{}),
		sandboxLanes:  make(map[string]chan struct{}),
		providerSlots: make(chan struct{}, providerQuota),
		sandboxSlots:  make(chan struct{}, 4),
		externalSlots: make(chan struct{}, externalJobQuota),
		providerQuota: providerQuota,
		externalQuota: externalJobQuota,
	}
}

func (s *workspaceScheduler) description() map[string]any {
	return map[string]any{
		"classes": []string{
			string(schedulePureRead),
			string(scheduleProviderRead),
			string(scheduleCanonicalWrite),
			string(scheduleSandboxWrite),
			string(scheduleExternalJob),
		},
		"provider_quota":          s.providerQuota,
		"external_job_quota":      s.externalQuota,
		"sandbox_service_quota":   cap(s.sandboxSlots),
		"sandbox_workspace_quota": 2,
	}
}

func (s *workspaceScheduler) acquire(ctx context.Context, workspaceID string, class schedulerClass) (func(), error) {
	switch class {
	case schedulePureRead:
		return func() {}, nil
	case scheduleProviderRead:
		releaseProvider, err := acquireSlot(ctx, s.providerSlots)
		if err != nil {
			return nil, err
		}
		releaseLane, err := acquireSlot(ctx, s.lane(workspaceID))
		if err != nil {
			releaseProvider()
			return nil, err
		}
		return func() {
			releaseLane()
			releaseProvider()
		}, nil
	case scheduleCanonicalWrite:
		return acquireSlot(ctx, s.lane(workspaceID))
	case scheduleSandboxWrite:
		releaseProvider, err := acquireSlot(ctx, s.providerSlots)
		if err != nil {
			return nil, err
		}
		releaseService, err := acquireSlot(ctx, s.sandboxSlots)
		if err != nil {
			releaseProvider()
			return nil, err
		}
		releaseWorkspace, err := acquireSlot(ctx, s.sandboxLane(workspaceID))
		if err != nil {
			releaseService()
			releaseProvider()
			return nil, err
		}
		return func() {
			releaseWorkspace()
			releaseService()
			releaseProvider()
		}, nil
	case scheduleExternalJob:
		return acquireSlot(ctx, s.externalSlots)
	default:
		return nil, fmt.Errorf("unknown scheduler class %q", class)
	}
}

func (s *workspaceScheduler) lane(workspaceID string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	lane := s.lanes[workspaceID]
	if lane == nil {
		lane = make(chan struct{}, 1)
		s.lanes[workspaceID] = lane
	}
	return lane
}

func (s *workspaceScheduler) sandboxLane(workspaceID string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	lane := s.sandboxLanes[workspaceID]
	if lane == nil {
		lane = make(chan struct{}, 2)
		s.sandboxLanes[workspaceID] = lane
	}
	return lane
}

func acquireSlot(ctx context.Context, slot chan struct{}) (func(), error) {
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func knownSchedulerClass(class schedulerClass) bool {
	switch class {
	case schedulePureRead, scheduleProviderRead, scheduleCanonicalWrite, scheduleSandboxWrite, scheduleExternalJob:
		return true
	default:
		return false
	}
}

// modernSchedulerClass returns the class a tool declares on its descriptor.
// Unknown tools fall back to the most exclusive class so a registration
// mistake degrades to serialisation rather than to an unguarded provider call.
func modernSchedulerClass(name string) schedulerClass {
	for _, descriptor := range modernTools {
		if descriptor.Name == name {
			return descriptor.Class
		}
	}
	return scheduleCanonicalWrite
}

// classForCall refines the declared class for argument-dependent behaviour:
// change_plan actions other than prepare only touch durable plan intent and
// the canonical tree, and a read addressed by symbol locator may consult the
// provider to resolve the declaration.
func classForCall(name string, arguments map[string]any) schedulerClass {
	class := modernSchedulerClass(name)
	switch name {
	case "change_plan":
		if action, _ := arguments["action"].(string); action != "prepare" {
			class = scheduleCanonicalWrite
		}
	case "read":
		if target, _ := arguments["target"].(map[string]any); target != nil {
			if _, symbol := target["symbol_locator"]; symbol {
				class = scheduleProviderRead
			}
		}
	}
	return class
}
