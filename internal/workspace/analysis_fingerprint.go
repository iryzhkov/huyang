package workspace

import "encoding/json"

// pipelineFingerprint is the part of the project's configuration that can
// change what an analysis concludes: the commands, the caps and the variants.
// It is part of a snapshot's identity, so raising a cap or declaring a variant
// produces a new snapshot instead of reusing one that could not have known.
func pipelineFingerprint(policy PipelinePolicy) string {
	encoded, err := json.Marshal(struct {
		Impact   ImpactPolicy    `json:"impact"`
		Variants []VariantPolicy `json:"variants"`
		Tests    []CommandPolicy `json:"tests"`
		Check    []CommandPolicy `json:"check"`
	}{Impact: policy.Impact, Variants: policy.Variants, Tests: policy.Tests, Check: policy.Check})
	if err != nil {
		return "unencodable"
	}
	return hashBytes(encoded)[:16]
}
