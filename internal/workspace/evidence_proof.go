package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// baselineKnownLocked checks immutable same-lane proof of the exact preimage.
func (s *diagnosticStore) baselineKnownLocked(batch DiagnosticBatch, evidenceIDs []string) bool {
	baselineKnown := batch.Baseline
	// Postimage reads invalidate current lane coverage, not immutable proof of
	// the exact preimage. Recheck that payload's own authority below instead.
	if !baselineKnown && batch.PreEditDocumentRevision != "" {
		for _, id := range evidenceIDs {
			var previous DiagnosticBatch
			if evidence, ok := s.state.Evidence[id]; ok && json.Unmarshal(evidence.Payload, &previous) == nil &&
				previous.DocumentRevision == batch.PreEditDocumentRevision && !previous.Staged &&
				previous.Producer == batch.Producer && previous.ProducerVersion == batch.ProducerVersion {
				if proof, _ := confidenceFor(previous); proof != ConfidenceAuthoritative {
					continue
				}
				baselineKnown = true
				break
			}
		}
	}
	return baselineKnown
}

// baselineProofKey deduplicates only proven canonical baseline storage.
// Observation time/state and mutation hints are not proof. All other publication
// fields, exact native revision, finding placement and multiplicity are retained.
func baselineProofKey(batch DiagnosticBatch, confidence DiagnosticConfidence) string {
	if !batch.Baseline || batch.Staged || batch.DocumentRevision == "" || confidence != ConfidenceAuthoritative {
		return ""
	}
	batch.ObservedAt, batch.StateSeq = time.Time{}, 0
	batch.TransactionID, batch.Candidates, batch.PreEditDocumentRevision = "", nil, ""
	if batch.Dimension == "" {
		batch.Dimension = "edited_documents"
	}
	batch.Findings = append([]DiagnosticFinding(nil), batch.Findings...)
	for i := range batch.Findings {
		batch.Findings[i] = normalizeFinding(batch.Findings[i])
	}
	sort.Slice(batch.Findings, func(i, j int) bool {
		left, _ := json.Marshal(batch.Findings[i])
		right, _ := json.Marshal(batch.Findings[j])
		return string(left) < string(right)
	})
	payload, _ := json.Marshal(batch)
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}
