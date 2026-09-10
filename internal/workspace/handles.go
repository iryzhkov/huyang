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

type handleStore struct {
	mu      sync.RWMutex
	ttl     time.Duration
	handles map[HandleID]HandleRecord
	sets    map[ResultSetID]ResultSet
}

func newHandleStore() *handleStore {
	return &handleStore{
		ttl: defaultHandleTTL, handles: make(map[HandleID]HandleRecord), sets: make(map[ResultSetID]ResultSet),
	}
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
	store.handles[record.Handle] = record
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
	store.handles[record.Handle] = record
	return record, nil
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
			coverage.Complete = false
			coverage.Skipped = append(coverage.Skipped, displayPath(w.identity.Root, path)+": "+sanitizeText(readErr.Error(), 256))
			continue
		}
		sections, sectionErr := w.sectioner.Sections(read.Path, read.Content)
		if sectionErr != nil {
			coverage.Complete = false
			coverage.Skipped = append(coverage.Skipped, displayPath(w.identity.Root, path)+": "+sanitizeText(sectionErr.Error(), 256))
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

func (w *Workspace) InspectHandle(id HandleID) (HandleRecord, error) {
	store := w.handleRegistry()
	store.mu.RLock()
	record, ok := store.handles[id]
	store.mu.RUnlock()
	if !ok {
		return HandleRecord{}, errors.New("unknown handle")
	}
	if !record.ExpiresAt.After(time.Now()) {
		store.mu.Lock()
		delete(store.handles, id)
		store.mu.Unlock()
		return HandleRecord{}, errors.New("handle expired")
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
	if read.Snapshot.Disk.Device != original.Device || read.Snapshot.Disk.Inode != original.Inode {
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
	code := ConflictTargetDeleted
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
	files, _, err := w.collectFiles()
	if err != nil {
		return HandleResolution{}, err
	}
	var sameName, fingerprint []HandleCandidate
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
			if section.Name != original.NamePath || section.Kind != original.Kind ||
				section.ByteStart < 0 || section.ByteEnd > len(read.Content) || section.ByteEnd <= section.ByteStart {
				continue
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
				continue
			}
			locator.BeforeSHA256 = currentRange.BeforeSHA256
			locator.AfterSHA256 = currentRange.AfterSHA256
			line, column := bytePosition(read.Content, section.ByteStart)
			candidate := HandleCandidate{Display: fmt.Sprintf("%s:%d:%d %s %s", read.Path, line, column, section.Name, section.Kind), Locator: locator}
			sameName = append(sameName, candidate)
			if locator.NodeSHA256 == original.NodeSHA256 && locator.SignatureSHA256 == original.SignatureSHA256 {
				fingerprint = append(fingerprint, candidate)
			}
		}
	}
	if len(fingerprint) == 1 {
		current := fingerprint[0].Locator
		if current.Path == original.Path && current.ByteStart == original.ByteStart &&
			current.DocumentRevision == original.DocumentRevision {
			return HandleResolution{Status: ResolutionExact, Handle: record.Handle, Original: original, Current: &current}, nil
		}
		if current.Path == original.Path && (current.Device != original.Device || current.Inode != original.Inode) {
			return HandleResolution{Status: ResolutionConflicted, Code: ConflictTargetDeleted, Handle: record.Handle, Original: original, Candidates: fingerprint}, nil
		}
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
		StateSeq: identity.StateSeq, Matches: append([]SearchHit(nil), result.Hits...), MatchCount: len(result.Hits),
		FileCount: len(files), Complete: result.Coverage.Complete, NonOverlapping: nonOverlapping(result.Hits),
		Retained: len(result.Hits), Coverage: result.Coverage, Query: result.Query, Mode: result.Mode,
		DocumentRevisions: revisions, SourceFiles: append([]string(nil), result.SourceFiles...),
	}
	set.AllMatchesEligible = set.Complete && set.NonOverlapping
	store := w.handleRegistry()
	store.mu.Lock()
	defer store.mu.Unlock()
	set.ExpiresAt = time.Now().Add(store.ttl)
	store.sets[set.Handle] = set
	return set, nil
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
	return append([]SearchHit(nil), set.Matches...), nil
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
	hits := make([]SearchHit, 0, len(parent.Matches))
	for _, hit := range parent.Matches {
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
	store.sets[child.Handle] = child
	store.mu.Unlock()
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
