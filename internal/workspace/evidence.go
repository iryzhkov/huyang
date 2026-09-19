package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const diagnosticStateVersion = 1

type DiagnosticConfidence string

const (
	ConfidenceAuthoritative DiagnosticConfidence = "authoritative"
	ConfidenceCorroborated  DiagnosticConfidence = "corroborated"
	ConfidenceProvisional   DiagnosticConfidence = "provisional"
	ConfidenceUnavailable   DiagnosticConfidence = "unavailable"
)

type DiagnosticEvidenceKind string

const (
	EvidencePush         DiagnosticEvidenceKind = "lsp_push"
	EvidencePull         DiagnosticEvidenceKind = "lsp_pull"
	EvidenceWorkspace    DiagnosticEvidenceKind = "workspace_diagnostic"
	EvidenceProjectCheck DiagnosticEvidenceKind = "project_check"
	EvidenceUnavailable  DiagnosticEvidenceKind = "unavailable"
)

type DiagnosticRange struct {
	StartLine      int `json:"start_line"`
	StartCharacter int `json:"start_character"`
	EndLine        int `json:"end_line"`
	EndCharacter   int `json:"end_character"`
}

type DiagnosticFinding struct {
	Range    DiagnosticRange `json:"range"`
	Severity int             `json:"severity"`
	Code     string          `json:"code,omitempty"`
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}

type CulpritCandidate struct {
	TransactionID         string `json:"transaction_id"`
	ExactSandbox          bool   `json:"exact_sandbox,omitempty"`
	PostimageVersionMatch bool   `json:"postimage_version_match,omitempty"`
	ChangedSymbol         bool   `json:"changed_symbol,omitempty"`
	ImpactPredecessor     bool   `json:"impact_predecessor,omitempty"`
	ExternalChange        bool   `json:"external_change,omitempty"`
}

