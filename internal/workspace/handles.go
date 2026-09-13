package workspace

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultHandleTTL = 30 * time.Minute

type HandleID string

type HandleKind string

const (
	HandleRange  HandleKind = "range"
	HandleMatch  HandleKind = "match"
	HandleSymbol HandleKind = "symbol"
)

type SemanticLocator struct {
	Path             string     `json:"path"`
	Language         string     `json:"language,omitempty"`
	Kind             string     `json:"kind"`
	NamePath         string     `json:"name_path,omitempty"`
	ParentPath       string     `json:"parent_path,omitempty"`
	ByteStart        int        `json:"byte_start"`
	ByteEnd          int        `json:"byte_end"`
	SignatureSHA256  string     `json:"signature_sha256,omitempty"`
	NodeSHA256       string     `json:"node_sha256"`
	ContentSHA256    string     `json:"content_sha256"`
	BeforeSHA256     string     `json:"anchor_before_sha256"`
	AfterSHA256      string     `json:"anchor_after_sha256"`
	AnchorBytes      int        `json:"anchor_bytes"`
	DocumentRevision RevisionID `json:"document_revision"`
	Device           uint64     `json:"-"`
	Inode            uint64     `json:"-"`
}

type HandleRecord struct {
	Handle      HandleID        `json:"handle"`
	WorkspaceID ID              `json:"workspace_id"`
	Epoch       uint64          `json:"epoch"`
	Kind        HandleKind      `json:"kind"`
	Display     string          `json:"display"`
	ExpiresAt   time.Time       `json:"expires_at"`
	Locator     SemanticLocator `json:"locator"`
}

type ResolutionStatus string

const (
	ResolutionExact      ResolutionStatus = "exact"
	ResolutionRelocated  ResolutionStatus = "relocated"
	ResolutionConflicted ResolutionStatus = "conflicted"
)

type HandleCandidate struct {
	Display string          `json:"display"`
	Locator SemanticLocator `json:"locator"`
}

type HandleResolution struct {
	Status     ResolutionStatus  `json:"status"`
	Code       ConflictCode      `json:"code,omitempty"`
	Handle     HandleID          `json:"handle"`
	Original   SemanticLocator   `json:"original"`
	Current    *SemanticLocator  `json:"current,omitempty"`
	Candidates []HandleCandidate `json:"candidates,omitempty"`
}

type ResultSetKind string

const (
	ResultSetCurrentSource ResultSetKind = "current_source"
	ResultSetHistorical    ResultSetKind = "historical"
)

type ResultSetID string

type ResultConstraint struct {
	Kind  string     `json:"kind"`
	Value string     `json:"value"`
	Mode  SearchMode `json:"mode,omitempty"`
}

type ResultSet struct {
	Handle             ResultSetID           `json:"handle"`
	Parent             ResultSetID           `json:"parent,omitempty"`
	Kind               ResultSetKind         `json:"kind"`
	WorkspaceID        ID                    `json:"workspace_id"`
	Epoch              uint64                `json:"epoch"`
	StateSeq           uint64                `json:"state_seq"`
	ExpiresAt          time.Time             `json:"expires_at"`
	Matches            []SearchHit           `json:"-"`
	MatchCount         int                   `json:"match_count"`
	FileCount          int                   `json:"file_count"`
	Complete           bool                  `json:"complete"`
	NonOverlapping     bool                  `json:"non_overlapping"`
	AllMatchesEligible bool                  `json:"all_matches_eligible"`
	Retained           int                   `json:"retained"`
	Eliminated         int                   `json:"eliminated"`
	Coverage           Coverage              `json:"coverage"`
	Query              string                `json:"query,omitempty"`
	Mode               SearchMode            `json:"mode,omitempty"`
	Constraints        []ResultConstraint    `json:"constraints,omitempty"`
	DocumentRevisions  map[string]RevisionID `json:"-"`
	SourceFiles        []string              `json:"-"`
}

type ResultRefinement struct {
	Path         string
	MatchLiteral string
	MatchRegex   string
}

// Retention bounds for the in-memory handle and result-set store. Expired
// entries used to be removed only when they were touched, so a workspace
// that kept issuing handles grew without limit.
const (
	// handleSweepInterval is the longest an expired handle or result set
	// stays in memory before an insert sweeps it out.
	handleSweepInterval = time.Minute
	// handleSweepThreshold forces a sweep after this many inserts since the
	// previous sweep, regardless of elapsed time.
	handleSweepThreshold = 1024
	// maxLiveHandles caps the handles kept per workspace; when exceeded the
	// entries closest to expiry are evicted first.
	maxLiveHandles = 20000
	// maxLiveResultSets caps the frozen result sets kept per workspace.
	maxLiveResultSets = 64
	// maxRetainedMatchBytes caps the matched text retained per hit in a
	// frozen result set. Longer matches keep only the locator and are read
	// back from the (revision-validated) document when needed.
	maxRetainedMatchBytes = 256
)

