package bridge

import (
	"context"
	"fmt"
	"sync"
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
