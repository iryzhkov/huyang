package workspace

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultPlanIdleTTL is how long a plan nobody touches keeps what it holds.
//
// A prepared plan owns a sandbox copy of the tree and a Neovim provider with
// its language servers, and until it is applied or discarded nothing else
// would ever release them. Agents prepare and apply within one exchange -
// seconds, or minutes when they read the prepared revision or verify it first
// - so an hour without any use means the plan was abandoned, not that someone
// is still reviewing it. Every use of the plan or its preparation restarts
// the hour, and an expired plan can be prepared again from its operations.
const DefaultPlanIdleTTL = time.Hour

// PlanIdleTTLVariable overrides DefaultPlanIdleTTL with a Go duration such as
// "30m" or "4h"; "0" or "off" turns expiry off.
const PlanIdleTTLVariable = "HUYANG_PLAN_IDLE_TTL"

var planIdleTTLWarning sync.Once

// PlanIdleTTL is the configured idle limit for plans, or zero when expiry is
// off. A value that does not parse keeps the default and says so once, rather
// than turning expiry off by accident.
func PlanIdleTTL() time.Duration {
	value := strings.TrimSpace(os.Getenv(PlanIdleTTLVariable))
	switch strings.ToLower(value) {
	case "":
		return DefaultPlanIdleTTL
	case "off", "false", "0":
		return 0
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 {
		planIdleTTLWarning.Do(func() {
			log.Printf("huyang: %s=%q is not a non-negative duration; using %s", PlanIdleTTLVariable, value, DefaultPlanIdleTTL)
		})
		return DefaultPlanIdleTTL
	}
	return parsed
}

// planExpirable reports whether a plan is waiting on its author and can
// therefore be abandoned: an intent nobody prepared, a preparation nobody
// applied, or a preparation a restart discarded that apply would recreate.
// A plan in the middle of an operation (PREPARING, COMMITTING, ROLLING_BACK)
// is never expired underneath it, and a terminal plan already has retention.
func planExpirable(plan PlanRecord) bool {
	switch plan.State {
	case PlanOpen, PlanPreviewed, PlanReady, PlanProvisional:
		return true
	case PlanFailed:
		return plan.Preparation != nil && restartRestored(plan)
	}
	return false
}

// planLastActivity is when the plan was last changed by somebody. The event a
// service restart records is not somebody: counting it would give every
// abandoned preparation a fresh hour each time the service starts.
func planLastActivity(plan PlanRecord) time.Time {
	for i := len(plan.Events) - 1; i >= 0; i-- {
		if plan.Events[i].Action != "provider_restart_restore" {
			return plan.Events[i].At
		}
	}
	return plan.UpdatedAt
}

// ExpireIdlePlans moves every plan idle for longer than ttl to EXPIRED and
// returns the plans it moved. activity holds, per plan ID, the last time the
// plan was used without its record changing - a read or verification of its
// prepared revision, a lookup by apply - which counts as use as well. The
// caller owns whatever the plans held and releases it: this only records that
// nothing may be applied from them any more. A zero ttl expires nothing.
func (w *Workspace) ExpireIdlePlans(now time.Time, ttl time.Duration, activity map[string]time.Time) ([]PlanRecord, error) {
	w.plansMu.Lock()
	expired, err := w.expireIdlePlansLocked(now, ttl, activity)
	if err == nil && len(expired) > 0 {
		_, _, err = w.pruneTerminalPlansLocked(now)
	}
	w.plansMu.Unlock()
	// A prepared plan holds the prepare lease until it is applied or rolled
	// back; an expired one will be neither.
	w.prepareMu.Lock()
	for _, plan := range expired {
		delete(w.activePlans, plan.PlanID)
	}
	w.prepareMu.Unlock()
	return expired, err
}

// expireIdlePlansLocked is ExpireIdlePlans without the prepare lease and
// retention, for loadPlans, which does both itself. The caller holds plansMu.
func (w *Workspace) expireIdlePlansLocked(now time.Time, ttl time.Duration, activity map[string]time.Time) ([]PlanRecord, error) {
	if ttl <= 0 {
		return nil, nil
	}
	var expired []PlanRecord
	for id, previous := range w.plans {
		if !planExpirable(previous) {
			continue
		}
		last := planLastActivity(previous)
		if used := activity[id]; used.After(last) {
			last = used
		}
		idle := now.Sub(last)
		if idle <= ttl {
			continue
		}
		plan := clonePlan(previous)
		plan.State = PlanExpired
		recordPlanEventAt(&plan, "expire", fmt.Sprintf("idle %s, limit %s", idle.Round(time.Second), ttl), now)
		w.plans[id] = plan
		if err := w.persistPlanLocked(id); err != nil {
			w.plans[id] = previous
			return expired, err
		}
		expired = append(expired, clonePlan(plan))
	}
	return expired, nil
}

// ExpiredPlanError is the refusal for an action that needs a preparation an
// expired plan no longer has. It names the way back, because the plan itself
// is intact: only its sandbox and language servers were released.
func ExpiredPlanError(plan PlanRecord) error {
	return Codedf(CodePlanStateInvalid,
		"plan %s expired: it was idle for longer than the plan idle limit (%s, default %s) and its sandbox and language servers were released; "+
			"prepare it again with change_plan action=prepare plan_id=%s plan_revision=%d, then apply the new prepared_revision",
		plan.PlanID, PlanIdleTTLVariable, DefaultPlanIdleTTL, plan.PlanID, plan.PlanRevision)
}
