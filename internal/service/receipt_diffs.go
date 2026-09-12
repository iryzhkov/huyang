package service

// editReceiptDiffs returns the per-file diff records an edit_apply receipt
// carries: the compact response lists them under diffs, and receipts written
// by earlier builds carry one under change.diff.
func editReceiptDiffs(data map[string]any) []map[string]any {
	var diffs []map[string]any
	switch values := data["diffs"].(type) {
	case []map[string]any:
		diffs = append(diffs, values...)
	case []any:
		for _, value := range values {
			if diff, ok := value.(map[string]any); ok {
				diffs = append(diffs, diff)
			}
		}
	}
	if len(diffs) > 0 {
		return diffs
	}
	if change, ok := data["change"].(map[string]any); ok {
		if diff, ok := change["diff"].(map[string]any); ok {
			return []map[string]any{diff}
		}
	}
	return nil
}
