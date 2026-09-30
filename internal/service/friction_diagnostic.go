package service

import (
	"encoding/json"
	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// Inspect only known state fields. Source-sized read/search payloads are
// neither copied nor serialized to extract diagnostic metadata.
func frictionDiagnosticState(value any) ([]string, string, *bool) {
	data, _ := value.(map[string]any)
	var changed *bool
	if v, ok := data["canonical_changed"].(bool); ok {
		changed = &v
	}
	reasons := []string{}
	confidence := ""
	add := func(value any) {
		switch values := value.(type) {
		case []string:
			reasons = append(reasons, values...)
		case []any:
			for _, value := range values {
				if code, ok := value.(string); ok {
					reasons = append(reasons, code)
				}
			}
		}
	}
	collect := func(report map[string]any) {
		if report == nil {
			return
		}
		if candidate, ok := report["confidence"].(string); ok {
			switch candidate {
			case "authoritative", "corroborated", "provisional", "unavailable":
				confidence = candidate
			}
		}
		add(report["reason_codes"])
		add(report["reasons"])
		// Coverage may be typed in-process; normalize just this bounded schema
		// field, never the diagnostic findings or the tool's source content.
		if coverage := report["coverage"]; coverage != nil {
			raw, err := json.Marshal(coverage)
			var dimensions map[string]any
			if err == nil && json.Unmarshal(raw, &dimensions) == nil {
				for _, dimension := range dimensions {
					if item, ok := dimension.(map[string]any); ok {
						add(item["reasons"])
					}
				}
			}
		}
	}
	verification, _ := data["verification"].(map[string]any)
	collect(verification)
	diagnostics, _ := data["diagnostics"].(map[string]any)
	collect(diagnostics)
	return mcpapi.DiagnosticReasonCodes(reasons), confidence, changed
}
