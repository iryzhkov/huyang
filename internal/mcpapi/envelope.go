package mcpapi

import (
	"encoding/json"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// APIVersion is the contract revision every envelope declares.
const APIVersion = "huyang.workspace/v1alpha1"

// MaxNextEntries bounds the next array the output schema advertises.
const MaxNextEntries = 2

// envelopeAudit, when set, observes every finalised envelope. Production
// never sets it: the output schema is enforced structurally by
// FinalizeEnvelope, and full validation against the schema is test-only. The
// test suites install ValidateOutput here through SetEnvelopeAudit so
// every envelope produced by any package is checked.
var envelopeAudit func(tool string, envelope map[string]any)

// SetEnvelopeAudit installs the envelope observer. It exists for the test
// suites; see envelopeAudit.
func SetEnvelopeAudit(audit func(tool string, envelope map[string]any)) {
	envelopeAudit = audit
}

// ValidateOutput checks an envelope against the advertised output
// schema after a JSON round trip, exactly as a client would see it. It is
// the test-only half of output-schema validation: production relies on
// FinalizeEnvelope and never validates its own results.
func ValidateOutput(value map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return err
	}
	return validateSchemaValue(OutputEnvelopeSchema(), decoded, "result")
}

// FinalizeEnvelope is the single place every tool result passes through
// before it leaves the service. It enforces the invariants the output schema
// declares: the required keys exist with their declared types and next holds
// at most MaxNextEntries entries, keeping the earliest, most specific ones.
func FinalizeEnvelope(tool string, result map[string]any) map[string]any {
	if result == nil {
		result = map[string]any{}
	}
	if _, ok := result["api_version"]; !ok {
		result["api_version"] = APIVersion
	}
	if _, ok := result["warnings"].([]string); !ok {
		result["warnings"] = []string{}
	}
	if _, ok := result["evidence"]; !ok {
		result["evidence"] = map[string]any{"ids": []string{}, "truncated": false}
	}
	if _, ok := result["data"]; !ok || result["data"] == nil {
		result["data"] = map[string]any{}
	}
	switch next := result["next"].(type) {
	case nil:
		result["next"] = []any{}
	case []any:
		if len(next) > MaxNextEntries {
			result["next"] = next[:MaxNextEntries]
		}
	case []map[string]any:
		bounded := make([]any, 0, MaxNextEntries)
		for _, item := range next {
			if len(bounded) == MaxNextEntries {
				break
			}
			bounded = append(bounded, item)
		}
		result["next"] = bounded
	}
	if envelopeAudit != nil {
		envelopeAudit(tool, result)
	}
	return result
}

func CloneEnvelope(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func UniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func NonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func Failure(requestID string, workspace *workspacecore.Workspace, code string, err error) map[string]any {
	return Envelope(requestID, workspace, "failed", code, err.Error(), map[string]any{})
}

func Envelope(requestID string, workspace *workspacecore.Workspace, outcome, code, summary string, data any) map[string]any {
	result := map[string]any{
		"api_version": APIVersion, "request_id": requestID, "outcome": outcome,
		"summary": summary, "data": data,
		"evidence": map[string]any{"ids": []string{}, "truncated": false},
		"warnings": []string{}, "next": []any{},
	}
	if code != "" {
		result["code"] = code
	}
	if workspace != nil {
		result["workspace"] = workspace.Identity()
	}
	return result
}

func AnySlice(value any) []any {
	if values, ok := value.([]any); ok {
		return values
	}
	return nil
}