type handleStore struct {
	mu      sync.RWMutex
	ttl     time.Duration
	handles map[HandleID]HandleRecord
	sets    map[ResultSetID]ResultSet

	maxHandles        int
	maxSets           int
	lastSweep         time.Time
	insertsSinceSweep int
}

func newHandleStore() *handleStore {
	return &handleStore{
		ttl: defaultHandleTTL, handles: make(map[HandleID]HandleRecord), sets: make(map[ResultSetID]ResultSet),
		maxHandles: maxLiveHandles, maxSets: maxLiveResultSets, lastSweep: time.Now(),
	}
}

// putHandleLocked stores a record and applies retention. The caller holds
// store.mu for writing.
func (s *handleStore) putHandleLocked(record HandleRecord) {
	s.handles[record.Handle] = record
	s.noteInsertLocked()
}

// putSetLocked stores a result set and applies retention. The caller holds
// store.mu for writing.
func (s *handleStore) putSetLocked(set ResultSet) {
	s.sets[set.Handle] = set
	s.noteInsertLocked()
}

func (s *handleStore) noteInsertLocked() {
	s.insertsSinceSweep++
	now := time.Now()
	if s.insertsSinceSweep >= handleSweepThreshold || now.Sub(s.lastSweep) >= handleSweepInterval ||
		len(s.handles) > s.maxHandles || len(s.sets) > s.maxSets {
		s.sweepLocked(now)
	}
}

// sweepLocked drops expired handles and result sets, then evicts the entries
// closest to expiry until the store is within its caps.
func (s *handleStore) sweepLocked(now time.Time) {
	s.lastSweep = now
	s.insertsSinceSweep = 0
	for id, record := range s.handles {
		if !record.ExpiresAt.After(now) {
			delete(s.handles, id)
		}
	}
	for id, set := range s.sets {
		if !set.ExpiresAt.After(now) {
			delete(s.sets, id)
		}
	}
	if excess := len(s.handles) - s.maxHandles; excess > 0 {
		ids := make([]HandleID, 0, len(s.handles))
		for id := range s.handles {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			left, right := s.handles[ids[i]].ExpiresAt, s.handles[ids[j]].ExpiresAt
			if left.Equal(right) {
				return ids[i] < ids[j]
			}
			return left.Before(right)
		})
		for _, id := range ids[:excess] {
			delete(s.handles, id)
		}
	}
	if excess := len(s.sets) - s.maxSets; excess > 0 {
		ids := make([]ResultSetID, 0, len(s.sets))
		for id := range s.sets {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			left, right := s.sets[ids[i]].ExpiresAt, s.sets[ids[j]].ExpiresAt
			if left.Equal(right) {
				return ids[i] < ids[j]
			}
			return left.Before(right)
		})
		for _, id := range ids[:excess] {
			delete(s.sets, id)
		}
	}
}

// HandleStoreSize reports the live handle and result-set counts. It exists
// for tests and inspection of retention behaviour.
func (w *Workspace) HandleStoreSize() (handles, sets int) {
	store := w.handleRegistry()
	store.mu.RLock()
	defer store.mu.RUnlock()
	return len(store.handles), len(store.sets)
}

// setHandleCaps lowers the retention caps; tests use it to exercise
// eviction without registering tens of thousands of handles.
func (w *Workspace) setHandleCaps(handles, sets int) {
	store := w.handleRegistry()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.maxHandles, store.maxSets = handles, sets
}

func (w *Workspace) handleRegistry() *handleStore {
	w.handlesMu.Lock()
	defer w.handlesMu.Unlock()
	if w.handles == nil {
		w.handles = newHandleStore()
	}
	return w.handles
}