type CulpritAttribution struct {
	Rank       string   `json:"rank"`
	Candidates []string `json:"candidates,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
}

type DiagnosticBatch struct {
	Kind             DiagnosticEvidenceKind `json:"kind"`
	ProviderID       string                 `json:"provider_id"`
	Producer         string                 `json:"producer"`
	ProducerVersion  string                 `json:"producer_version,omitempty"`
	Document         string                 `json:"document,omitempty"`
	DocumentRevision string                 `json:"document_revision,omitempty"`
	DocumentVersion  *int64                 `json:"document_version,omitempty"`
	ExpectedVersion  *int64                 `json:"expected_version,omitempty"`
	ResultID         string                 `json:"result_id,omitempty"`
	TransactionID    string                 `json:"transaction_id,omitempty"`
	StateSeq         uint64                 `json:"state_seq"`
	Complete         bool                   `json:"complete"`
	TimedOut         bool                   `json:"timed_out,omitempty"`
	Reason           string                 `json:"reason,omitempty"`
	ProgressPending  bool                   `json:"progress_pending,omitempty"`
	ChangeBarrier    bool                   `json:"change_barrier,omitempty"`
	Selected         bool                   `json:"selected"`
	// Staged marks evidence about bytes that only exist inside a preparation.
	// It is recorded as evidence and kept out of the ledger's current set: a
	// proposal nobody applied must not change what the workspace believes
	// about its own files, in either direction.
	Staged     bool                `json:"staged,omitempty"`
	Dimension  string              `json:"dimension,omitempty"`
	Findings   []DiagnosticFinding `json:"findings,omitempty"`
	Candidates []CulpritCandidate  `json:"candidates,omitempty"`
	ObservedAt time.Time           `json:"observed_at,omitempty"`
}

type DiagnosticItem struct {
	ID               string                   `json:"id"`
	ProviderID       string                   `json:"provider_id"`
	Producer         string                   `json:"producer"`
	ProducerVersion  string                   `json:"producer_version,omitempty"`
	Document         string                   `json:"document"`
	DocumentRevision string                   `json:"document_revision,omitempty"`
	DocumentVersion  *int64                   `json:"document_version,omitempty"`
	TransactionID    string                   `json:"transaction_id,omitempty"`
	StateSeq         uint64                   `json:"state_seq"`
	Finding          DiagnosticFinding        `json:"finding"`
	EvidenceKinds    []DiagnosticEvidenceKind `json:"evidence_kinds"`
	EvidenceIDs      []string                 `json:"evidence_ids"`
	FirstSeen        time.Time                `json:"first_seen"`
	LastSeen         time.Time                `json:"last_seen"`
	Attribution      CulpritAttribution       `json:"attribution"`
	// Status is one of DiagnosticStatusCurrent, DiagnosticStatusStale or
	// DiagnosticStatusResolved. A stale item was recorded against document
	// content that has since changed and the producer has not re-published
	// for the new content; it is neither current nor verified resolved.
	Status        string    `json:"status,omitempty"`
	StaleReason   string    `json:"stale_reason,omitempty"`
	StaleRevision string    `json:"stale_revision,omitempty"`
	ResolvedAt    time.Time `json:"resolved_at,omitempty"`
}

const (
	DiagnosticStatusCurrent  = "current"
	DiagnosticStatusStale    = "stale"
	DiagnosticStatusResolved = "resolved"

	staleReasonDocumentChanged = "document_changed"
)

// Retention bounds for the diagnostic ledger. Items that are neither current
// nor stale, evidence nobody references and consumed notices are pruned so a
// long-lived workspace does not accumulate every observation it ever made.
const (
	// diagnosticRetentionWindow is how long resolved items and unreferenced
	// evidence stay in the ledger after they were last seen.
	diagnosticRetentionWindow = 24 * time.Hour
	// maxInactiveDiagnosticItems caps resolved items kept regardless of age;
	// the least recently seen are dropped first.
	maxInactiveDiagnosticItems = 1000
	// maxUnreferencedDiagnosticEvidence caps evidence payloads that no
	// retained item and no coverage dimension references.
	maxUnreferencedDiagnosticEvidence = 500
	// maxDiagnosticNotices caps retained notices. Acknowledged notices are
	// dropped as soon as they are acknowledged; beyond the cap the oldest
	// unacknowledged notices are dropped and the notice floor records the
	// gap so cursors older than the floor are reported as truncated.
	maxDiagnosticNotices = 1000
)

type DiagnosticDimension struct {
	State       string               `json:"state"`
	Confidence  DiagnosticConfidence `json:"confidence"`
	EvidenceIDs []string             `json:"evidence_ids,omitempty"`
	Reasons     []string             `json:"reasons,omitempty"`
}

type DiagnosticNotice struct {
	Cursor      string             `json:"cursor"`
	Kind        string             `json:"kind"`
	ID          string             `json:"id"`
	Severity    int                `json:"severity,omitempty"`
	Document    string             `json:"document,omitempty"`
	Attribution CulpritAttribution `json:"attribution,omitempty"`
}

type DiagnosticReport struct {
	Confidence         DiagnosticConfidence           `json:"confidence"`
	Coverage           map[string]DiagnosticDimension `json:"coverage"`
	New                []DiagnosticItem               `json:"new"`
	Current            []DiagnosticItem               `json:"current,omitempty"`
	Resolved           []DiagnosticItem               `json:"resolved"`
	Stale              []DiagnosticItem               `json:"stale,omitempty"`
	PreexistingCount   int                            `json:"preexisting_count"`
	CurrentCount       int                            `json:"current_count"`
	StaleCount         int                            `json:"stale_count"`
	ProvisionalReasons []string                       `json:"provisional_reasons,omitempty"`
	EvidenceIDs        []string                       `json:"evidence_ids"`
	Cursor             string                         `json:"cursor"`
	Notices            []DiagnosticNotice             `json:"notices,omitempty"`
	// NoticesTruncated reports that notices between the caller's cursor and
	// the retained notice floor were pruned, so the delta is incomplete.
	NoticesTruncated bool `json:"notices_truncated,omitempty"`
}

// DiagnosticNoticePage is a bounded delta of notices strictly after a cursor.
type DiagnosticNoticePage struct {
	Notices   []DiagnosticNotice `json:"notices"`
	Cursor    string             `json:"cursor"`
	Truncated bool               `json:"truncated,omitempty"`
	More      bool               `json:"more,omitempty"`
}

type DiagnosticEvidence struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	RecordedAt time.Time       `json:"recorded_at"`
	Payload    json.RawMessage `json:"payload"`
}

type diagnosticState struct {
	Version            int                            `json:"version"`
	Sequence           uint64                         `json:"sequence"`
	Ack                uint64                         `json:"ack"`
	Items              map[string]DiagnosticItem      `json:"items"`
	Active             map[string][]string            `json:"active"`
	Stale              map[string][]string            `json:"stale,omitempty"`
	NoticeFloor        uint64                         `json:"notice_floor,omitempty"`
	Evidence           map[string]DiagnosticEvidence  `json:"evidence"`
	Notices            []DiagnosticNotice             `json:"notices"`
	Dimensions         map[string]DiagnosticDimension `json:"dimensions"`
	ProviderDimensions map[string]DiagnosticDimension `json:"provider_dimensions"`
}

type diagnosticStore struct {
	mu    sync.Mutex
	path  string
	state diagnosticState
	// delivered is the highest notice sequence handed out by pendingNotices
	// during this process lifetime. It is deliberately not persisted: after a
	// restart unacknowledged notices are delivered again, which is the
	// conservative direction.
	delivered uint64
}

func newDiagnosticStore(stateDir string, workspaceID ID) (*diagnosticStore, error) {
	store := &diagnosticStore{state: diagnosticState{
		Version: diagnosticStateVersion, Items: map[string]DiagnosticItem{}, Active: map[string][]string{}, Stale: map[string][]string{},
		Evidence: map[string]DiagnosticEvidence{}, Dimensions: map[string]DiagnosticDimension{}, ProviderDimensions: map[string]DiagnosticDimension{},
	}}
	if stateDir == "" {
		return store, nil
	}
	store.path = filepath.Join(stateDir, "diagnostics", string(workspaceID)+".json")
	content, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read diagnostic state: %w", err)
	}
	if err := json.Unmarshal(content, &store.state); err != nil {
		return nil, fmt.Errorf("decode diagnostic state: %w", err)
	}
	if store.state.Version != diagnosticStateVersion {
		return nil, fmt.Errorf("unsupported diagnostic state version %d", store.state.Version)
	}
	if store.state.Items == nil {
		store.state.Items = map[string]DiagnosticItem{}
	}
	if store.state.Active == nil {
		store.state.Active = map[string][]string{}
	}
	if store.state.Stale == nil {
		store.state.Stale = map[string][]string{}
	}
	if store.state.Evidence == nil {
		store.state.Evidence = map[string]DiagnosticEvidence{}
	}
	if store.state.Dimensions == nil {
		store.state.Dimensions = map[string]DiagnosticDimension{}
	}
	if store.state.ProviderDimensions == nil {
		store.state.ProviderDimensions = map[string]DiagnosticDimension{}
	}
	return store, nil
}

func (s *diagnosticStore) save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.path, append(content, '\n'), 0o600)
}

func normalizeFinding(f DiagnosticFinding) DiagnosticFinding {
	f.Message = strings.Join(strings.Fields(f.Message), " ")
	f.Source = strings.TrimSpace(f.Source)
	f.Code = strings.TrimSpace(f.Code)
	return f
}

// diagnosticFingerprint identifies a finding by what it says rather than by
// where it currently sits. The range is deliberately left out: an edit above
// an untouched warning moves it, and a fingerprint that included the line
// would retire that warning and announce an identical one, so every edit
// reported diagnostics it had not caused. occurrence separates findings that
// are otherwise identical within one document, so two of the same message
// stay two items.
func diagnosticFingerprint(providerID, document string, finding DiagnosticFinding, occurrence int) string {
	encoded, _ := json.Marshal([]any{
		providerID, filepath.ToSlash(filepath.Clean(document)),
		finding.Severity, finding.Code, finding.Source, finding.Message, occurrence,
	})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}

// findingIdentity is the part of a finding its fingerprint is taken over.
func findingIdentity(finding DiagnosticFinding) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s", finding.Severity, finding.Code, finding.Source, finding.Message)
}

func evidenceID(batch DiagnosticBatch, sequence uint64) string {
	encoded, _ := json.Marshal(batch)
	sum := sha256.Sum256(append(encoded, fmt.Appendf(nil, ":%d", sequence)...))
	return "ev_" + hex.EncodeToString(sum[:16])
}

func confidenceFor(batch DiagnosticBatch) (DiagnosticConfidence, []string) {
	if batch.TimedOut {
		return ConfidenceProvisional, []string{"diagnostic_barrier_timed_out"}
	}
	if !batch.Selected {
		return ConfidenceUnavailable, []string{"provider_not_selected"}
	}
	if batch.ProgressPending {
		return ConfidenceProvisional, []string{"work_done_progress_pending"}
	}
	switch batch.Kind {
	case EvidencePush:
		if batch.Complete && (batch.ChangeBarrier ||
			(batch.DocumentVersion != nil && batch.ExpectedVersion != nil && *batch.DocumentVersion == *batch.ExpectedVersion)) {
			return ConfidenceAuthoritative, nil
		}
		return ConfidenceProvisional, []string{"push_missing_current_document_proof"}
	case EvidencePull:
		if batch.Complete && batch.ResultID != "" {
			return ConfidenceAuthoritative, nil
		}
		return ConfidenceProvisional, []string{"pull_incomplete_or_missing_result_id"}
	case EvidenceWorkspace:
		if batch.Complete && batch.ResultID != "" {
			return ConfidenceAuthoritative, nil
		}
		return ConfidenceProvisional, []string{"workspace_diagnostics_incomplete"}
	case EvidenceProjectCheck:
		if batch.Complete {
			return ConfidenceCorroborated, nil
		}
		return ConfidenceUnavailable, []string{"project_check_unavailable"}
	default:
		reason := strings.TrimSpace(batch.Reason)
		if reason == "" {
			reason = "unsupported_diagnostic_evidence"
		}
		return ConfidenceUnavailable, []string{reason}
	}
}

func rankCulprit(candidates []CulpritCandidate) CulpritAttribution {
	var exact, strong, likely []string
	reasons := map[string]bool{}
	for _, candidate := range candidates {
		switch {
		case candidate.ExactSandbox:
			exact = append(exact, candidate.TransactionID)
			reasons["sandbox_producer_version"] = true
		case candidate.PostimageVersionMatch && !candidate.ExternalChange:
			strong = append(strong, candidate.TransactionID)
			reasons["postimage_version_match"] = true
		case candidate.ChangedSymbol || candidate.ImpactPredecessor:
			likely = append(likely, candidate.TransactionID)
			if candidate.ChangedSymbol {
				reasons["changed_symbol"] = true
			}
			if candidate.ImpactPredecessor {
				reasons["impact_predecessor"] = true
			}
		}
	}
	rank, selected := "unattributed", []string(nil)
	switch {
	case len(exact) == 1:
		rank, selected = "exact", exact
	case len(exact) > 1:
		rank, selected = "ambiguous", exact
	case len(strong) == 1:
		rank, selected = "strong", strong
	case len(strong) > 1:
		rank, selected = "ambiguous", strong
	case len(likely) == 1:
		rank, selected = "likely", likely
	case len(likely) > 1:
		rank, selected = "ambiguous", likely
	}
	sort.Strings(selected)
	var why []string
	for reason := range reasons {
		why = append(why, reason)
	}
	sort.Strings(why)
	return CulpritAttribution{Rank: rank, Candidates: selected, Reasons: why}
}

func (s *diagnosticStore) record(batch DiagnosticBatch) (DiagnosticReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch.ProviderID == "" || batch.Producer == "" {
		return DiagnosticReport{}, errors.New("diagnostic evidence requires provider_id and producer")
	}
	if !batch.Selected {
		return DiagnosticReport{}, errors.New("diagnostic evidence from an unselected provider is rejected")
	}
	if batch.ObservedAt.IsZero() {
		batch.ObservedAt = time.Now().UTC()
	}
	batch.Document = filepath.ToSlash(filepath.Clean(batch.Document))
	confidence, reasons := confidenceFor(batch)
	if batch.Staged {
		return s.recordStagedLocked(batch, confidence, reasons)
	}
	evID := s.recordEvidenceLocked(batch, confidence, reasons)
	key := batch.ProviderID + "\x00" + batch.Document
	previous := append([]string(nil), s.state.Active[key]...)
	previousStale := append([]string(nil), s.state.Stale[key]...)
	current, added, seen := s.recordFindingsLocked(batch, evID)
	var resolved []DiagnosticItem
	if batch.Complete && confidenceRank(confidence) >= confidenceRank(ConfidenceCorroborated) {
		// A complete observation of this document verifies the absence of
		// everything it did not report: previously current items and items
		// left stale by a content change are both resolved.
		resolved = s.resolveAbsentLocked(append(previous, previousStale...), seen, batch.ObservedAt)
		s.state.Active[key] = idsOf(current)
		delete(s.state.Stale, key)
	} else {
		s.state.Active[key] = appendUnique(previous, idsOf(current)...)
		s.state.Stale[key] = withoutIDs(previousStale, seen)
	}
	s.pruneLocked(batch.ObservedAt)
	if err := s.save(); err != nil {
		return DiagnosticReport{}, err
	}
	report := s.reportLocked(added, resolved, evID, reasons)
	// Mutation-time verification needs the confidence of this observation. The
	// persisted coverage remains historical and is returned by diagnostics queries.
	report.Confidence = confidence
	report.Current = append([]DiagnosticItem(nil), current...)
	report.ProvisionalReasons = append([]string(nil), reasons...)
	return report, nil
}

// recordEvidenceLocked stores the batch as an evidence record and folds its confidence
// into the coverage dimension it reports on. It returns the new evidence ID.
// recordStagedLocked keeps an observation of staged bytes out of the ledger.
//
// The findings are real and the caller needs them - they are how a preparation
// knows whether it is applicable - but they describe a revision the workspace
// does not hold. Letting them into the current set corrupts the baseline in
// both directions: a proposal that fixes an error would mark the canonical
// file clean while it is still broken, and a proposal that introduces one
// would put it in the ledger as something that was already there. The evidence
// record is still written, so the preparation can point at it.
func (s *diagnosticStore) recordStagedLocked(batch DiagnosticBatch, confidence DiagnosticConfidence, reasons []string) (DiagnosticReport, error) {
	s.state.Sequence++
	evID := evidenceID(batch, s.state.Sequence)
	payload, _ := json.Marshal(batch)
	s.state.Evidence[evID] = DiagnosticEvidence{ID: evID, Kind: string(batch.Kind), RecordedAt: batch.ObservedAt, Payload: payload}
	s.pruneLocked(batch.ObservedAt)
	if err := s.save(); err != nil {
		return DiagnosticReport{}, err
	}
	current := stagedItems(batch, evID)
	return DiagnosticReport{
		Confidence: confidence, Coverage: cloneDimensions(s.state.Dimensions),
		New: current, Current: current, CurrentCount: len(current),
		ProvisionalReasons: append([]string(nil), reasons...),
		EvidenceIDs:        []string{evID}, Cursor: fmt.Sprintf("diagcur_%d", s.state.Sequence),
	}, nil
}

// stagedItems are one staged batch's findings as items, built for the caller
// and stored nowhere.
func stagedItems(batch DiagnosticBatch, evID string) []DiagnosticItem {
	seen := map[string]bool{}
	occurrences := map[string]int{}
	var items []DiagnosticItem
	for _, finding := range batch.Findings {
		finding = normalizeFinding(finding)
		identity := findingIdentity(finding)
		id := "diag_" + diagnosticFingerprint(batch.ProviderID, batch.Document, finding, occurrences[identity])
		occurrences[identity]++
		if seen[id] {
			continue
		}
		seen[id] = true
		items = append(items, DiagnosticItem{
			ID: id, ProviderID: batch.ProviderID, Producer: batch.Producer, ProducerVersion: batch.ProducerVersion,
			Document: batch.Document, DocumentRevision: batch.DocumentRevision, DocumentVersion: batch.DocumentVersion,
			TransactionID: batch.TransactionID, StateSeq: batch.StateSeq, Finding: finding,
			EvidenceKinds: []DiagnosticEvidenceKind{batch.Kind}, EvidenceIDs: []string{evID},
			Status: DiagnosticStatusCurrent, FirstSeen: batch.ObservedAt, LastSeen: batch.ObservedAt,
			Attribution: rankCulprit(batch.Candidates),
		})
	}
	return items
}

func (s *diagnosticStore) recordEvidenceLocked(batch DiagnosticBatch, confidence DiagnosticConfidence, reasons []string) string {
	dimension := batch.Dimension
	if dimension == "" {
		dimension = "edited_documents"
	}
	s.state.Sequence++
	evID := evidenceID(batch, s.state.Sequence)
	payload, _ := json.Marshal(batch)
	s.state.Evidence[evID] = DiagnosticEvidence{ID: evID, Kind: string(batch.Kind), RecordedAt: batch.ObservedAt, Payload: payload}
	dimensionState := "complete"
	if confidence == ConfidenceProvisional || confidence == ConfidenceUnavailable {
		dimensionState = "incomplete"
	}
	updated := DiagnosticDimension{State: dimensionState, Confidence: confidence, EvidenceIDs: []string{evID}, Reasons: reasons}
	if batch.Kind == EvidenceProjectCheck {
		s.state.Dimensions[dimension] = updated
		return evID
	}
	providerDimension := dimension + "\x00" + batch.ProviderID
	if prior, ok := s.state.ProviderDimensions[providerDimension]; ok {
		updated.EvidenceIDs = appendUnique(prior.EvidenceIDs, evID)
	}
	s.state.ProviderDimensions[providerDimension] = updated
	s.state.Dimensions[dimension] = s.aggregateDimensionLocked(dimension)
	return evID
}

// aggregateDimensionLocked combines every provider's evidence for a dimension, taking the
// weakest confidence and its state.
func (s *diagnosticStore) aggregateDimensionLocked(dimension string) DiagnosticDimension {
	aggregate := DiagnosticDimension{State: "complete", Confidence: ConfidenceAuthoritative}
	for key, providerEvidence := range s.state.ProviderDimensions {
		if !strings.HasPrefix(key, dimension+"\x00") {
			continue
		}
		aggregate.EvidenceIDs = appendUnique(aggregate.EvidenceIDs, providerEvidence.EvidenceIDs...)
		aggregate.Reasons = appendUnique(aggregate.Reasons, providerEvidence.Reasons...)
		if confidenceRank(providerEvidence.Confidence) < confidenceRank(aggregate.Confidence) {
			aggregate.Confidence = providerEvidence.Confidence
			aggregate.State = providerEvidence.State
		}
	}
	return aggregate
}

// recordFindingsLocked upserts every finding of the batch as a current item. It returns
// the current items, the ones that were announced as new, and the set of IDs reported.
func (s *diagnosticStore) recordFindingsLocked(batch DiagnosticBatch, evID string) (current, added []DiagnosticItem, seen map[string]bool) {
	seen = map[string]bool{}
	occurrences, placements := map[string]int{}, map[string]bool{}
	for _, finding := range batch.Findings {
		finding = normalizeFinding(finding)
		identity := findingIdentity(finding)
		// The same finding at the same place, twice in one batch, is one
		// finding published twice; at another place it is a second one.
		placement := fmt.Sprintf("%s\x00%v", identity, finding.Range)
		if placements[placement] {
			continue
		}
		placements[placement] = true
		id := "diag_" + diagnosticFingerprint(batch.ProviderID, batch.Document, finding, occurrences[identity])
		occurrences[identity]++
		if seen[id] {
			continue
		}
		seen[id] = true
		item, existed := s.state.Items[id]
		if !existed {
			item = DiagnosticItem{ID: id, ProviderID: batch.ProviderID, Producer: batch.Producer,
				ProducerVersion: batch.ProducerVersion, Document: batch.Document,
				Finding: finding, FirstSeen: batch.ObservedAt, Attribution: rankCulprit(batch.Candidates)}
		}
		// An item that was stale or resolved and is reported again is a new
		// current finding for this content; announce it again.
		announce := !existed || item.Status != DiagnosticStatusCurrent
		item.Status = DiagnosticStatusCurrent
		item.StaleReason, item.StaleRevision, item.ResolvedAt = "", "", time.Time{}
		// The provenance follows the latest observation so a re-published
		// finding is attributed to the content it was verified against, and
		// the range follows it too: the same warning is reported at whatever
		// line the edits since have left it on.
		item.Finding = finding
		item.DocumentRevision = batch.DocumentRevision
		item.DocumentVersion = batch.DocumentVersion
		item.TransactionID = batch.TransactionID
		item.StateSeq = batch.StateSeq
		item.LastSeen = batch.ObservedAt
		item.EvidenceIDs = appendUnique(item.EvidenceIDs, evID)
		item.EvidenceKinds = appendUniqueKind(item.EvidenceKinds, batch.Kind)
		s.state.Items[id] = item
		if announce {
			added = append(added, item)
			s.addNotice("new", item)
		}
		current = append(current, item)
	}
	return current, added, seen
}

// resolveAbsentLocked marks every listed item the batch did not report as resolved.
func (s *diagnosticStore) resolveAbsentLocked(ids []string, seen map[string]bool, observedAt time.Time) []DiagnosticItem {
	var resolved []DiagnosticItem
	for _, id := range ids {
		if seen[id] {
			continue
		}
		item, ok := s.state.Items[id]
		if !ok {
			continue
		}
		item.Status = DiagnosticStatusResolved
		item.ResolvedAt = observedAt
		s.state.Items[id] = item
		resolved = append(resolved, item)
		s.addNotice("resolved", item)
	}
	return resolved
}

// documentChanged marks every current finding recorded for document as stale
// unless it was already recorded against revision. It reports whether the
// ledger changed. Stale findings leave the active set, are announced with a
// "stale" notice and stay visible as stale until the producer re-publishes
// for the new content (current again) or a complete observation omits them
// (resolved). They are never silently promoted to resolved.
func (s *diagnosticStore) documentChanged(documents []string, revision string, observedAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := map[string]bool{}
	for _, document := range documents {
		names[filepath.ToSlash(filepath.Clean(document))] = true
	}
	changed := false
	keys := make([]string, 0, len(s.state.Active))
	for key := range s.state.Active {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, document, found := strings.Cut(key, "\x00")
		if !found || !names[document] {
			continue
		}
		var remaining []string
		for _, id := range s.state.Active[key] {
			item, ok := s.state.Items[id]
			if !ok {
				continue
			}
			if revision != "" && item.DocumentRevision == revision {
				remaining = append(remaining, id)
				continue
			}
			item.Status = DiagnosticStatusStale
			item.StaleReason = staleReasonDocumentChanged
			item.StaleRevision = revision
			s.state.Items[id] = item
			s.state.Stale[key] = appendUnique(s.state.Stale[key], id)
			s.addNotice("stale", item)
			changed = true
		}
		if len(remaining) == 0 {
			delete(s.state.Active, key)
		} else {
			s.state.Active[key] = remaining
		}
	}
	if !changed {
		return false, nil
	}
	s.pruneLocked(observedAt)
	return true, s.save()
}

func (s *diagnosticStore) addNotice(kind string, item DiagnosticItem) {
	s.state.Sequence++
	s.state.Notices = append(s.state.Notices, DiagnosticNotice{
		Cursor: fmt.Sprintf("diagcur_%d", s.state.Sequence), Kind: kind, ID: item.ID,
		Severity: item.Finding.Severity, Document: item.Document, Attribution: item.Attribution,
	})
}

// pruneLocked applies the retention bounds. Evidence referenced by a current
// or stale item, or by a coverage dimension, is never dropped. The caller
// holds s.mu.
func (s *diagnosticStore) pruneLocked(now time.Time) {
	noticed := s.pruneNoticesLocked()
	s.pruneItemsLocked(now, noticed)
	s.pruneEvidenceLocked(now)
}

// pruneNoticesLocked drops acknowledged notices and the oldest beyond the cap, and
// returns the IDs the retained notices still name.
func (s *diagnosticStore) pruneNoticesLocked() map[string]bool {
	// Acknowledged notices have been consumed through diagnostics(since).
	// NoticeFloor remembers the highest dropped notice sequence so a cursor
	// older than it is told that its delta is incomplete.
	var retained []DiagnosticNotice
	dropUpTo := func(notice DiagnosticNotice) {
		seq, _ := parseDiagnosticCursor(notice.Cursor)
		if seq > s.state.NoticeFloor {
			s.state.NoticeFloor = seq
		}
	}
	for _, notice := range s.state.Notices {
		seq, _ := parseDiagnosticCursor(notice.Cursor)
		if seq > s.state.Ack {
			retained = append(retained, notice)
		} else {
			dropUpTo(notice)
		}
	}
	if excess := len(retained) - maxDiagnosticNotices; excess > 0 {
		dropUpTo(retained[excess-1])
		retained = retained[excess:]
	}
	s.state.Notices = retained
	noticed := map[string]bool{}
	for _, notice := range s.state.Notices {
		noticed[notice.ID] = true
	}
	return noticed
}

// pruneItemsLocked keeps every current or stale item and every item a retained notice
// names; other items stay for the retention window, then up to the cap, oldest first.
func (s *diagnosticStore) pruneItemsLocked(now time.Time, noticed map[string]bool) {
	live := map[string]bool{}
	for _, ids := range s.state.Active {
		for _, id := range ids {
			live[id] = true
		}
	}
	for _, ids := range s.state.Stale {
		for _, id := range ids {
			live[id] = true
		}
	}
	var inactive []string
	for id, item := range s.state.Items {
		if live[id] || noticed[id] {
			continue
		}
		if now.Sub(item.LastSeen) > diagnosticRetentionWindow {
			delete(s.state.Items, id)
			continue
		}
		inactive = append(inactive, id)
	}
	dropOldest(inactive, len(inactive)-maxInactiveDiagnosticItems,
		func(id string) time.Time { return s.state.Items[id].LastSeen },
		func(id string) { delete(s.state.Items, id) })
}

// pruneEvidenceLocked keeps evidence an item or a dimension references; unreferenced
// evidence stays for the retention window, then up to the cap, oldest first.
func (s *diagnosticStore) pruneEvidenceLocked(now time.Time) {
	referenced := map[string]bool{}
	for _, item := range s.state.Items {
		for _, id := range item.EvidenceIDs {
			referenced[id] = true
		}
	}
	for _, dimension := range s.state.Dimensions {
		for _, id := range dimension.EvidenceIDs {
			referenced[id] = true
		}
	}
	for _, dimension := range s.state.ProviderDimensions {
		for _, id := range dimension.EvidenceIDs {
			referenced[id] = true
		}
	}
	var unreferenced []string
	for id, evidence := range s.state.Evidence {
		if referenced[id] {
			continue
		}
		if now.Sub(evidence.RecordedAt) > diagnosticRetentionWindow {
			delete(s.state.Evidence, id)
			continue
		}
		unreferenced = append(unreferenced, id)
	}
	dropOldest(unreferenced, len(unreferenced)-maxUnreferencedDiagnosticEvidence,
		func(id string) time.Time { return s.state.Evidence[id].RecordedAt },
		func(id string) { delete(s.state.Evidence, id) })
}

// dropOldest drops the excess oldest IDs, breaking equal timestamps by ID.
func dropOldest(ids []string, excess int, at func(string) time.Time, drop func(string)) {
	if excess <= 0 {
		return
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := at(ids[i]), at(ids[j])
		if left.Equal(right) {
			return ids[i] < ids[j]
		}
		return left.Before(right)
	})
	for _, id := range ids[:excess] {
		drop(id)
	}
}

// findings looks up the named findings under the store's lock.
func (s *diagnosticStore) findings(ids []string) map[string]DiagnosticFinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := make(map[string]DiagnosticFinding, len(ids))
	for _, id := range ids {
		if item, ok := s.state.Items[id]; ok {
			found[id] = item.Finding
		}
	}
	return found
}

func (s *diagnosticStore) countsLocked() (current, stale int) {
	for _, ids := range s.state.Active {
		current += len(ids)
	}
	for _, ids := range s.state.Stale {
		stale += len(ids)
	}
	return current, stale
}

func (s *diagnosticStore) staleItemsLocked() []DiagnosticItem {
	var items []DiagnosticItem
	for _, ids := range s.state.Stale {
		for _, id := range ids {
			if item, ok := s.state.Items[id]; ok {
				items = append(items, item)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (s *diagnosticStore) reportLocked(added, resolved []DiagnosticItem, evID string, reasons []string) DiagnosticReport {
	current, stale := s.countsLocked()
	return DiagnosticReport{Confidence: weakestConfidence(s.state.Dimensions), Coverage: cloneDimensions(s.state.Dimensions),
		New: added, Resolved: resolved, Stale: s.staleItemsLocked(), PreexistingCount: current - len(added),
		CurrentCount: current, StaleCount: stale, ProvisionalReasons: reasons,
		EvidenceIDs: []string{evID}, Cursor: fmt.Sprintf("diagcur_%d", s.state.Sequence)}
}

func (s *diagnosticStore) query(since string) (DiagnosticReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, err := parseDiagnosticCursor(since)
	if err != nil {
		return DiagnosticReport{}, err
	}
	if since != "" && seq > s.state.Ack {
		s.state.Ack = seq
	}
	truncated := seq < s.state.NoticeFloor
	var notices []DiagnosticNotice
	var added, resolved, stale []DiagnosticItem
	for _, notice := range s.state.Notices {
		n, _ := parseDiagnosticCursor(notice.Cursor)
		if n <= seq {
			continue
		}
		notices = append(notices, notice)
		item := s.state.Items[notice.ID]
		switch notice.Kind {
		case "new":
			added = append(added, item)
		case "stale":
			stale = append(stale, item)
		default:
			resolved = append(resolved, item)
		}
	}
	s.pruneLocked(time.Now().UTC())
	if err := s.save(); err != nil {
		return DiagnosticReport{}, err
	}
	current, staleCount := s.countsLocked()
	preexisting := current - len(added)
	if preexisting < 0 {
		preexisting = 0
	}
	report := DiagnosticReport{Confidence: weakestConfidence(s.state.Dimensions), Coverage: cloneDimensions(s.state.Dimensions),
		New: added, Resolved: resolved, Stale: stale, PreexistingCount: preexisting, CurrentCount: current, StaleCount: staleCount,
		EvidenceIDs: evidenceIDsFromDimensions(s.state.Dimensions), Cursor: fmt.Sprintf("diagcur_%d", s.state.Sequence), Notices: notices,
		NoticesTruncated: truncated}
	if len(s.state.Dimensions) == 0 {
		report.Confidence = ConfidenceUnavailable
		report.Coverage = map[string]DiagnosticDimension{"edited_documents": {State: "unavailable", Confidence: ConfidenceUnavailable, Reasons: []string{"no_diagnostic_evidence"}}}
		report.ProvisionalReasons = []string{"no_diagnostic_evidence"}
	}
	return report, nil
}

// pendingNotices returns unacknowledged notices that this process has not
// handed out before. Each notice is therefore attached to exactly one reply;
// a client that needs the full unacknowledged backlog uses query or
// noticesSince with its own cursor.
func (s *diagnosticStore) pendingNotices(limit int) []DiagnosticNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	floor := s.state.Ack
	if s.delivered > floor {
		floor = s.delivered
	}
	var out []DiagnosticNotice
	for _, notice := range s.state.Notices {
		seq, _ := parseDiagnosticCursor(notice.Cursor)
		if seq <= floor {
			continue
		}
		out = append(out, notice)
		s.delivered = seq
		if len(out) == limit {
			break
		}
	}
	return out
}

// noticeHead is the cursor of the newest notice recorded, which is where a
// client that has just connected starts.
func (s *diagnosticStore) noticeHead() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := s.state.NoticeFloor
	if len(s.state.Notices) > 0 {
		head, _ = parseDiagnosticCursor(s.state.Notices[len(s.state.Notices)-1].Cursor)
	}
	return head
}

// noticesSince returns up to limit notices strictly after cursor without
// acknowledging anything. The returned cursor names the last notice in the
// page (or the caller's cursor when the page is empty) so the caller can
// continue from it; Truncated reports that notices between cursor and the
// retained floor were pruned.
func (s *diagnosticStore) noticesSince(cursor string, limit int) (DiagnosticNoticePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, err := parseDiagnosticCursor(cursor)
	if err != nil {
		return DiagnosticNoticePage{}, err
	}
	if limit <= 0 {
		limit = 20
	}
	page := DiagnosticNoticePage{Notices: []DiagnosticNotice{}, Cursor: fmt.Sprintf("diagcur_%d", seq),
		Truncated: seq < s.state.NoticeFloor}
	for _, notice := range s.state.Notices {
		n, _ := parseDiagnosticCursor(notice.Cursor)
		if n <= seq {
			continue
		}
		if len(page.Notices) == limit {
			page.More = true
			break
		}
		page.Notices = append(page.Notices, notice)
		page.Cursor = notice.Cursor
	}
	return page, nil
}

func withoutIDs(ids []string, drop map[string]bool) []string {
	var kept []string
	for _, id := range ids {
		if !drop[id] {
			kept = append(kept, id)
		}
	}
	return kept
}

func (s *diagnosticStore) evidenceByID(id string) (DiagnosticEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.state.Evidence[id]
	if !ok {
		return DiagnosticEvidence{}, fmt.Errorf("evidence %q not found", id)
	}
	return value, nil
}

func parseDiagnosticCursor(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	var seq uint64
	if _, err := fmt.Sscanf(cursor, "diagcur_%d", &seq); err != nil {
		return 0, fmt.Errorf("invalid diagnostic cursor %q", cursor)
	}
	return seq, nil
}
func idsOf(items []DiagnosticItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	sort.Strings(out)
	return out
}
func appendUnique(values []string, additions ...string) []string {
	seen := map[string]bool{}
	for _, v := range values {
		seen[v] = true
	}
	for _, v := range additions {
		if !seen[v] {
			values = append(values, v)
			seen[v] = true
		}
	}
	sort.Strings(values)
	return values
}
func appendUniqueKind(values []DiagnosticEvidenceKind, value DiagnosticEvidenceKind) []DiagnosticEvidenceKind {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
func cloneDimensions(source map[string]DiagnosticDimension) map[string]DiagnosticDimension {
	out := make(map[string]DiagnosticDimension, len(source))
	for k, v := range source {
		v.EvidenceIDs = append([]string(nil), v.EvidenceIDs...)
		v.Reasons = append([]string(nil), v.Reasons...)
		out[k] = v
	}
	return out
}
func evidenceIDsFromDimensions(dimensions map[string]DiagnosticDimension) []string {
	var ids []string
	for _, dimension := range dimensions {
		ids = appendUnique(ids, dimension.EvidenceIDs...)
	}
	return ids
}

func confidenceRank(value DiagnosticConfidence) int {
	return map[DiagnosticConfidence]int{ConfidenceAuthoritative: 3, ConfidenceCorroborated: 2, ConfidenceProvisional: 1, ConfidenceUnavailable: 0}[value]
}

func weakestConfidence(dimensions map[string]DiagnosticDimension) DiagnosticConfidence {
	if len(dimensions) == 0 {
		return ConfidenceUnavailable
	}
	rank := map[DiagnosticConfidence]int{ConfidenceAuthoritative: 3, ConfidenceCorroborated: 2, ConfidenceProvisional: 1, ConfidenceUnavailable: 0}
	result := ConfidenceAuthoritative
	for _, d := range dimensions {
		if rank[d.Confidence] < rank[result] {
			result = d.Confidence
		}
	}
	return result
}

func (w *Workspace) RecordDiagnosticEvidence(batch DiagnosticBatch) (DiagnosticReport, error) {
	w.mu.Lock()
	identity := w.identity
	w.mu.Unlock()
	if batch.StateSeq == 0 {
		batch.StateSeq = identity.StateSeq
	}
	return w.diagnostics.record(batch)
}
func (w *Workspace) Diagnostics(since string) (DiagnosticReport, error) {
	return w.diagnostics.query(since)
}

// DiagnosticFindings returns the recorded finding of each id that is still
// known. A notice delta names ids, and an id alone is not something an agent
// can act on: with the line and the message beside it the reply says what
// broke, instead of costing a diagnostics call to find out.
func (w *Workspace) DiagnosticFindings(ids []string) map[string]DiagnosticFinding {
	if w.diagnostics == nil || len(ids) == 0 {
		return nil
	}
	return w.diagnostics.findings(ids)
}

func (w *Workspace) DiagnosticNotices(limit int) []DiagnosticNotice {
	return w.diagnostics.pendingNotices(limit)
}

// DiagnosticNoticeHead is the newest notice sequence recorded. A bridge
// registers a client it has not seen before at the head, so its first reply
// is not the backlog of everything that happened before it connected.
func (w *Workspace) DiagnosticNoticeHead() uint64 {
	if w.diagnostics == nil {
		return 0
	}
	return w.diagnostics.noticeHead()
}

// DiagnosticNoticesSince returns a bounded page of notices strictly after
// cursor without acknowledging them. A bridge that tracks one cursor per
// client should call this instead of DiagnosticNotices so every reply
// carries only the delta that client has not seen.
func (w *Workspace) DiagnosticNoticesSince(cursor string, limit int) (DiagnosticNoticePage, error) {
	return w.diagnostics.noticesSince(cursor, limit)
}

// noteDocumentContentChanged tells the ledger that the content of absolute
// changed and now carries revision, so findings recorded for the older
// content become stale. Documents are matched by absolute path and by the
// workspace-relative display path, which are the forms producers record.
func (w *Workspace) noteDocumentContentChanged(absolute string, revision RevisionID) error {
	if w.diagnostics == nil {
		return nil
	}
	documents := []string{absolute, displayPath(w.identity.Root, absolute)}
	_, err := w.diagnostics.documentChanged(documents, string(revision), time.Now().UTC())
	return err
}
func (w *Workspace) Evidence(id string) (DiagnosticEvidence, error) {
	return w.diagnostics.evidenceByID(id)
}
