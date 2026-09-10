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
	ProgressPending  bool                   `json:"progress_pending,omitempty"`
	Selected         bool                   `json:"selected"`
	Dimension        string                 `json:"dimension,omitempty"`
	Findings         []DiagnosticFinding    `json:"findings,omitempty"`
	Candidates       []CulpritCandidate     `json:"candidates,omitempty"`
	ObservedAt       time.Time              `json:"observed_at,omitempty"`
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
}

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
	Resolved           []DiagnosticItem               `json:"resolved"`
	PreexistingCount   int                            `json:"preexisting_count"`
	ProvisionalReasons []string                       `json:"provisional_reasons,omitempty"`
	EvidenceIDs        []string                       `json:"evidence_ids"`
	Cursor             string                         `json:"cursor"`
	Notices            []DiagnosticNotice             `json:"notices,omitempty"`
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
	Evidence           map[string]DiagnosticEvidence  `json:"evidence"`
	Notices            []DiagnosticNotice             `json:"notices"`
	Dimensions         map[string]DiagnosticDimension `json:"dimensions"`
	ProviderDimensions map[string]DiagnosticDimension `json:"provider_dimensions"`
}

type diagnosticStore struct {
	mu    sync.Mutex
	path  string
	state diagnosticState
}

func newDiagnosticStore(stateDir string, workspaceID ID) (*diagnosticStore, error) {
	store := &diagnosticStore{state: diagnosticState{
		Version: diagnosticStateVersion, Items: map[string]DiagnosticItem{}, Active: map[string][]string{},
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
	if err := atomicWrite(s.path, append(content, '\n'), 0o600); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(s.path))
}

func normalizeFinding(f DiagnosticFinding) DiagnosticFinding {
	f.Message = strings.Join(strings.Fields(f.Message), " ")
	f.Source = strings.TrimSpace(f.Source)
	f.Code = strings.TrimSpace(f.Code)
	return f
}

func diagnosticFingerprint(providerID, document string, finding DiagnosticFinding) string {
	encoded, _ := json.Marshal([]any{providerID, filepath.ToSlash(filepath.Clean(document)), finding})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
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
		if batch.Complete && batch.DocumentVersion != nil && batch.ExpectedVersion != nil && *batch.DocumentVersion == *batch.ExpectedVersion {
			return ConfidenceAuthoritative, nil
		}
		return ConfidenceProvisional, []string{"push_missing_or_wrong_document_version"}
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
		return ConfidenceUnavailable, []string{"unsupported_diagnostic_evidence"}
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
	updatedDimension := DiagnosticDimension{State: dimensionState, Confidence: confidence, EvidenceIDs: []string{evID}, Reasons: reasons}
	if batch.Kind == EvidenceProjectCheck {
		s.state.Dimensions[dimension] = updatedDimension
	} else {
		providerDimension := dimension + "\x00" + batch.ProviderID
		if prior, ok := s.state.ProviderDimensions[providerDimension]; ok {
			updatedDimension.EvidenceIDs = appendUnique(prior.EvidenceIDs, evID)
		}
		s.state.ProviderDimensions[providerDimension] = updatedDimension
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
		s.state.Dimensions[dimension] = aggregate
	}

	key := batch.ProviderID + "\x00" + batch.Document
	previous := append([]string(nil), s.state.Active[key]...)
	seen := map[string]bool{}
	var current, added, resolved []DiagnosticItem
	for _, finding := range batch.Findings {
		finding = normalizeFinding(finding)
		id := "diag_" + diagnosticFingerprint(batch.ProviderID, batch.Document, finding)
		if seen[id] {
			continue
		}
		seen[id] = true
		item, existed := s.state.Items[id]
		if !existed {
			item = DiagnosticItem{ID: id, ProviderID: batch.ProviderID, Producer: batch.Producer,
				ProducerVersion: batch.ProducerVersion, Document: batch.Document, DocumentRevision: batch.DocumentRevision,
				DocumentVersion: batch.DocumentVersion, TransactionID: batch.TransactionID, StateSeq: batch.StateSeq,
				Finding: finding, FirstSeen: batch.ObservedAt, Attribution: rankCulprit(batch.Candidates)}
			added = append(added, item)
			s.addNotice("new", item)
		}
		item.LastSeen = batch.ObservedAt
		item.EvidenceIDs = appendUnique(item.EvidenceIDs, evID)
		item.EvidenceKinds = appendUniqueKind(item.EvidenceKinds, batch.Kind)
		s.state.Items[id] = item
		current = append(current, item)
	}
	if batch.Complete && confidenceRank(confidence) >= confidenceRank(ConfidenceCorroborated) {
		for _, id := range previous {
			if seen[id] {
				continue
			}
			item := s.state.Items[id]
			resolved = append(resolved, item)
			s.addNotice("resolved", item)
		}
		s.state.Active[key] = idsOf(current)
	} else {
		s.state.Active[key] = appendUnique(previous, idsOf(current)...)
	}
	if err := s.save(); err != nil {
		return DiagnosticReport{}, err
	}
	return s.reportLocked(added, resolved, evID, reasons), nil
}

func (s *diagnosticStore) addNotice(kind string, item DiagnosticItem) {
	s.state.Sequence++
	s.state.Notices = append(s.state.Notices, DiagnosticNotice{
		Cursor: fmt.Sprintf("diagcur_%d", s.state.Sequence), Kind: kind, ID: item.ID,
		Severity: item.Finding.Severity, Document: item.Document, Attribution: item.Attribution,
	})
}

func (s *diagnosticStore) reportLocked(added, resolved []DiagnosticItem, evID string, reasons []string) DiagnosticReport {
	return DiagnosticReport{Confidence: weakestConfidence(s.state.Dimensions), Coverage: cloneDimensions(s.state.Dimensions),
		New: added, Resolved: resolved, PreexistingCount: len(s.state.Items) - len(added), ProvisionalReasons: reasons,
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
	var notices []DiagnosticNotice
	var added, resolved []DiagnosticItem
	for _, notice := range s.state.Notices {
		n, _ := parseDiagnosticCursor(notice.Cursor)
		if n <= seq {
			continue
		}
		notices = append(notices, notice)
		if notice.Kind == "new" {
			added = append(added, s.state.Items[notice.ID])
		} else {
			resolved = append(resolved, s.state.Items[notice.ID])
		}
	}
	if err := s.save(); err != nil {
		return DiagnosticReport{}, err
	}
	report := DiagnosticReport{Confidence: weakestConfidence(s.state.Dimensions), Coverage: cloneDimensions(s.state.Dimensions),
		New: added, Resolved: resolved, PreexistingCount: len(s.state.Items) - len(added), EvidenceIDs: evidenceIDsFromDimensions(s.state.Dimensions), Cursor: fmt.Sprintf("diagcur_%d", s.state.Sequence), Notices: notices}
	if len(s.state.Dimensions) == 0 {
		report.Confidence = ConfidenceUnavailable
		report.Coverage = map[string]DiagnosticDimension{"edited_documents": {State: "unavailable", Confidence: ConfidenceUnavailable, Reasons: []string{"no_diagnostic_evidence"}}}
		report.ProvisionalReasons = []string{"no_diagnostic_evidence"}
	}
	return report, nil
}

func (s *diagnosticStore) pendingNotices(limit int) []DiagnosticNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	var out []DiagnosticNotice
	for _, notice := range s.state.Notices {
		seq, _ := parseDiagnosticCursor(notice.Cursor)
		if seq > s.state.Ack {
			out = append(out, notice)
		}
		if len(out) == limit {
			break
		}
	}
	return out
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
func (w *Workspace) DiagnosticNotices(limit int) []DiagnosticNotice {
	return w.diagnostics.pendingNotices(limit)
}
func (w *Workspace) Evidence(id string) (DiagnosticEvidence, error) {
	return w.diagnostics.evidenceByID(id)
}