func randomOpaque(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func (w *Workspace) SetHandleTTL(ttl time.Duration) {
	store := w.handleRegistry()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.ttl = ttl
}

func (w *Workspace) RegisterRangeHandle(handle RangeHandle, kind HandleKind, display string) (HandleRecord, error) {
	if kind != HandleRange && kind != HandleMatch {
		return HandleRecord{}, fmt.Errorf("invalid range handle kind %q", kind)
	}
	current, content, err := w.validateRange(w.Identity().ID, handle)
	if err != nil {
		return HandleRecord{}, err
	}
	id, err := randomOpaque("rng_")
	if err != nil {
		return HandleRecord{}, err
	}
	if display == "" {
		line, column := bytePosition(content, handle.ByteStart)
		display = fmt.Sprintf("%s:%d:%d exact range", displayPath(w.Identity().Root, handle.Path), line, column)
	}
	record := HandleRecord{
		Handle: HandleID(id), WorkspaceID: current.Workspace.ID, Epoch: current.Workspace.Epoch,
		Kind: kind, Display: display,
		Locator: SemanticLocator{
			Path: handle.Path, Kind: string(kind), ByteStart: handle.ByteStart, ByteEnd: handle.ByteEnd,
			NodeSHA256: handle.ExpectedSHA256, ContentSHA256: handle.ExpectedSHA256, BeforeSHA256: handle.BeforeSHA256, AfterSHA256: handle.AfterSHA256,
			AnchorBytes: handle.AnchorBytes, DocumentRevision: handle.Revision,
			Device: current.Disk.Device, Inode: current.Disk.Inode,
		},
	}
	store := w.handleRegistry()
	store.mu.Lock()
	defer store.mu.Unlock()
	record.ExpiresAt = time.Now().Add(store.ttl)
	store.putHandleLocked(record)
	return record, nil
}

func normalizedFingerprint(content []byte) string {
	return hashBytes(bytes.Join(bytes.Fields(content), []byte(" ")))
}

func signatureFingerprint(content []byte) string {
	line := content
	if index := bytes.IndexByte(content, '\n'); index >= 0 {
		line = content[:index]
	}
	return normalizedFingerprint(line)
}

func (w *Workspace) registerSymbol(read TextRead, section Section) (HandleRecord, error) {
	if section.ByteStart < 0 || section.ByteEnd <= section.ByteStart || section.ByteEnd > len(read.Content) {
		return HandleRecord{}, fmt.Errorf("invalid section range for %s", section.Name)
	}
	rangeValue, err := rangeHandle(read, section.ByteStart, section.ByteEnd, defaultAnchorBytes)
	if err != nil {
		return HandleRecord{}, err
	}
	id, err := randomOpaque("sym_")
	if err != nil {
		return HandleRecord{}, err
	}
	line, column := bytePosition(read.Content, section.ByteStart)
	record := HandleRecord{
		Handle: HandleID(id), WorkspaceID: read.Workspace.ID, Epoch: read.Workspace.Epoch, Kind: HandleSymbol,
		Display: fmt.Sprintf("%s:%d:%d %s %s", displayPath(read.Workspace.Root, read.Path), line, column, section.Name, section.Kind),
		Locator: SemanticLocator{
			Path: read.Path, Kind: section.Kind, NamePath: section.Name, ByteStart: section.ByteStart, ByteEnd: section.ByteEnd,
			SignatureSHA256: signatureFingerprint(read.Content[section.ByteStart:section.ByteEnd]),
			NodeSHA256:      normalizedFingerprint(read.Content[section.ByteStart:section.ByteEnd]),
			ContentSHA256:   hashBytes(read.Content[section.ByteStart:section.ByteEnd]),
			BeforeSHA256:    rangeValue.BeforeSHA256, AfterSHA256: rangeValue.AfterSHA256,
			AnchorBytes: rangeValue.AnchorBytes, DocumentRevision: read.Snapshot.Revision,
			Device: read.Snapshot.Disk.Device, Inode: read.Snapshot.Disk.Inode,
		},
	}
	store := w.handleRegistry()
	store.mu.Lock()
	defer store.mu.Unlock()
	record.ExpiresAt = time.Now().Add(store.ttl)
	store.putHandleLocked(record)
	return record, nil
}

func (w *Workspace) RegisterSymbolHandle(path, name, kind string, byteStart, byteEnd int) (HandleRecord, error) {
	read, err := w.Read(path)
	if err != nil {
		return HandleRecord{}, err
	}
	return w.registerSymbol(read, Section{Name: name, Kind: kind, ByteStart: byteStart, ByteEnd: byteEnd})
}

func (w *Workspace) FindSymbols(query string) ([]HandleRecord, Coverage, error) {
	if strings.TrimSpace(query) == "" {
		return nil, Coverage{}, errors.New("symbol query is required")
	}
	if w.sectioner == nil {
		return nil, Coverage{Complete: false, Semantic: "text_only", Skipped: []string{"semantic parser unavailable"}}, nil
	}
	files, coverage, err := w.collectFiles()
	if err != nil {
		return nil, Coverage{}, err
	}
	var records []HandleRecord
	for _, path := range files {
		read, readErr := w.Read(path)
		if readErr != nil {
			coverage.noteSkipped(displayPath(w.identity.Root, path), sanitizeText(readErr.Error(), 256))
			continue
		}
		sections, sectionErr := w.sectioner.Sections(read.Path, read.Content)
		if sectionErr != nil {
			coverage.noteSkipped(displayPath(w.identity.Root, path), sanitizeText(sectionErr.Error(), 256))
			continue
		}
		for _, section := range sections {
			if !strings.Contains(section.Name, query) {
				continue
			}
			record, registerErr := w.registerSymbol(read, section)
			if registerErr != nil {
				return nil, coverage, registerErr
			}
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Locator.Path == records[j].Locator.Path {
			return records[i].Locator.ByteStart < records[j].Locator.ByteStart
		}
		return records[i].Locator.Path < records[j].Locator.Path
	})
	return records, coverage, nil
}

// ResolveSymbolLocator resolves an exact declaration locator from either the
// built-in sectioner or declarations previously registered by a semantic
// provider. Provider-backed symbol_find calls register durable opaque handles;
// plans using the human-readable locator must be able to select the same
// declaration without requiring callers to substitute the opaque handle.
func (w *Workspace) ResolveSymbolLocator(path, namePath string) (HandleRecord, error) {
	records, _, err := w.FindSymbols(namePath)
	if err != nil {
		return HandleRecord{}, err
	}
	store := w.handleRegistry()
	store.mu.RLock()
	for _, record := range store.handles {
		if record.Kind == HandleSymbol && record.ExpiresAt.After(time.Now()) {
			records = append(records, record)
		}
	}
	store.mu.RUnlock()

	unique := make(map[string]HandleRecord)
	for _, record := range records {
		if record.Locator.Path != path || record.Locator.NamePath != namePath {
			continue
		}
		resolution, resolveErr := w.ResolveHandle(record.Handle)
		if resolveErr != nil || resolution.Status == ResolutionConflicted {
			continue
		}
		rangeHandle, rangeErr := resolution.RangeHandle()
		if rangeErr != nil {
			continue
		}
		key := fmt.Sprintf("%s:%d:%d:%s", rangeHandle.Path, rangeHandle.ByteStart, rangeHandle.ByteEnd, rangeHandle.ExpectedSHA256)
		unique[key] = record
	}
	if len(unique) != 1 {
		return HandleRecord{}, fmt.Errorf("symbol locator resolved to %d declarations", len(unique))
	}
	for _, record := range unique {
		return record, nil
	}
	panic("unreachable")
}

func (w *Workspace) InspectHandle(id HandleID) (HandleRecord, error) {
	store := w.handleRegistry()
	store.mu.RLock()
	record, ok := store.handles[id]
	store.mu.RUnlock()
	if !ok {
		// Handles live in a bounded in-memory store, so one can be missing
		// because it was never issued, because the cap dropped it, or because
		// the service was restarted under the caller. The caller cannot tell
		// those apart and does not need to: all three mean take a fresh one.
		return HandleRecord{}, Codedf(CodeHandleUnknown,
			"handle %s is not known to this service instance; handles are held in memory, so one is lost when the cap drops it or the service restarts", id)
	}
	if !record.ExpiresAt.After(time.Now()) {
		store.mu.Lock()
		delete(store.handles, id)
		store.mu.Unlock()
		return HandleRecord{}, Codedf(CodeHandleExpired, "handle %s expired at %s", id, record.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if identity := w.Identity(); record.WorkspaceID != identity.ID || record.Epoch != identity.Epoch {
		return HandleRecord{}, &Conflict{Code: ConflictWorkspaceEpoch, Path: record.Locator.Path}
	}
	return record, nil
}

func (w *Workspace) ResolveHandle(id HandleID) (HandleResolution, error) {
	record, err := w.InspectHandle(id)
	if err != nil {
		var conflict *Conflict
		if errors.As(err, &conflict) {
			return HandleResolution{Status: ResolutionConflicted, Code: conflict.Code, Handle: id}, nil
		}
		return HandleResolution{}, err
	}
	if record.Kind == HandleSymbol {
		return w.resolveSymbol(record)
	}
	return w.resolveRange(record)
}

func (w *Workspace) resolveRange(record HandleRecord) (HandleResolution, error) {
	original := record.Locator
	read, err := w.Read(original.Path)
	if err != nil {
		return HandleResolution{Status: ResolutionConflicted, Code: ConflictTargetDeleted, Handle: record.Handle, Original: original}, nil
	}
	if read.Snapshot.Revision == original.DocumentRevision && locatorMatches(read.Content, original, original.ByteStart) {
		current := original
		return HandleResolution{Status: ResolutionExact, Handle: record.Handle, Original: original, Current: &current}, nil
	}
	candidates := locateRanges(read, original)
	if len(candidates) == 1 {
		current := candidates[0].Locator
		return HandleResolution{Status: ResolutionRelocated, Code: ConflictFormatOnlyRelocation, Handle: record.Handle, Original: original, Current: &current, Candidates: candidates}, nil
	}
	// Several identical spans: the one still at the original offset with
	// the same bytes before it is the original occurrence, because nothing
	// before it moved; only the bytes after it changed (a neighbouring
	// edit), which is not a reason to refuse.
	for _, candidate := range candidates {
		if candidate.Locator.ByteStart == original.ByteStart && candidate.Locator.BeforeSHA256 == original.BeforeSHA256 {
			current := candidate.Locator
			return HandleResolution{Status: ResolutionRelocated, Code: ConflictFormatOnlyRelocation, Handle: record.Handle, Original: original, Current: &current, Candidates: candidates}, nil
		}
	}
	code := ConflictDocumentChanged
	if len(candidates) > 1 {
		code = ConflictSymbolAmbiguous
	}
	return HandleResolution{Status: ResolutionConflicted, Code: code, Handle: record.Handle, Original: original, Candidates: candidates}, nil
}

func locatorMatches(content []byte, locator SemanticLocator, start int) bool {
	end := start + locator.ByteEnd - locator.ByteStart
	if start < 0 || end < start || end > len(content) || hashBytes(content[start:end]) != locator.NodeSHA256 {
		return false
	}
	beforeStart := start - locator.AnchorBytes
	if beforeStart < 0 {
		beforeStart = 0
	}
	afterEnd := end + locator.AnchorBytes
	if afterEnd > len(content) {
		afterEnd = len(content)
	}
	return hashBytes(content[beforeStart:start]) == locator.BeforeSHA256 &&
		hashBytes(content[end:afterEnd]) == locator.AfterSHA256
}

func locateRanges(read TextRead, locator SemanticLocator) []HandleCandidate {
	length := locator.ByteEnd - locator.ByteStart
	if length <= 0 || length > len(read.Content) {
		return nil
	}
	var all, anchored []HandleCandidate
	for start := 0; start+length <= len(read.Content); start++ {
		end := start + length
		if hashBytes(read.Content[start:end]) != locator.ContentSHA256 {
			continue
		}
		beforeStart := start - locator.AnchorBytes
		if beforeStart < 0 {
			beforeStart = 0
		}
		afterEnd := end + locator.AnchorBytes
		if afterEnd > len(read.Content) {
			afterEnd = len(read.Content)
		}
		current := locator
		current.Path = read.Path
		current.ByteStart = start
		current.ByteEnd = end
		current.DocumentRevision = read.Snapshot.Revision
		current.Device = read.Snapshot.Disk.Device
		current.Inode = read.Snapshot.Disk.Inode
		current.BeforeSHA256 = hashBytes(read.Content[beforeStart:start])
		current.AfterSHA256 = hashBytes(read.Content[end:afterEnd])
		line, column := bytePosition(read.Content, start)
		candidate := HandleCandidate{Display: fmt.Sprintf("%s:%d:%d", read.Path, line, column), Locator: current}
		all = append(all, candidate)
		if current.BeforeSHA256 == locator.BeforeSHA256 && current.AfterSHA256 == locator.AfterSHA256 {
			anchored = append(anchored, candidate)
		}
	}
	if len(anchored) == 1 {
		return anchored
	}
	return all
}

func (w *Workspace) resolveSymbol(record HandleRecord) (HandleResolution, error) {
	original := record.Locator
	if resolution, ok := w.resolveSymbolInPlace(record); ok {
		return resolution, nil
	}
	if w.sectioner == nil {
		return w.resolveSymbolByBytes(record)
	}
	sameName, fingerprint, err := w.sectionCandidates(original)
	if err != nil {
		return HandleResolution{}, err
	}
	if len(fingerprint) == 1 {
		current := fingerprint[0].Locator
		if current.Path == original.Path && current.ByteStart == original.ByteStart &&
			current.DocumentRevision == original.DocumentRevision {
			return HandleResolution{Status: ResolutionExact, Handle: record.Handle, Original: original, Current: &current}, nil
		}
		// A different device or inode with the same declaration bytes is an
		// atomic save, a checkout or a delete/recreate of equal content. The
		// content hash is the authoritative token, so the handle stays bound.
		code := ConflictFormatOnlyRelocation
		if current.Path != original.Path {
			code = ConflictSymbolMoved
		}
		return HandleResolution{Status: ResolutionRelocated, Code: code, Handle: record.Handle, Original: original, Current: &current, Candidates: fingerprint}, nil
	}
	code := ConflictTargetDeleted
	if len(fingerprint) > 1 {
		code = ConflictSymbolAmbiguous
	} else if len(sameName) > 0 {
		code = ConflictSymbolSignatureChanged
	}
	return HandleResolution{Status: ResolutionConflicted, Code: code, Handle: record.Handle, Original: original, Candidates: sameName}, nil
}

// resolveSymbolInPlace answers a handle whose recorded bytes are still at the recorded
// range of the recorded document revision, without any search.
func (w *Workspace) resolveSymbolInPlace(record HandleRecord) (HandleResolution, bool) {
	original := record.Locator
	read, readErr := w.Read(original.Path)
	if readErr != nil || read.Snapshot.Revision != original.DocumentRevision ||
		original.ByteStart < 0 || original.ByteEnd > len(read.Content) || original.ByteEnd <= original.ByteStart {
		return HandleResolution{}, false
	}
	if hashBytes(read.Content[original.ByteStart:original.ByteEnd]) != original.ContentSHA256 {
		return HandleResolution{}, false
	}
	current := original
	current.DocumentRevision = read.Snapshot.Revision
	current.Device, current.Inode = read.Snapshot.Disk.Device, read.Snapshot.Disk.Inode
	status := ResolutionExact
	if current.DocumentRevision != original.DocumentRevision {
		status = ResolutionRelocated
	}
	return HandleResolution{Status: status, Handle: record.Handle, Original: original, Current: &current}, true
}

// resolveSymbolByBytes relocates a handle by its exact bytes when no sectioner is
// available: one match relocates, several are ambiguous, none is a changed or deleted
// document.
func (w *Workspace) resolveSymbolByBytes(record HandleRecord) (HandleResolution, error) {
	original := record.Locator
	files, _, err := w.collectFiles()
	if err != nil {
		return HandleResolution{}, err
	}
	var candidates []HandleCandidate
	for _, path := range files {
		read, readErr := w.Read(path)
		if readErr == nil {
			candidates = append(candidates, locateRanges(read, original)...)
		}
	}
	if len(candidates) == 1 {
		current := candidates[0].Locator
		code := ConflictFormatOnlyRelocation
		if current.Path != original.Path {
			code = ConflictSymbolMoved
		}
		return HandleResolution{Status: ResolutionRelocated, Code: code, Handle: record.Handle, Original: original, Current: &current, Candidates: candidates}, nil
	}
	code := ConflictDocumentChanged
	if len(candidates) > 1 {
		code = ConflictSymbolAmbiguous
	} else if _, readErr := w.Read(original.Path); readErr != nil {
		code = ConflictTargetDeleted
	}
	return HandleResolution{Status: ResolutionConflicted, Code: code, Handle: record.Handle, Original: original, Candidates: candidates}, nil
}

// sectionCandidates lists every parsed declaration in the workspace with the original's
// name and kind, and the subset whose normalized node and signature fingerprints match.
func (w *Workspace) sectionCandidates(original SemanticLocator) (sameName, fingerprint []HandleCandidate, err error) {
	files, _, err := w.collectFiles()
	if err != nil {
		return nil, nil, err
	}
	for _, path := range files {
		read, readErr := w.Read(path)
		if readErr != nil {
			continue
		}
		sections, sectionErr := w.sectioner.Sections(read.Path, read.Content)
		if sectionErr != nil {
			continue
		}
		for _, section := range sections {
			candidate, ok := sectionCandidate(read, section, original)
			if !ok {
				continue
			}
			sameName = append(sameName, candidate)
			if candidate.Locator.NodeSHA256 == original.NodeSHA256 && candidate.Locator.SignatureSHA256 == original.SignatureSHA256 {
				fingerprint = append(fingerprint, candidate)
			}
		}
	}
	return sameName, fingerprint, nil
}

// sectionCandidate builds the candidate locator for one parsed section that has the
// original's name and kind and a valid range.
func sectionCandidate(read TextRead, section Section, original SemanticLocator) (HandleCandidate, bool) {
	if section.Name != original.NamePath || section.Kind != original.Kind ||
		section.ByteStart < 0 || section.ByteEnd > len(read.Content) || section.ByteEnd <= section.ByteStart {
		return HandleCandidate{}, false
	}
	content := read.Content[section.ByteStart:section.ByteEnd]
	locator := original
	locator.Path, locator.ByteStart, locator.ByteEnd = read.Path, section.ByteStart, section.ByteEnd
	locator.DocumentRevision = read.Snapshot.Revision
	locator.Device, locator.Inode = read.Snapshot.Disk.Device, read.Snapshot.Disk.Inode
	locator.NodeSHA256 = normalizedFingerprint(content)
	locator.SignatureSHA256 = signatureFingerprint(content)
	locator.ContentSHA256 = hashBytes(content)
	currentRange, rangeErr := rangeHandle(read, section.ByteStart, section.ByteEnd, locator.AnchorBytes)
	if rangeErr != nil {
		return HandleCandidate{}, false
	}
	locator.BeforeSHA256 = currentRange.BeforeSHA256
	locator.AfterSHA256 = currentRange.AfterSHA256
	line, column := bytePosition(read.Content, section.ByteStart)
	return HandleCandidate{Display: fmt.Sprintf("%s:%d:%d %s %s", read.Path, line, column, section.Name, section.Kind), Locator: locator}, true
}

func nonOverlapping(hits []SearchHit) bool {
	sorted := append([]SearchHit(nil), hits...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Path == sorted[j].Path {
			return sorted[i].ByteStart < sorted[j].ByteStart
		}
		return sorted[i].Path < sorted[j].Path
	})
	for index := 1; index < len(sorted); index++ {
		if sorted[index-1].Path == sorted[index].Path && sorted[index].ByteStart < sorted[index-1].ByteEnd {
			return false
		}
	}
	return true
}

func (w *Workspace) FreezeSearch(result SearchResult) (ResultSet, error) {
	id, err := randomOpaque("set_")
	if err != nil {
		return ResultSet{}, err
	}
	identity := w.Identity()
	files := make(map[string]struct{})
	for _, hit := range result.Hits {
		files[hit.Path] = struct{}{}
	}
	revisions := make(map[string]RevisionID, len(result.DocumentRevisions))
	for path, revision := range result.DocumentRevisions {
		revisions[path] = revision
	}
	set := ResultSet{
		Handle: ResultSetID(id), Kind: ResultSetCurrentSource, WorkspaceID: identity.ID, Epoch: identity.Epoch,
		StateSeq: identity.StateSeq, Matches: retainedHits(result.Hits), MatchCount: len(result.Hits),
		FileCount: len(files), Complete: result.Coverage.Complete, NonOverlapping: nonOverlapping(result.Hits),
		Retained: len(result.Hits), Coverage: result.Coverage, Query: result.Query, Mode: result.Mode,
		DocumentRevisions: revisions, SourceFiles: append([]string(nil), result.SourceFiles...),
	}
	set.AllMatchesEligible = set.Complete && set.NonOverlapping
	store := w.handleRegistry()
	store.mu.Lock()
	defer store.mu.Unlock()
	set.ExpiresAt = time.Now().Add(store.ttl)
	store.putSetLocked(set)
	return set, nil
}

// retainedHits copies hits for storage inside a frozen result set, keeping
// at most maxRetainedMatchBytes of matched text per hit. The locator is
// always kept, so hydrateHits can read the exact bytes back later.
func retainedHits(hits []SearchHit) []SearchHit {
	retained := make([]SearchHit, len(hits))
	for index, hit := range hits {
		if len(hit.Match) > maxRetainedMatchBytes {
			hit.Match = ""
			hit.MatchTruncated = true
		}
		retained[index] = hit
	}
	return retained
}

// hydrateHits restores the matched text for hits whose content was not
// retained. It is only valid after InspectResultSet confirmed that every
// source document still carries the frozen revision.
func (w *Workspace) hydrateHits(hits []SearchHit) ([]SearchHit, error) {
	result := append([]SearchHit(nil), hits...)
	contents := map[string][]byte{}
	for index := range result {
		hit := &result[index]
		if !hit.MatchTruncated {
			continue
		}
		content, cached := contents[hit.Path]
		if !cached {
			read, err := w.Read(hit.Path)
			if err != nil {
				return nil, err
			}
			content = read.Content
			contents[hit.Path] = content
		}
		if hit.ByteStart < 0 || hit.ByteEnd > len(content) || hit.ByteEnd < hit.ByteStart {
			return nil, &Conflict{Code: ConflictDocumentChanged, Path: hit.Path}
		}
		hit.Match = string(content[hit.ByteStart:hit.ByteEnd])
		hit.MatchTruncated = false
	}
	return result, nil
}

func (w *Workspace) FreezeHistoricalResultSet(matchCount, fileCount int, coverage Coverage) (ResultSet, error) {
	set, err := w.FreezeSearch(SearchResult{Workspace: w.Identity(), Coverage: coverage})
	if err != nil {
		return ResultSet{}, err
	}
	store := w.handleRegistry()
	store.mu.Lock()
	defer store.mu.Unlock()
	set.Kind = ResultSetHistorical
	set.MatchCount = matchCount
	set.FileCount = fileCount
	set.Retained = matchCount
	set.AllMatchesEligible = false
	set.Matches = nil
	store.sets[set.Handle] = set
	return set, nil
}

func (w *Workspace) InspectResultSet(id ResultSetID) (ResultSet, error) {
	store := w.handleRegistry()
	store.mu.RLock()
	set, ok := store.sets[id]
	store.mu.RUnlock()
	if !ok {
		return ResultSet{}, errors.New("unknown result set")
	}
	if !set.ExpiresAt.After(time.Now()) {
		store.mu.Lock()
		delete(store.sets, id)
		store.mu.Unlock()
		return ResultSet{}, errors.New("result set expired")
	}
	identity := w.Identity()
	if set.WorkspaceID != identity.ID || set.Epoch != identity.Epoch {
		return ResultSet{}, &Conflict{Code: ConflictWorkspaceEpoch}
	}
	if set.Kind == ResultSetCurrentSource {
		files, _, err := w.collectFiles()
		if err != nil || len(files) != len(set.SourceFiles) {
			return ResultSet{}, &Conflict{Code: ConflictDocumentChanged}
		}
		for index, path := range files {
			if path != set.SourceFiles[index] {
				return ResultSet{}, &Conflict{Code: ConflictDocumentChanged, Path: displayPath(w.identity.Root, path)}
			}
		}
		for path, expected := range set.DocumentRevisions {
			read, readErr := w.Read(path)
			if readErr != nil || expected != read.Snapshot.Revision {
				return ResultSet{}, &Conflict{Code: ConflictDocumentChanged, Path: path}
			}
		}
	}
	if set.StateSeq != w.Identity().StateSeq {
		return ResultSet{}, &Conflict{Code: ConflictDocumentChanged}
	}
	return set, nil
}

func (w *Workspace) ResolveAllMatches(id ResultSetID) ([]SearchHit, error) {
	set, err := w.InspectResultSet(id)
	if err != nil {
		return nil, err
	}
	if set.Kind != ResultSetCurrentSource {
		return nil, errors.New("historical result sets are not editable")
	}
	if !set.Complete {
		return nil, errors.New("incomplete result set is not eligible for all-match replacement")
	}
	if !set.NonOverlapping {
		return nil, errors.New("overlapping result set is not eligible for all-match replacement")
	}
	for _, hit := range set.Matches {
		if hit.MatchHandle == nil {
			return nil, errors.New("result set contains a match without a handle")
		}
		resolution, resolveErr := w.ResolveHandle(hit.MatchHandle.Handle)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if resolution.Status != ResolutionExact {
			return nil, &Conflict{Code: ConflictDocumentChanged, Path: hit.Path}
		}
	}
	return w.hydrateHits(set.Matches)
}

func (w *Workspace) RefineResultSet(parentID ResultSetID, refinement ResultRefinement) (ResultSet, error) {
	parent, err := w.InspectResultSet(parentID)
	if err != nil {
		return ResultSet{}, err
	}
	if parent.Kind != ResultSetCurrentSource {
		return ResultSet{}, errors.New("historical result sets cannot be refined as current source")
	}
	var expression *regexp.Regexp
	if refinement.MatchRegex != "" {
		expression, err = regexp.Compile(refinement.MatchRegex)
		if err != nil {
			return ResultSet{}, fmt.Errorf("invalid refinement regular expression: %w", err)
		}
	}
	parentHits, err := w.hydrateHits(parent.Matches)
	if err != nil {
		return ResultSet{}, err
	}
	hits := make([]SearchHit, 0, len(parentHits))
	for _, hit := range parentHits {
		if refinement.Path != "" && !strings.Contains(hit.Path, refinement.Path) {
			continue
		}
		if refinement.MatchLiteral != "" && !strings.Contains(hit.Match, refinement.MatchLiteral) {
			continue
		}
		if expression != nil && !expression.MatchString(hit.Match) {
			continue
		}
		hits = append(hits, hit)
	}
	child, err := w.FreezeSearch(SearchResult{
		Workspace: w.Identity(), Hits: hits, Coverage: parent.Coverage, Query: parent.Query, Mode: parent.Mode,
		DocumentRevisions: parent.DocumentRevisions, SourceFiles: parent.SourceFiles,
	})
	if err != nil {
		return ResultSet{}, err
	}
	child.Parent = parent.Handle
	child.Constraints = append([]ResultConstraint(nil), parent.Constraints...)
	if refinement.Path != "" {
		child.Constraints = append(child.Constraints, ResultConstraint{Kind: "path", Value: refinement.Path})
	}
	if refinement.MatchLiteral != "" {
		child.Constraints = append(child.Constraints, ResultConstraint{Kind: "matched_text", Value: refinement.MatchLiteral, Mode: SearchLiteral})
	}
	if refinement.MatchRegex != "" {
		child.Constraints = append(child.Constraints, ResultConstraint{Kind: "matched_text", Value: refinement.MatchRegex, Mode: SearchRegex})
	}
	child.Complete = parent.Complete && child.Complete
	child.AllMatchesEligible = child.Complete && child.NonOverlapping
	child.Retained = len(hits)
	child.Eliminated = len(parent.Matches) - len(hits)
	store := w.handleRegistry()
	store.mu.Lock()
	store.putSetLocked(child)
	store.mu.Unlock()
	// The stored copy keeps only bounded match text; the returned set carries
	// the exact hits the caller filtered on.
	child.Matches = hits
	return child, nil
}

func (resolution HandleResolution) RangeHandle() (RangeHandle, error) {
	if resolution.Status == ResolutionConflicted || resolution.Current == nil {
		return RangeHandle{}, &Conflict{Code: resolution.Code, Path: resolution.Original.Path}
	}
	locator := resolution.Current
	return RangeHandle{
		Path: locator.Path, Revision: locator.DocumentRevision, ByteStart: locator.ByteStart, ByteEnd: locator.ByteEnd,
		ExpectedSHA256: locator.ContentSHA256, BeforeSHA256: locator.BeforeSHA256, AfterSHA256: locator.AfterSHA256,
		AnchorBytes: locator.AnchorBytes,
	}, nil
}
