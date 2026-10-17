package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Version 3 retains independent stream coverage. Older binaries must reject
// this ledger rather than collapse its lanes back into document-wide proof.
const diagnosticStateVersion = 3

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
	Kind                    DiagnosticEvidenceKind `json:"kind"`
	ProviderID              string                 `json:"provider_id"`
	Producer                string                 `json:"producer"`
	ProducerVersion         string                 `json:"producer_version,omitempty"`
	Document                string                 `json:"document,omitempty"`
	DocumentRevision        string                 `json:"document_revision,omitempty"`
	PreEditDocumentRevision string                 `json:"pre_edit_document_revision,omitempty"`
	DocumentVersion         *int64                 `json:"document_version,omitempty"`
	ExpectedVersion         *int64                 `json:"expected_version,omitempty"`
	ResultID                string                 `json:"result_id,omitempty"`
	TransactionID           string                 `json:"transaction_id,omitempty"`
	StateSeq                uint64                 `json:"state_seq"`
	Complete                bool                   `json:"complete"`
	TimedOut                bool                   `json:"timed_out,omitempty"`
	Reason                  string                 `json:"reason,omitempty"`
	ProgressPending         bool                   `json:"progress_pending,omitempty"`
	ChangeBarrier           bool                   `json:"change_barrier,omitempty"`
	Selected                bool                   `json:"selected"`
	// Baseline seeds or refreshes proven-current pre-edit findings without
	// announcing them as introduced by a mutation or resolving omitted findings.
	Baseline bool `json:"baseline,omitempty"`
	// nativeContentChanged is an ingestion hint from the native preimage read,
	// not wire evidence or persisted publication proof.
	nativeContentChanged bool
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
	BaselineMissing  bool                     `json:"baseline_missing,omitempty"`
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
	// Each owner retains a bounded recent proof history. Older exact revisions
	// may become unavailable; lookup must then report a missing baseline.
	maxDiagnosticEvidenceReferences = 8
)

type DiagnosticDimension struct {
	State       string               `json:"state"`
	Confidence  DiagnosticConfidence `json:"confidence"`
	EvidenceIDs []string             `json:"evidence_ids,omitempty"`
	Reasons     []string             `json:"reasons,omitempty"`
}

