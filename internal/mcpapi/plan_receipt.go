package mcpapi

import "encoding/json"

// Plans retain evidence and acceptance semantics. Only bulky operation bodies,
// diffs and tool deltas are omitted; inspect remains available for full details.
func compactPlanReceipt(envelope, data, arguments map[string]any) map[string]any {
	raw, err := json.Marshal(data["plan"])
	if err != nil {
		return envelope
	}
	var plan map[string]any
	if json.Unmarshal(raw, &plan) != nil || plan == nil {
		return envelope
	}
	receipt := CloneEnvelope(data)
	selected := CloneEnvelope(plan)
	for _, key := range []string{"operations", "preview", "events"} {
		delete(selected, key)
	}
	if prep, ok := plan["preparation"].(map[string]any); ok {
		selected["preparation"] = selectReceiptFields(prep, []string{
			"prepared_revision", "provider_epoch", "affected_files", "canonical_changed",
			"canonical_revision", "canonical_from_revision", "base_revision", "journal_id",
			"diagnostics", "disk_checks", "intermediate_reports", "verification",
			"provisional_accepted", "missing_coverage",
		})
		if arguments["include_diagnostics"] == true {
			selected["preparation"] = prep
		}
	}
	receipt["plan"] = selected
	receipt["details_omitted"] = true
	receipt["details"] = map[string]any{"tool": "change_plan", "action": "inspect", "plan_id": plan["plan_id"], "response_mode": "full"}
	envelope["data"] = receipt
	return envelope
}

func selectReceiptFields(source map[string]any, keys []string) map[string]any {
	selected := map[string]any{}
	for _, key := range keys {
		if value, ok := source[key]; ok {
			selected[key] = value
		}
	}
	return selected
}
