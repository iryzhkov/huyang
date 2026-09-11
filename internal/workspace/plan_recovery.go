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
		restartRestored := false
		for i := len(plan.Events) - 1; i >= 0; i-- {
			event := plan.Events[i]
			if event.Action == "provider_restart_restore" &&
				event.Outcome == "provider_buffers_discarded_reprepare_available" {
				restartRestored = true
				break
			}
			if event.Action == "prepare" || event.Action == "prepare_failed" {
				break
			}
		}
		if !restartRestored {
			continue
		}
		return clonePlan(plan), true
	}
	return PlanRecord{}, false
}