// DiagnosticNotice records new, stale, persisting and resolved transitions.
// Persisting covers stale refreshes and first-observed preexisting baseline
// findings. A baseline recurrence after verified resolution is new, but carries
// no mutation attribution and is excluded from the mutation report's New set.
type DiagnosticNotice struct {
	BaselineMissing bool               `json:"baseline_missing,omitempty"`
	Cursor          string             `json:"cursor"`
	Kind            string             `json:"kind"`
	ID              string             `json:"id"`
	Severity        int                `json:"severity,omitempty"`
	Document        string             `json:"document,omitempty"`
	Attribution     CulpritAttribution `json:"attribution,omitempty"`
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
	// writeFile is normally atomicWriteFile; tests can count or fail persistence.
	writeFile func(string, []byte, os.FileMode) error
	// baselineIndex is a rebuildable storage index, not persisted lifecycle state.
	baselineIndex map[string]string
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
	if store.state.Version != 1 && store.state.Version != 2 && store.state.Version != diagnosticStateVersion {
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
	store.migrateDocumentDimensions()
	if store.state.Version != diagnosticStateVersion {
		store.state.Version = diagnosticStateVersion
		// Persist the version before returning: a downgrade must not consume
		// migrated dimensions or persisting notices under the old version.
		if err := store.save(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// Old coverage did not preserve independent streams. Bounded evidence cannot
// reconstruct every historical origin, so keep the unsplittable key as an
// explicit provisional dimension. Never rewrite historical resolved items.
func (s *diagnosticStore) migrateDocumentDimensions() {
	dimensions := map[string]bool{}
	for key, dimension := range s.state.ProviderDimensions {
		name, _, _ := strings.Cut(key, "\x00")
		dimensions[name] = true
		if strings.Count(key, "\x00") < 3 {
			dimension.State = "incomplete"
			if confidenceRank(dimension.Confidence) > confidenceRank(ConfidenceProvisional) {
				dimension.Confidence = ConfidenceProvisional
			}
			dimension.Reasons = appendUnique(dimension.Reasons, "legacy_stream_coverage_unknown")
			s.state.ProviderDimensions[key] = dimension
		}
	}
	for name := range dimensions {
		s.state.Dimensions[name] = s.aggregateDimensionLocked(name)
	}
	if s.state.Version != diagnosticStateVersion {
		for name, dimension := range s.state.Dimensions {
			if !dimensions[name] {
				dimension.State = "incomplete"
				if confidenceRank(dimension.Confidence) > confidenceRank(ConfidenceProvisional) {
					dimension.Confidence = ConfidenceProvisional
				}
				dimension.Reasons = appendUnique(dimension.Reasons, "legacy_stream_coverage_unknown")
				s.state.Dimensions[name] = dimension
			}
		}
	}
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
	write := s.writeFile
	if write == nil {
		write = atomicWriteFile
	}
	return write(s.path, append(content, '\n'), 0o600)
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

// currentPublication requires the producer's document version, never a syntax
// request acknowledgement or the version of the editor's diagnostic cache.
func currentPublication(batch DiagnosticBatch) bool {
	return batch.DocumentVersion != nil && batch.ExpectedVersion != nil &&
		*batch.DocumentVersion >= *batch.ExpectedVersion
}

func confidenceFor(batch DiagnosticBatch) (confidence DiagnosticConfidence, reasons []string) {
	defer func() {
		if confidence == ConfidenceProvisional && (batch.Producer == "rust_analyzer" || batch.Producer == "rust-analyzer") {
			reasons = append([]string{"rust_diagnostics_pending"}, reasons...)
		}
	}()
	// Environment-noise collapsing weakens semantic coverage, never the
	// publication's version/progress guards. Preserve both reasons.
	if batch.Reason == "python_environment_unresolved" {
		defer func() {
			if confidence == ConfidenceAuthoritative || confidence == ConfidenceCorroborated {
				confidence = ConfidenceProvisional
			}
			reasons = append(reasons, "python_environment_unresolved")
		}()
	}
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
		if batch.Complete && currentPublication(batch) {
			return ConfidenceAuthoritative, nil
		}
		return ConfidenceProvisional, []string{"push_missing_current_document_proof"}
	case EvidencePull:
		if batch.Complete && currentPublication(batch) {
			return ConfidenceAuthoritative, nil
		}
		return ConfidenceProvisional, []string{"pull_incomplete_or_missing_result_id"}
	case EvidenceWorkspace:
		if batch.Complete && batch.ResultID != "" && currentPublication(batch) {
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
	reports, err := s.recordBatches([]DiagnosticBatch{batch})
	if err != nil {
		return DiagnosticReport{}, err
	}
	return reports[0], nil
}

// recordBatches publishes all transitions together, only after one successful
// persistence. Validation precedes mutation; every batch still runs lifecycle
// processing even when its immutable evidence payload is already stored.
func (s *diagnosticStore) recordBatches(batches []DiagnosticBatch, retained ...string) ([]DiagnosticReport, error) {
	if len(batches) == 0 {
		return nil, nil
	}
	batches = append([]DiagnosticBatch(nil), batches...)
	now := time.Now().UTC()
	for i := range batches {
		batch := &batches[i]
		if batch.ProviderID == "" || batch.Producer == "" {
			return nil, errors.New("diagnostic evidence requires provider_id and producer")
		}
		if !batch.Selected {
			return nil, errors.New("diagnostic evidence from an unselected provider is rejected")
		}
		if batch.ObservedAt.IsZero() {
			batch.ObservedAt = now
		}
		batch.Document = filepath.ToSlash(filepath.Clean(batch.Document))
		if batch.Baseline {
			batch.TransactionID, batch.Candidates = "", nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state
	s.state = cloneDiagnosticState(previous)
	committed := false
	defer func() {
		if !committed {
			s.state = previous
			s.baselineIndex = nil
		}
	}()
	var reports []DiagnosticReport
	keep := append([]string(nil), retained...)
	for _, batch := range batches {
		report, err := s.recordLocked(batch)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
		keep = append(keep, report.EvidenceIDs...)
	}
	s.pruneLocked(now, keep...)
	if err := s.save(); err != nil {
		return nil, err
	}
	committed = true
	return reports, nil
}

// cloneDiagnosticState detaches every slice/map mutated by ingestion or pruning.
// Payloads and attribution slices are immutable after construction.
func cloneDiagnosticState(source diagnosticState) diagnosticState {
	cloned := source
	cloned.Items = make(map[string]DiagnosticItem, len(source.Items))
	for id, item := range source.Items {
		item.EvidenceIDs = append([]string(nil), item.EvidenceIDs...)
		item.EvidenceKinds = append([]DiagnosticEvidenceKind(nil), item.EvidenceKinds...)
		cloned.Items[id] = item
	}
	cloneIDs := func(source map[string][]string) map[string][]string {
		result := make(map[string][]string, len(source))
		for key, ids := range source {
			result[key] = append([]string(nil), ids...)
		}
		return result
	}
	cloned.Active, cloned.Stale = cloneIDs(source.Active), cloneIDs(source.Stale)
	cloned.Evidence = make(map[string]DiagnosticEvidence, len(source.Evidence))
	for id, evidence := range source.Evidence {
		cloned.Evidence[id] = evidence
	}
	cloned.Notices = append([]DiagnosticNotice(nil), source.Notices...)
	cloned.Dimensions = cloneDimensions(source.Dimensions)
	cloned.ProviderDimensions = cloneDimensions(source.ProviderDimensions)
	return cloned
}

func (s *diagnosticStore) recordLocked(batch DiagnosticBatch) (DiagnosticReport, error) {
	if batch.Baseline && !batch.Staged && batch.nativeContentChanged {
		s.documentChangedLocked([]string{batch.Document}, batch.DocumentRevision)
	}
	confidence, reasons := confidenceFor(batch)
	if batch.Staged {
		return s.recordStagedLocked(batch, confidence, reasons)
	}
	key := batch.ProviderID + "\x00" + batch.Document
	dimension := batch.Dimension
	if dimension == "" {
		dimension = "edited_documents"
	}
	prior := s.state.ProviderDimensions[dimension+"\x00"+key+"\x00"+string(batch.Kind)]
	baselineKnown := s.baselineKnownLocked(batch, prior.EvidenceIDs)
	evID := s.recordEvidenceLocked(batch, confidence, reasons)
	previous := append([]string(nil), s.state.Active[key]...)
	previousStale := append([]string(nil), s.state.Stale[key]...)
	var current, added []DiagnosticItem
	seen := map[string]bool{}
	// An explicitly old publication cannot refresh stale findings or resurrect
	// resolved ones. Keep its evidence, but leave the ledger's items alone.
	if batch.ExpectedVersion == nil || batch.DocumentVersion == nil || currentPublication(batch) {
		current, added, seen = s.recordFindingsLocked(batch, evID, baselineKnown)
	}
	var resolved []DiagnosticItem
	// A baseline seeds the cache; even a current snapshot may omit findings
	// while the producer is between partial publications.
	if !batch.Baseline && batch.Complete && confidenceRank(confidence) >= confidenceRank(ConfidenceCorroborated) {
		// Omission proves withdrawal only within this stream. Rejected
		// withdrawals retain their original current/stale membership.
		resolved = s.resolveAbsentLocked(append(previous, previousStale...), seen, batch.Kind, batch.ObservedAt)
	}
	removed := map[string]bool{}
	for _, item := range resolved {
		removed[item.ID] = true
	}
	s.state.Active[key] = appendUnique(withoutIDs(previous, removed), idsOf(current)...)
	for id := range seen {
		removed[id] = true
	}
	s.state.Stale[key] = withoutIDs(previousStale, removed)
	if batch.Kind != EvidenceProjectCheck {
		s.state.Dimensions[dimension] = s.aggregateDimensionLocked(dimension)
	}
	report := s.reportLocked(added, resolved, evID, reasons)
	// Observation confidence cannot hide an independent incomplete lane.
	if confidenceRank(confidence) < confidenceRank(report.Confidence) {
		report.Confidence = confidence
	}
	report.Current = append([]DiagnosticItem(nil), current...)
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

// recentEvidenceIDs moves observed IDs to the end and bounds each owner's
// history. Missing older exact proof is deliberately not inferred from this set.
func recentEvidenceIDs(existing []string, ids ...string) []string {
	if len(existing) > maxDiagnosticEvidenceReferences {
		existing = existing[len(existing)-maxDiagnosticEvidenceReferences:]
	}
	result := append([]string(nil), existing...)
	for _, id := range ids {
		kept := result[:0]
		for _, previous := range result {
			if previous != id {
				kept = append(kept, previous)
			}
		}
		result = append(kept, id)
		if len(result) > maxDiagnosticEvidenceReferences {
			result = result[len(result)-maxDiagnosticEvidenceReferences:]
		}
	}
	return result
}

func (s *diagnosticStore) recordEvidenceLocked(batch DiagnosticBatch, confidence DiagnosticConfidence, reasons []string) string {
	dimension := batch.Dimension
	if dimension == "" {
		dimension = "edited_documents"
	}
	proofKey := baselineProofKey(batch, confidence)
	if proofKey != "" && s.baselineIndex == nil {
		s.baselineIndex = map[string]string{}
		for id, evidence := range s.state.Evidence {
			var previous DiagnosticBatch
			if json.Unmarshal(evidence.Payload, &previous) == nil {
				proof, _ := confidenceFor(previous)
				if key := baselineProofKey(previous, proof); key != "" {
					s.baselineIndex[key] = id
				}
			}
		}
	}
	evID := s.baselineIndex[proofKey]
	if _, exists := s.state.Evidence[evID]; evID == "" || !exists {
		s.state.Sequence++
		evID = evidenceID(batch, s.state.Sequence)
		payload, _ := json.Marshal(batch)
		s.state.Evidence[evID] = DiagnosticEvidence{ID: evID, Kind: string(batch.Kind), RecordedAt: batch.ObservedAt, Payload: payload}
		if proofKey != "" {
			s.baselineIndex[proofKey] = evID
		}
	}
	dimensionState := "complete"
	if confidence == ConfidenceProvisional || confidence == ConfidenceUnavailable {
		dimensionState = "incomplete"
	}
	updated := DiagnosticDimension{State: dimensionState, Confidence: confidence, EvidenceIDs: []string{evID}, Reasons: reasons}
	if batch.Kind == EvidenceProjectCheck {
		s.state.Dimensions[dimension] = updated
		return evID
	}
	providerDimension := dimension + "\x00" + batch.ProviderID + "\x00" + batch.Document + "\x00" + string(batch.Kind)
	if prior, ok := s.state.ProviderDimensions[providerDimension]; ok {
		updated.EvidenceIDs = recentEvidenceIDs(prior.EvidenceIDs, evID)
	}
	s.state.ProviderDimensions[providerDimension] = updated
	if confidence == ConfidenceAuthoritative && batch.Kind != EvidenceUnavailable {
		// Attachment failures describe the absence of a producer at that time.
		// A current publication for this document supersedes that placeholder.
		placeholder := dimension + "\x00nvim_lsp\x00" + batch.Document
		for key := range s.state.ProviderDimensions {
			if key == placeholder || strings.HasPrefix(key, placeholder+"\x00") {
				delete(s.state.ProviderDimensions, key)
			}
		}
	}
	s.state.Dimensions[dimension] = s.aggregateDimensionLocked(dimension)
	return evID
}

// aggregateDimensionLocked combines every provider's evidence for a dimension, taking the
// weakest confidence and its state.
func (s *diagnosticStore) aggregateDimensionLocked(dimension string) DiagnosticDimension {
	aggregate := DiagnosticDimension{State: "complete", Confidence: ConfidenceAuthoritative}
	keys := make([]string, 0, len(s.state.ProviderDimensions))
	for key := range s.state.ProviderDimensions {
		keys = append(keys, key)
	}
	// Keep representative proof for the weakest confidence when the global
	// dimension has more providers/documents than its reference budget.
	sort.Slice(keys, func(i, j int) bool {
		left := confidenceRank(s.state.ProviderDimensions[keys[i]].Confidence)
		right := confidenceRank(s.state.ProviderDimensions[keys[j]].Confidence)
		if left != right {
			return left > right
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		providerEvidence := s.state.ProviderDimensions[key]
		if !strings.HasPrefix(key, dimension+"\x00") {
			continue
		}
		aggregate.EvidenceIDs = recentEvidenceIDs(aggregate.EvidenceIDs, providerEvidence.EvidenceIDs...)
		aggregate.Reasons = appendUnique(aggregate.Reasons, providerEvidence.Reasons...)
		if confidenceRank(providerEvidence.Confidence) < confidenceRank(aggregate.Confidence) {
			aggregate.Confidence = providerEvidence.Confidence
			aggregate.State = providerEvidence.State
		}
	}
	// Latest item provenance is shared across lanes. It cannot prove that
	// another historical origin refreshed, or repair unknown legacy origins.
	for _, memberships := range []map[string][]string{s.state.Active, s.state.Stale} {
		for key, ids := range memberships {
			prefix := dimension + "\x00" + key + "\x00"
			relevant := dimension == "edited_documents"
			if !relevant {
				for lane := range s.state.ProviderDimensions {
					if strings.HasPrefix(lane, prefix) {
						relevant = true
						break
					}
				}
			}
			if !relevant {
				continue
			}
			for _, id := range ids {
				item := s.state.Items[id]
				unknown := len(item.EvidenceKinds) == 0
				for _, kind := range item.EvidenceKinds {
					if _, ok := s.state.ProviderDimensions[prefix+string(kind)]; !knownDiagnosticKind(kind) || !ok {
						unknown = true
					}
				}
				reason := ""
				if unknown {
					reason = "diagnostic_origin_coverage_unknown"
				} else if item.Status == DiagnosticStatusStale {
					reason = staleReasonDocumentChanged
				}
				if reason != "" {
					if confidenceRank(aggregate.Confidence) > confidenceRank(ConfidenceProvisional) {
						aggregate.State, aggregate.Confidence = "incomplete", ConfidenceProvisional
					}
					aggregate.Reasons = appendUnique(aggregate.Reasons, reason)
				}
			}
		}
	}
	return aggregate
}

func knownDiagnosticKind(kind DiagnosticEvidenceKind) bool {
	switch kind {
	case EvidencePush, EvidencePull, EvidenceWorkspace, EvidenceProjectCheck:
		return true
	default:
		return false
	}
}

// Historical kinds deliberately over-approximate outstanding origins, even
// after recurrence. Empty/unknown sets never establish withdrawal coverage.
func streamCoversItem(item DiagnosticItem, kind DiagnosticEvidenceKind) bool {
	if len(item.EvidenceKinds) == 0 || !knownDiagnosticKind(kind) {
		return false
	}
	for _, origin := range item.EvidenceKinds {
		if !knownDiagnosticKind(origin) || origin != kind {
			return false
		}
	}
	return true
}

// recordFindingsLocked upserts every finding of the batch as a current item. It returns
// the current items, the ones that were announced as new, and the set of IDs reported.
func (s *diagnosticStore) recordFindingsLocked(batch DiagnosticBatch, evID string, baselineKnown bool) (current, added []DiagnosticItem, seen map[string]bool) {
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
		// Stale means the document changed, not that the finding went away.
		// Only a verified resolution followed by a return is a regression.
		announce := !existed || item.Status == DiagnosticStatusResolved
		if announce {
			item.BaselineMissing = !baselineKnown
		}
		persisting := existed && item.Status == DiagnosticStatusStale
		if batch.Baseline {
			// A baseline is observed before this mutation. Its first sightings
			// are preexisting; a verified recurrence must still reach cursors.
			announce = existed && item.Status == DiagnosticStatusResolved
			persisting = persisting || !existed
			item.BaselineMissing = false
			item.Attribution = rankCulprit(nil)
		}
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
		item.EvidenceIDs = recentEvidenceIDs(item.EvidenceIDs, evID)
		if existed && len(item.EvidenceKinds) == 0 {
			// Refresh is positive evidence, not proof of missing legacy history.
			item.EvidenceKinds = []DiagnosticEvidenceKind{EvidenceUnavailable}
		}
		item.EvidenceKinds = appendUniqueKind(item.EvidenceKinds, batch.Kind)
		s.state.Items[id] = item
		if announce {
			if !batch.Baseline {
				added = append(added, item)
			}
			s.addNotice("new", item)
		} else if persisting {
			// Preserve the freshness transition for cursor consumers without
			// telling them the edit introduced an unchanged finding.
			s.addNotice("persisting", item)
		}
		current = append(current, item)
	}
	return current, added, seen
}

// resolveAbsentLocked resolves omitted items only when this stream covers all
// their known historical positive origins.
func (s *diagnosticStore) resolveAbsentLocked(ids []string, seen map[string]bool, kind DiagnosticEvidenceKind, observedAt time.Time) []DiagnosticItem {
	var resolved []DiagnosticItem
	for _, id := range ids {
		if seen[id] {
			continue
		}
		item, ok := s.state.Items[id]
		if !ok || !streamCoversItem(item, kind) {
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

// documentChanged marks old current findings stale and persists the transition.
func (s *diagnosticStore) documentChanged(documents []string, revision string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.documentChangedLocked(documents, revision) {
		return false, nil
	}
	// Snapshot can reach this path while EditPlan holds plansMu. Persist
	// staleness now; defer pruning to record/query, where retained plan proof
	// can be collected without re-entering that lock.
	return true, s.save()
}

// documentChangedLocked also serves bulk baseline ingestion: external changes
// must stale omitted old findings without publishing a save per loaded buffer.
func (s *diagnosticStore) documentChangedLocked(documents []string, revision string) bool {
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
	// Invalidate every lane, including one whose finding ID was most recently
	// refreshed by another stream. An empty old lane also needs new proof.
	dimensions := map[string]bool{}
	for key, dimension := range s.state.ProviderDimensions {
		parts := strings.Split(key, "\x00")
		if len(parts) < 3 || !names[parts[2]] {
			continue
		}
		current := false
		if len(dimension.EvidenceIDs) > 0 {
			var batch DiagnosticBatch
			evidence := s.state.Evidence[dimension.EvidenceIDs[len(dimension.EvidenceIDs)-1]]
			current = json.Unmarshal(evidence.Payload, &batch) == nil && revision != "" && batch.DocumentRevision == revision
		}
		if !current {
			dimension.State = "incomplete"
			if confidenceRank(dimension.Confidence) > confidenceRank(ConfidenceProvisional) {
				dimension.Confidence = ConfidenceProvisional
			}
			dimension.Reasons = appendUnique(dimension.Reasons, staleReasonDocumentChanged)
			s.state.ProviderDimensions[key] = dimension
			dimensions[parts[0]], changed = true, true
		}
	}
	for name := range dimensions {
		s.state.Dimensions[name] = s.aggregateDimensionLocked(name)
	}
	return changed
}

func (s *diagnosticStore) addNotice(kind string, item DiagnosticItem) {
	s.state.Sequence++
	s.state.Notices = append(s.state.Notices, DiagnosticNotice{
		Cursor: fmt.Sprintf("diagcur_%d", s.state.Sequence), Kind: kind, ID: item.ID, BaselineMissing: item.BaselineMissing,
		Severity: item.Finding.Severity, Document: item.Document, Attribution: item.Attribution,
	})
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
	reasons = appendUnique(reasons, diagnosticCoverageReasons(s.state.Dimensions)...)
	return DiagnosticReport{Confidence: weakestConfidence(s.state.Dimensions), Coverage: cloneDimensions(s.state.Dimensions),
		New: added, Resolved: resolved, Stale: s.staleItemsLocked(), PreexistingCount: current - len(added),
		CurrentCount: current, StaleCount: stale, ProvisionalReasons: reasons,
		EvidenceIDs: []string{evID}, Cursor: fmt.Sprintf("diagcur_%d", s.state.Sequence)}
}

func diagnosticCoverageReasons(dimensions map[string]DiagnosticDimension) []string {
	var reasons []string
	names := make([]string, 0, len(dimensions))
	for name := range dimensions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		reasons = appendUnique(reasons, dimensions[name].Reasons...)
	}
	return reasons
}

func (s *diagnosticStore) query(since string, retained ...string) (DiagnosticReport, error) {
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
		case "resolved":
			resolved = append(resolved, item)
		case "persisting":
			// Restores freshness; it is neither new nor resolved.
		}
	}
	s.pruneLocked(time.Now().UTC(), retained...)
	if err := s.save(); err != nil {
		return DiagnosticReport{}, err
	}
	current, staleCount := s.countsLocked()
	// Full diagnostics queries retain access to current finding bodies even
	// after their original new notices have been acknowledged and pruned.
	var currentItems []DiagnosticItem
	for _, ids := range s.state.Active {
		for _, id := range ids {
			if item, ok := s.state.Items[id]; ok {
				currentItems = append(currentItems, item)
			}
		}
	}
	sort.Slice(currentItems, func(i, j int) bool { return currentItems[i].ID < currentItems[j].ID })
	preexisting := current - len(added)
	if preexisting < 0 {
		preexisting = 0
	}
	report := DiagnosticReport{Confidence: weakestConfidence(s.state.Dimensions), Coverage: cloneDimensions(s.state.Dimensions),
		New: added, Current: currentItems, Resolved: resolved, Stale: stale, PreexistingCount: preexisting, CurrentCount: current, StaleCount: staleCount,
		EvidenceIDs: evidenceIDsFromDimensions(s.state.Dimensions), Cursor: fmt.Sprintf("diagcur_%d", s.state.Sequence), Notices: notices,
		NoticesTruncated: truncated}
	report.ProvisionalReasons = diagnosticCoverageReasons(s.state.Dimensions)
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

// DiagnosticPreEditRevision binds a baseline to the exact before image, using
// the same content and object identity as native document snapshots.
func (w *Workspace) DiagnosticPreEditRevision(file PlanStageFile) (string, error) {
	absolute, err := w.confinedPath(file.Path)
	if err != nil {
		return "", err
	}
	snapshot := DocumentSnapshot{Workspace: w.Identity(), URI: (&url.URL{Scheme: "file", Path: absolute}).String(), Disk: file.BeforeDisk}
	if file.BeforeExists {
		snapshot.ContentSHA256 = hashBytes(file.Before)
	}
	return string(revisionFor(snapshot)), nil
}

// diagnosticBaselineRevision reads the native preimage without publishing a
// separate workspace/ledger observation for every loaded buffer. The RPC and
// this disk read remain separate observations, not an atomic source proof.
func (w *Workspace) diagnosticBaselineRevision(path string) (string, bool, error) {
	absolute, err := w.confinedPath(path)
	if err != nil {
		return "", false, err
	}
	disk, content, err := inspectPath(absolute)
	if err != nil {
		return "", false, err
	}
	if disk.Kind != ObjectRegularText {
		return "", false, fmt.Errorf("baseline document %s is not native regular text", absolute)
	}
	contentHash := hashBytes(content)
	w.mu.Lock()
	previous, known := w.documents[absolute]
	identity := w.identity
	w.mu.Unlock()
	snapshot := DocumentSnapshot{Workspace: identity, URI: (&url.URL{Scheme: "file", Path: absolute}).String(), Disk: disk, ContentSHA256: contentHash}
	return string(revisionFor(snapshot)), known && previous.contentHash != contentHash, nil
}

// RecordDiagnosticBaselines filters missing native proof before bulk ingestion.
// Content-change invalidations are folded into that same ledger transaction.
func (w *Workspace) RecordDiagnosticBaselines(batches []DiagnosticBatch) ([]DiagnosticReport, error) {
	var accepted []DiagnosticBatch
	for _, batch := range batches {
		if proof, _ := confidenceFor(batch); batch.Staged || proof != ConfidenceAuthoritative {
			continue
		}
		batch.Baseline = true
		revision, changed, err := w.diagnosticBaselineRevision(batch.Document)
		if err != nil {
			continue
		}
		batch.DocumentRevision, batch.nativeContentChanged = revision, changed
		accepted = append(accepted, batch)
	}
	return w.RecordDiagnosticEvidenceBatches(accepted)
}

func (w *Workspace) RecordDiagnosticEvidence(batch DiagnosticBatch) (DiagnosticReport, error) {
	reports, err := w.RecordDiagnosticEvidenceBatches([]DiagnosticBatch{batch})
	if err != nil {
		return DiagnosticReport{}, err
	}
	return reports[0], nil
}

// RecordDiagnosticEvidenceBatches ingests one provider RPC's accepted batches
// atomically, with one ledger persistence regardless of loaded document count.
func (w *Workspace) RecordDiagnosticEvidenceBatches(batches []DiagnosticBatch) ([]DiagnosticReport, error) {
	w.mu.Lock()
	stateSeq := w.identity.StateSeq
	w.mu.Unlock()
	batches = append([]DiagnosticBatch(nil), batches...)
	for i := range batches {
		if batches[i].StateSeq == 0 {
			batches[i].StateSeq = stateSeq
		}
	}
	return w.diagnostics.recordBatches(batches, w.retainedDiagnosticEvidence()...)
}

// Preparations and retained plan invariants name immutable proof IDs. Keep the
// exact payloads while those owners survive; never replace an expired ID with
// evidence for a newer revision. Receipt replay stores its original envelope
// separately and does not use the ledger to reconstruct that result.
func (w *Workspace) retainedDiagnosticEvidence() []string {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	var ids []string
	for _, plan := range w.plans {
		if plan.Preparation != nil {
			for _, stage := range plan.Preparation.Verification {
				ids = append(ids, stage.EvidenceIDs...)
			}
		}
		for _, invariant := range plan.Invariants {
			ids = append(ids, invariant.EvidenceIDs...)
		}
	}
	return ids
}
func (w *Workspace) Diagnostics(since string) (DiagnosticReport, error) {
	return w.diagnostics.query(since, w.retainedDiagnosticEvidence()...)
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
