package providerpool

import (
	"context"
	"log"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// stagerReleaseGrace is how long a stager whose plan no longer needs it stays
// registered. It covers the moment between PlanStager creating a stager and
// PreparePlan moving the plan to PREPARING, when the plan still reads as an
// intent, or as the failed plan being prepared again.
const stagerReleaseGrace = time.Minute

// SetWorkspaceSource names the function that lists every open workspace, so
// the idle plan sweep reaches plans that hold no stager.
func (p *Pool) SetWorkspaceSource(workspaces func() []*workspacecore.Workspace) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workspaces = workspaces
}

// ExpireIdlePlans expires every plan idle for longer than the plan idle limit
// and releases what its stager held: the sandbox tree and the provider with
// its language servers. It then drops every stager whose plan has finished
// with it - applied, discarded, failed, expired or gone - so the stager map
// holds only live preparations. It returns the number of plans expired. The
// reaper runs it after every idle provider sweep.
func (p *Pool) ExpireIdlePlans() int {
	now := p.now()
	p.mu.Lock()
	stagers := make(map[stagerKey]*SandboxStager, len(p.stagers))
	for key, stager := range p.stagers {
		stagers[key] = stager
	}
	source := p.workspaces
	p.mu.Unlock()
	workspaces := make(map[workspacecore.ID]*workspacecore.Workspace)
	activity := make(map[workspacecore.ID]map[string]time.Time)
	for key, stager := range stagers {
		workspaces[key.workspace] = stager.workspace
		if activity[key.workspace] == nil {
			activity[key.workspace] = make(map[string]time.Time)
		}
		activity[key.workspace][key.planID] = stager.lastUse()
	}
	if source != nil {
		for _, workspace := range source() {
			workspaces[workspace.Identity().ID] = workspace
		}
	}
	expired := make(map[stagerKey]bool)
	for id, workspace := range workspaces {
		plans, err := workspace.ExpireIdlePlans(now, p.planIdleTTL, activity[id])
		if err != nil {
			log.Printf("huyang: expire idle plans of workspace %s: %v", id, err)
		}
		for _, plan := range plans {
			log.Printf("huyang: plan %s of workspace %s expired after %s idle", plan.PlanID, id, p.planIdleTTL)
			expired[stagerKey{workspace: id, planID: plan.PlanID}] = true
		}
	}
	p.dropUnneededStagers(stagers, expired, now)
	return len(expired)
}

// dropUnneededStagers rolls back and unregisters the stagers whose plan no
// longer needs them. A stager of a plan that just expired goes at once; any
// other goes once it has been unused for stagerReleaseGrace. Rollback of a
// stager that already released its sandbox does nothing, and one that fails
// stays registered so the next sweep tries again.
func (p *Pool) dropUnneededStagers(stagers map[stagerKey]*SandboxStager, expired map[stagerKey]bool, now time.Time) {
	for key, stager := range stagers {
		if !expired[key] && (stagerNeeded(stager) || now.Sub(stager.lastUse()) < stagerReleaseGrace) {
			continue
		}
		if err := stager.Rollback(context.Background(), key.planID); err != nil {
			log.Printf("huyang: release sandbox of plan %s: %v", key.planID, err)
			continue
		}
		p.mu.Lock()
		if p.stagers[key] == stager {
			delete(p.stagers, key)
		}
		p.mu.Unlock()
	}
}

// stagerNeeded reports whether a stager still serves its plan: its sandbox is
// not released and the plan is being prepared, is prepared, or is in the
// middle of being applied or rolled back.
func stagerNeeded(stager *SandboxStager) bool {
	if stager.finished() {
		return false
	}
	plan, err := stager.workspace.InspectPlan(stager.planID, 0)
	if err != nil {
		return false
	}
	switch plan.State {
	case workspacecore.PlanPreparing, workspacecore.PlanReady, workspacecore.PlanProvisional,
		workspacecore.PlanCommitting, workspacecore.PlanRollingBack:
		return true
	}
	return false
}
