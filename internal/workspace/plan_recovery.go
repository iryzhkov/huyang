package workspace

// RecoverablePreparedPlan finds a durable prepared plan whose provider-local
// sandbox was discarded by a service restart and can be recreated exactly.
func (w *Workspace) RecoverablePreparedPlan(reference string) (PlanRecord, bool) {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	for _, plan := range w.plans {
		if plan.State != PlanFailed || plan.Preparation == nil {
			continue
		}
		if plan.PlanID != reference && plan.Preparation.PreparedRevision != reference {
			continue
		}
		if !restartRestored(plan) {
			continue
		}
		return clonePlan(plan), true
	}
	return PlanRecord{}, false
}

// restartRestored reports whether a plan's latest preparation was discarded by
// a service restart rather than by a failed prepare of its own.
func restartRestored(plan PlanRecord) bool {
	for i := len(plan.Events) - 1; i >= 0; i-- {
		event := plan.Events[i]
		if event.Action == "provider_restart_restore" &&
			event.Outcome == "provider_buffers_discarded_reprepare_available" {
			return true
		}
		if event.Action == "prepare" || event.Action == "prepare_failed" {
			return false
		}
	}
	return false
}
