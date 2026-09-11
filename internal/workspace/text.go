package workspace

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	defaultMaxFiles          = 2000
	defaultMaxBytes    int64 = 32 << 20
	defaultMaxDepth          = 32
	defaultMaxMatches        = 1000
	defaultAnchorBytes       = 64
)

type Limits struct {
	MaxFiles   int   `json:"max_files"`
	MaxBytes   int64 `json:"max_bytes"`
	MaxDepth   int   `json:"max_depth"`
	MaxMatches int   `json:"max_matches"`
}

type OpenOptions struct {
	Kind          Kind
	Root          string
	Files         []string
	ProviderEpoch uint64
	StateDir      string
	Limits        Limits
	Sectioner     Sectioner
	CommitFault   func(point, path string) error

	// Identity and StateSeq restore a service-owned workspace registry. They are
	// both optional for a newly opened workspace; callers restoring an existing
	// workspace must provide a valid ID and its last non-zero state sequence.
	Identity ID
	StateSeq uint64
}

type Coverage struct {
	Complete        bool     `json:"complete"`
	FilesConsidered int      `json:"files_considered"`
	FilesRead       int      `json:"files_read"`
	BytesRead       int64    `json:"bytes_read"`
	Skipped         []string `json:"skipped,omitempty"`
	Capped          bool     `json:"capped"`
	Semantic        string   `json:"semantic"`
}

type Entry struct {
	Path string     `json:"path"`
	Kind ObjectKind `json:"kind"`
	Size int64      `json:"size,omitempty"`
}

type Orientation struct {
	Workspace Identity `json:"workspace"`
	Entries   []Entry  `json:"entries"`
	Coverage  Coverage `json:"coverage"`
}

type TextRead struct {
	Workspace Identity         `json:"workspace"`
	Path      string           `json:"path"`
	Content   []byte           `json:"content"`
	Snapshot  DocumentSnapshot `json:"snapshot"`
	Coverage  Coverage         `json:"coverage"`
}

type SearchMode string

const (
	SearchLiteral SearchMode = "literal"
	SearchRegex   SearchMode = "regex"
)

type SearchRequest struct {
	Query string
	Mode  SearchMode
}

type SearchHit struct {
	Path        string        `json:"path"`
	ByteStart   int           `json:"byte_start"`
	ByteEnd     int           `json:"byte_end"`
	Line        int           `json:"line"`
	Column      int           `json:"column"`
	Match       string        `json:"match"`
	Range       RangeHandle   `json:"range"`
	MatchHandle *HandleRecord `json:"match_handle,omitempty"`
}

type SearchResult struct {
	Workspace         Identity              `json:"workspace"`
	Hits              []SearchHit           `json:"hits"`
	Coverage          Coverage              `json:"coverage"`
	Query             string                `json:"query"`
	Mode              SearchMode            `json:"mode"`
	DocumentRevisions map[string]RevisionID `json:"-"`
	SourceFiles       []string              `json:"-"`
	ResultSet         *ResultSet            `json:"result_set,omitempty"`
}

type RangeHandle struct {
	Path           string     `json:"path"`
	Revision       RevisionID `json:"revision_id"`
	ByteStart      int        `json:"byte_start"`
	ByteEnd        int        `json:"byte_end"`
	ExpectedSHA256 string     `json:"expected_sha256"`
	BeforeSHA256   string     `json:"before_sha256"`
	AfterSHA256    string     `json:"after_sha256"`
	AnchorBytes    int        `json:"anchor_bytes"`
}

type ExactDiff struct {
	Path         string `json:"path"`
	BeforeSHA256 string `json:"before_sha256"`
	AfterSHA256  string `json:"after_sha256"`
	Before       []byte `json:"before"`
	After        []byte `json:"after"`
	Patch        string `json:"patch"`
}

type TextChange struct {
	Workspace   Identity         `json:"workspace"`
	Before      DocumentSnapshot `json:"before"`
	AfterHash   string           `json:"after_sha256"`
	Range       RangeHandle      `json:"range"`
	Replacement []byte           `json:"replacement"`
	Diff        ExactDiff        `json:"diff"`
}

type Section struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	ByteStart int    `json:"byte_start"`
	ByteEnd   int    `json:"byte_end"`
}

type Sectioner interface {
	Sections(path string, content []byte) ([]Section, error)
}

type Outline struct {
	Workspace      Identity       `json:"workspace"`
	Path           string         `json:"path"`
	Sections       []Section      `json:"sections,omitempty"`
	Handles        []HandleRecord `json:"handles,omitempty"`
	Fallback       *RangeHandle   `json:"fallback_range,omitempty"`
	FallbackHandle *HandleRecord  `json:"fallback_handle,omitempty"`
	Coverage       Coverage       `json:"coverage"`
}

type EnvironmentFailure struct {
	Layer            string   `json:"layer"`
	Code             string   `json:"code"`
	Executable       string   `json:"executable,omitempty"`
	Arguments        []string `json:"arguments,omitempty"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
	Stderr           string   `json:"stderr,omitempty"`
}

type Inspection struct {
	Workspace Identity             `json:"workspace"`
	Limits    Limits               `json:"limits"`
	Allowlist []string             `json:"allowlist,omitempty"`
	Coverage  Coverage             `json:"coverage"`
	Native    map[string]bool      `json:"native"`
	Optional  map[string]string    `json:"optional"`
	Failures  []EnvironmentFailure `json:"failures,omitempty"`
}

type FileAction string

const (
	FileCreate  FileAction = "create"
	FileReplace FileAction = "replace"
	FileDelete  FileAction = "delete"
)

type RecoveryResult struct {
	Recovered []string `json:"recovered,omitempty"`
	Cleared   []string `json:"cleared,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

type journalRecord struct {
	Version    int    `json:"version"`
	Path       string `json:"path"`
	Mode       uint32 `json:"mode"`
	PreExists  bool   `json:"pre_exists"`
	PostExists bool   `json:"post_exists"`
	Preimage   string `json:"preimage"`
	Postimage  string `json:"postimage"`
}

func Open(options OpenOptions) (*Workspace, error) {
	return newWorkspace(options)
}

func OpenDocument(file string, providerEpoch uint64, stateDir string) (*Workspace, error) {
	return newWorkspace(OpenOptions{
		Kind: KindDocuments, Files: []string{file}, ProviderEpoch: providerEpoch, StateDir: stateDir,
	})
}

func newWorkspace(options OpenOptions) (*Workspace, error) {
	switch options.Kind {
	case KindProject, KindDocuments, KindTransactionSandbox:
	default:
		return nil, fmt.Errorf("unknown workspace kind %q", options.Kind)
	}
	if options.Kind == KindDocuments && len(options.Files) == 0 {
		return nil, errors.New("document workspace requires at least one allowlisted file")
	}
	root := options.Root
	if root == "" && options.Kind == KindDocuments {
		var err error
		root, err = commonRoot(options.Files)
		if err != nil {
			return nil, err
		}
	}
	if root == "" {
		return nil, errors.New("workspace root is required")
	}
	canonical, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	id := options.Identity
	if id == "" {
		id, err = newID()
		if err != nil {
			return nil, err
		}
	} else if !validID(id) {
		return nil, fmt.Errorf("invalid restored workspace ID %q", id)
	}
	stateSeq := options.StateSeq
	if stateSeq == 0 {
		stateSeq = 1
	}
	limits := normalizeLimits(options.Limits)
	workspace := &Workspace{
		identity:    Identity{ID: id, Kind: options.Kind, Root: canonical, Epoch: options.ProviderEpoch, StateSeq: stateSeq},
		documents:   make(map[string]cachedDocument),
		revisions:   make(map[RevisionID]DocumentSnapshot),
		allowlist:   make(map[string]struct{}),
		limits:      limits,
		stateDir:    options.StateDir,
		sectioner:   options.Sectioner,
		plans:       make(map[string]PlanRecord),
		activePlans: make(map[string]struct{}),
		commitFault: options.CommitFault,
	}
	for _, name := range options.Files {
		absolute := name
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(canonical, absolute)
		}
		absolute, err = filepath.Abs(absolute)
		if err != nil {
			return nil, err
		}
		absolute = filepath.Clean(absolute)
		rel, relErr := filepath.Rel(canonical, absolute)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("allowlisted document %s is outside workspace root %s", absolute, canonical)
		}
		workspace.allowlist[absolute] = struct{}{}
	}
	workspace.diagnostics, err = newDiagnosticStore(options.StateDir, id)
	if err != nil {
		return nil, err
	}
	if err := workspace.loadPlans(); err != nil {
		return nil, err
	}
	if err := workspace.recoverCommitJournals(); err != nil {
		return nil, err
	}
	return workspace, nil
}

func commonRoot(files []string) (string, error) {
	first, err := filepath.Abs(files[0])
	if err != nil {
		return "", err
	}
	root := filepath.Dir(first)
	for _, name := range files[1:] {
		absolute, absErr := filepath.Abs(name)
		if absErr != nil {
			return "", absErr
		}
		for {
			rel, relErr := filepath.Rel(root, absolute)
			if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				break
			}
			parent := filepath.Dir(root)
			if parent == root {
				return "", errors.New("documents do not share a filesystem root")
			}
			root = parent
		}
	}
	return root, nil
}

func normalizeLimits(limits Limits) Limits {
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaultMaxFiles
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaultMaxBytes
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = defaultMaxDepth
	}
	if limits.MaxMatches <= 0 {
		limits.MaxMatches = defaultMaxMatches
	}
	return limits
}

func (w *Workspace) Inspect() Inspection {
	w.mu.Lock()
	defer w.mu.Unlock()
	gitStatus := "unavailable"
	if _, err := os.Stat(filepath.Join(w.identity.Root, ".git")); err == nil {
		gitStatus = "available"
	}
	allowlist := make([]string, 0, len(w.allowlist))
	for name := range w.allowlist {
		allowlist = append(allowlist, name)
	}
	sort.Strings(allowlist)
	failures := make([]EnvironmentFailure, len(w.failures))
	for index, failure := range w.failures {
		failures[index] = failure
		failures[index].Arguments = append([]string(nil), failure.Arguments...)
	}
	return Inspection{
		Workspace: w.identity,
		Limits:    w.limits,
		Allowlist: allowlist,
		Coverage:  Coverage{Complete: true, Semantic: w.semanticCoverage()},
		Native:    map[string]bool{"walk": true, "search": true, "read": true, "guarded_edit": true, "diff": true, "recovery": true},
		Optional:  map[string]string{"git": gitStatus, "provider": "unavailable", "parser": w.parserStatus(), "lsp": "unavailable", "formatter": "unavailable", "project_commands": "unavailable"},
		Failures:  failures,
	}
}

func (w *Workspace) RecordEnvironmentFailure(failure EnvironmentFailure) {
	w.mu.Lock()
	defer w.mu.Unlock()
	failure.Layer = sanitizeText(failure.Layer, 64)
	failure.Code = sanitizeText(failure.Code, 64)
	failure.Executable = sanitizeText(failure.Executable, 256)
	failure.WorkingDirectory = sanitizeText(failure.WorkingDirectory, 512)
	failure.Stderr = sanitizeText(failure.Stderr, 2048)
	failure.Arguments = append([]string(nil), failure.Arguments...)
	for index, argument := range failure.Arguments {
		if sensitiveArgument(argument) {
			failure.Arguments[index] = "<redacted>"
		} else {
			failure.Arguments[index] = sanitizeText(argument, 256)
		}
	}
	w.failures = append(w.failures, failure)
}

func sanitizeText(value string, limit int) string {
	value = inlineSecretPattern.ReplaceAllString(value, "$1=<redacted>")
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 32 && r != 127) {
			return r
		}
		return -1
	}, value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

func sensitiveArgument(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "token=") || strings.Contains(lower, "password=") ||
		strings.Contains(lower, "secret=") || strings.Contains(lower, "authorization:")
}

func (w *Workspace) parserStatus() string {
	if w.sectioner != nil {
		return "available"
	}
	return "unavailable"
}

func (w *Workspace) semanticCoverage() string {
	if w.sectioner != nil {
		return "parser_sections"
	}
	return "text_only"
}

func (w *Workspace) Orient() (Orientation, error) {
	files, coverage, err := w.collectFiles()
	if err != nil {
		return Orientation{}, err
	}
	entries := make([]Entry, 0, len(files))
	for _, name := range files {
		disk, _, inspectErr := inspectPath(name)
		if inspectErr != nil {
			coverage.Complete = false
			coverage.Skipped = append(coverage.Skipped, displayPath(w.identity.Root, name)+": "+sanitizeText(inspectErr.Error(), 256))
			continue
		}
		entries = append(entries, Entry{Path: displayPath(w.identity.Root, name), Kind: disk.Kind, Size: disk.Size})
		coverage.FilesRead++
	}
	return Orientation{Workspace: w.Identity(), Entries: entries, Coverage: coverage}, nil
}

func (w *Workspace) collectFiles() ([]string, Coverage, error) {
	coverage := Coverage{Complete: true, Semantic: w.semanticCoverage()}
	if w.identity.Kind == KindDocuments {
		w.mu.Lock()
		candidates := make([]string, 0, len(w.allowlist))
		for name := range w.allowlist {
			candidates = append(candidates, name)
		}
		w.mu.Unlock()
		sort.Strings(candidates)
		coverage.FilesConsidered = len(candidates)
		files := make([]string, 0, len(candidates))
		for _, name := range candidates {
			if len(files) >= w.limits.MaxFiles {
				coverage.Complete = false
				coverage.Capped = true
				break
			}
			_, err := os.Lstat(name)
			if errors.Is(err, os.ErrNotExist) {
				files = append(files, name)
				continue
			}
			if err != nil {
				coverage.Complete = false
				coverage.Skipped = append(coverage.Skipped, displayPath(w.identity.Root, name)+": "+sanitizeText(err.Error(), 256))
				continue
			}
			files = append(files, name)
		}
		return files, coverage, nil
	}
	var files []string
	err := filepath.WalkDir(w.identity.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			coverage.Complete = false
			coverage.Skipped = append(coverage.Skipped, displayPath(w.identity.Root, path)+": "+sanitizeText(walkErr.Error(), 256))
			return nil
		}
		if path == w.identity.Root {
			return nil
		}
		rel, relErr := filepath.Rel(w.identity.Root, path)
		if relErr != nil {
			return relErr
		}
		depth := len(strings.Split(rel, string(filepath.Separator)))
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || depth > w.limits.MaxDepth {
				if depth > w.limits.MaxDepth {
					coverage.Complete = false
					coverage.Capped = true
					coverage.Skipped = append(coverage.Skipped, rel+": depth limit")
				}
				return filepath.SkipDir
			}
			return nil
		}
		coverage.FilesConsidered++
		if len(files) >= w.limits.MaxFiles {
			coverage.Complete = false
			coverage.Capped = true
			return errLimitReached
		}
		files = append(files, path)
		return nil
	})
	if errors.Is(err, errLimitReached) {
		err = nil
	}
	sort.Strings(files)
	return files, coverage, err
}

var (
	errLimitReached     = errors.New("workspace limit reached")
	inlineSecretPattern = regexp.MustCompile(`(?i)(token|password|secret)=[^\\s]+`)
)

func (w *Workspace) Read(path string) (TextRead, error) {
	absolute, err := w.confinedPath(path)
	if err != nil {
		return TextRead{}, err
	}
	snapshot, err := w.Refresh(absolute, ProviderLayer{})
	if err != nil {
		return TextRead{}, err
	}
	if snapshot.Disk.Kind != ObjectRegularText {
		return TextRead{}, fmt.Errorf("%s is %s, not regular text", absolute, snapshot.Disk.Kind)
	}
	if snapshot.Disk.Size > w.limits.MaxBytes {
		return TextRead{}, fmt.Errorf("%s exceeds the %d-byte read limit", absolute, w.limits.MaxBytes)
	}
	_, content, err := inspectPath(absolute)
	if err != nil {
		return TextRead{}, err
	}
	if hashBytes(content) != snapshot.ContentSHA256 {
		return TextRead{}, fmt.Errorf("document changed while reading: %s", absolute)
	}
	return TextRead{
		Workspace: snapshot.Workspace,
		Path:      displayPath(w.identity.Root, absolute),
		Content:   append([]byte(nil), content...),
		Snapshot:  snapshot,
		Coverage:  Coverage{Complete: true, FilesConsidered: 1, FilesRead: 1, BytesRead: int64(len(content)), Semantic: w.semanticCoverage()},
	}, nil
}

func (w *Workspace) Search(request SearchRequest) (SearchResult, error) {
	if request.Query == "" {
		return SearchResult{}, errors.New("search query is required")
	}
	mode := request.Mode
	if mode == "" {
		mode = SearchLiteral
	}
	var expression *regexp.Regexp
	var err error
	if mode == SearchRegex {
		expression, err = regexp.Compile(request.Query)
		if err != nil {
			return SearchResult{}, fmt.Errorf("invalid regular expression: %w", err)
		}
	} else if mode != SearchLiteral {
		return SearchResult{}, fmt.Errorf("unknown search mode %q", mode)
	}
	files, coverage, err := w.collectFiles()
	if err != nil {
		return SearchResult{}, err
	}
	coverage.BytesRead = 0
	revisions := make(map[string]RevisionID)
	var hits []SearchHit
	for _, name := range files {
		if len(hits) >= w.limits.MaxMatches {
			coverage.Complete = false
			coverage.Capped = true
			break
		}
		disk, _, inspectErr := inspectPath(name)
		if inspectErr == nil && disk.Kind == ObjectBinary {
			continue
		}
		read, readErr := w.Read(name)
		if readErr != nil {
			coverage.Complete = false
			coverage.Skipped = append(coverage.Skipped, displayPath(w.identity.Root, name)+": "+sanitizeText(readErr.Error(), 256))
			continue
		}
		if coverage.BytesRead+int64(len(read.Content)) > w.limits.MaxBytes {
			coverage.Complete = false
			coverage.Capped = true
			coverage.Skipped = append(coverage.Skipped, displayPath(w.identity.Root, name)+": byte limit")
			continue
		}
		coverage.FilesRead++
		coverage.BytesRead += int64(len(read.Content))
		revisions[read.Path] = read.Snapshot.Revision
		ranges := matchRanges(read.Content, []byte(request.Query), expression)
		for _, span := range ranges {
			if span[0] == span[1] {
				continue
			}
			if len(hits) >= w.limits.MaxMatches {
				coverage.Complete = false
				coverage.Capped = true
				break
			}
			handle, handleErr := rangeHandle(read, span[0], span[1], defaultAnchorBytes)
			if handleErr != nil {
				return SearchResult{}, handleErr
			}
			line, column := bytePosition(read.Content, span[0])
			record, registerErr := w.RegisterRangeHandle(handle, HandleMatch,
				fmt.Sprintf("%s:%d:%d exact match", displayPath(w.identity.Root, read.Path), line, column))
			if registerErr != nil {
				return SearchResult{}, registerErr
			}
			hits = append(hits, SearchHit{
				Path: read.Path, ByteStart: span[0], ByteEnd: span[1], Line: line, Column: column,
				Match: string(read.Content[span[0]:span[1]]), Range: handle, MatchHandle: &record,
			})
		}
	}
	result := SearchResult{
		Workspace: w.Identity(), Hits: hits, Coverage: coverage, Query: request.Query, Mode: mode,
		DocumentRevisions: revisions, SourceFiles: append([]string(nil), files...),
	}
	set, err := w.FreezeSearch(result)
	if err != nil {
		return SearchResult{}, err
	}
	result.ResultSet = &set
	return result, nil
}

func matchRanges(content, literal []byte, expression *regexp.Regexp) [][2]int {
	if expression != nil {
		raw := expression.FindAllIndex(content, -1)
		result := make([][2]int, 0, len(raw))
		for _, span := range raw {
			result = append(result, [2]int{span[0], span[1]})
		}
		return result
	}
	var result [][2]int
	for offset := 0; offset <= len(content)-len(literal); {
		index := bytes.Index(content[offset:], literal)
		if index < 0 {
			break
		}
		start := offset + index
		result = append(result, [2]int{start, start + len(literal)})
		offset = start + len(literal)
	}
	return result
}

func bytePosition(content []byte, offset int) (int, int) {
	line := 1
	column := 1
	lineStart := 0
	for index, value := range content[:offset] {
		if value == '\n' {
			line++
			lineStart = index + 1
		}
	}
	column = utf8.RuneCount(content[lineStart:offset]) + 1
	return line, column
}

func (w *Workspace) NewRange(path string, start, end int) (RangeHandle, error) {
	read, err := w.Read(path)
	if err != nil {
		return RangeHandle{}, err
	}
	return rangeHandle(read, start, end, defaultAnchorBytes)
}

func rangeHandle(read TextRead, start, end, anchorBytes int) (RangeHandle, error) {
	if start < 0 || end < start || end > len(read.Content) {
		return RangeHandle{}, fmt.Errorf("invalid byte range %d:%d for %s", start, end, read.Path)
	}
	beforeStart := start - anchorBytes
	if beforeStart < 0 {
		beforeStart = 0
	}
	afterEnd := end + anchorBytes
	if afterEnd > len(read.Content) {
		afterEnd = len(read.Content)
	}
	return RangeHandle{
		Path: read.Path, Revision: read.Snapshot.Revision, ByteStart: start, ByteEnd: end,
		ExpectedSHA256: hashBytes(read.Content[start:end]),
		BeforeSHA256:   hashBytes(read.Content[beforeStart:start]),
		AfterSHA256:    hashBytes(read.Content[end:afterEnd]),
		AnchorBytes:    anchorBytes,
	}, nil
}

func (w *Workspace) Outline(path string) (Outline, error) {
	read, err := w.Read(path)
	if err != nil {
		return Outline{}, err
	}
	coverage := read.Coverage
	if w.sectioner == nil {
		fallback, handleErr := rangeHandle(read, 0, len(read.Content), defaultAnchorBytes)
		if handleErr != nil {
			return Outline{}, handleErr
		}
		record, registerErr := w.RegisterRangeHandle(fallback, HandleRange, fmt.Sprintf("%s whole document", read.Path))
		if registerErr != nil {
			return Outline{}, registerErr
		}
		coverage.Semantic = "text_only"
		return Outline{Workspace: read.Workspace, Path: read.Path, Fallback: &fallback, FallbackHandle: &record, Coverage: coverage}, nil
	}
	sections, err := w.sectioner.Sections(read.Path, append([]byte(nil), read.Content...))
	if err != nil {
		coverage.Complete = false
		coverage.Semantic = "text_only"
		fallback, handleErr := rangeHandle(read, 0, len(read.Content), defaultAnchorBytes)
		if handleErr != nil {
			return Outline{}, handleErr
		}
		record, registerErr := w.RegisterRangeHandle(fallback, HandleRange, fmt.Sprintf("%s whole document", read.Path))
		if registerErr != nil {
			return Outline{}, registerErr
		}
		return Outline{Workspace: read.Workspace, Path: read.Path, Fallback: &fallback, FallbackHandle: &record, Coverage: coverage}, nil
	}
	handles := make([]HandleRecord, 0, len(sections))
	for _, section := range sections {
		record, registerErr := w.registerSymbol(read, section)
		if registerErr != nil {
			return Outline{}, registerErr
		}
		handles = append(handles, record)
	}
	coverage.Semantic = "parser_sections"
	return Outline{Workspace: read.Workspace, Path: read.Path, Sections: sections, Handles: handles, Coverage: coverage}, nil
}

func (w *Workspace) PreviewReplace(workspaceID ID, handle RangeHandle, replacement []byte) (TextChange, error) {
	current, content, err := w.validateRange(workspaceID, handle)
	if err != nil {
		return TextChange{}, err
	}
	after := make([]byte, 0, len(content)-(handle.ByteEnd-handle.ByteStart)+len(replacement))
	after = append(after, content[:handle.ByteStart]...)
	after = append(after, replacement...)
	after = append(after, content[handle.ByteEnd:]...)
	diff := exactDiff(handle.Path, content, after, handle.ByteStart, handle.ByteEnd, replacement)
	return TextChange{
		Workspace: current.Workspace, Before: current, AfterHash: hashBytes(after), Range: handle,
		Replacement: append([]byte(nil), replacement...), Diff: diff,
	}, nil
}

func (w *Workspace) validateRange(workspaceID ID, handle RangeHandle) (DocumentSnapshot, []byte, error) {
	current, err := w.ValidateMutation(workspaceID, handle.Path, handle.Revision, ProviderLayer{})
	if err != nil {
		return current, nil, err
	}
	absolute, err := w.confinedPath(handle.Path)
	if err != nil {
		return current, nil, err
	}
	disk, content, err := inspectPath(absolute)
	if err != nil {
		return current, nil, err
	}
	if disk.Kind != ObjectRegularText || handle.ByteStart < 0 || handle.ByteEnd < handle.ByteStart || handle.ByteEnd > len(content) {
		return current, nil, &Conflict{Code: ConflictDocumentChanged, Path: handle.Path, Expected: handle.Revision, Current: current.Revision}
	}
	anchorBytes := handle.AnchorBytes
	beforeStart := handle.ByteStart - anchorBytes
	if beforeStart < 0 {
		beforeStart = 0
	}
	afterEnd := handle.ByteEnd + anchorBytes
	if afterEnd > len(content) {
		afterEnd = len(content)
	}
	if hashBytes(content[handle.ByteStart:handle.ByteEnd]) != handle.ExpectedSHA256 ||
		hashBytes(content[beforeStart:handle.ByteStart]) != handle.BeforeSHA256 ||
		hashBytes(content[handle.ByteEnd:afterEnd]) != handle.AfterSHA256 {
		return current, nil, &Conflict{Code: ConflictDocumentChanged, Path: handle.Path, Expected: handle.Revision, Current: current.Revision}
	}
	return current, content, nil
}

func exactDiff(path string, before, after []byte, start, end int, replacement []byte) ExactDiff {
	return ExactDiff{
		Path: path, BeforeSHA256: hashBytes(before), AfterSHA256: hashBytes(after),
		Before: append([]byte(nil), before...), After: append([]byte(nil), after...),
		Patch: fmt.Sprintf("--- %s\n+++ %s\n@@ bytes %d:%d @@\n-%q\n+%q\n", path, path, start, end, before[start:end], replacement),
	}
}

func (w *Workspace) ApplyReplace(workspaceID ID, handle RangeHandle, replacement []byte) (TextChange, DocumentSnapshot, error) {
	change, err := w.PreviewReplace(workspaceID, handle, replacement)
	if err != nil {
		return TextChange{}, DocumentSnapshot{}, err
	}
	if w.stateDir == "" {
		return TextChange{}, DocumentSnapshot{}, errors.New("native mutation recovery state directory is required")
	}
	absolute, err := w.confinedPath(handle.Path)
	if err != nil {
		return TextChange{}, DocumentSnapshot{}, err
	}
	if err := w.mutateFile(absolute, true, change.Diff.Before, true, change.Diff.After, change.Before.Disk.Mode); err != nil {
		return TextChange{}, DocumentSnapshot{}, err
	}
	after, err := w.Refresh(absolute, ProviderLayer{})
	return change, after, err
}

func (w *Workspace) ApplyFile(workspaceID ID, path string, expected RevisionID, action FileAction, content []byte) (ExactDiff, DocumentSnapshot, error) {
	current, err := w.ValidateMutation(workspaceID, path, expected, ProviderLayer{})
	if err != nil {
		return ExactDiff{}, current, err
	}
	absolute, err := w.confinedPath(path)
	if err != nil {
		return ExactDiff{}, current, err
	}
	_, before, err := inspectPath(absolute)
	if err != nil {
		return ExactDiff{}, current, err
	}
	preExists := current.Disk.Kind != ObjectMissing
	postExists := action != FileDelete
	switch action {
	case FileCreate:
		if preExists {
			return ExactDiff{}, current, fmt.Errorf("create target already exists: %s", path)
		}
	case FileReplace:
		if current.Disk.Kind != ObjectRegularText {
			return ExactDiff{}, current, fmt.Errorf("replace target is %s, not regular text", current.Disk.Kind)
		}
	case FileDelete:
		if !preExists {
			return ExactDiff{}, current, fmt.Errorf("delete target does not exist: %s", path)
		}
	default:
		return ExactDiff{}, current, fmt.Errorf("unknown file action %q", action)
	}
	if w.stateDir == "" {
		return ExactDiff{}, current, errors.New("native mutation recovery state directory is required")
	}
	afterBytes := content
	if !postExists {
		afterBytes = nil
	}
	mode := current.Disk.Mode
	if !preExists {
		mode = uint32(0o644)
	}
	diff := exactDiff(displayPath(w.identity.Root, absolute), before, afterBytes, 0, len(before), afterBytes)
	if err := w.mutateFile(absolute, preExists, before, postExists, afterBytes, mode); err != nil {
		return ExactDiff{}, current, err
	}
	after, err := w.Refresh(absolute, ProviderLayer{})
	return diff, after, err
}

func (w *Workspace) mutateFile(path string, preExists bool, preimage []byte, postExists bool, postimage []byte, mode uint32) error {
	if err := os.MkdirAll(w.stateDir, 0o700); err != nil {
		return fmt.Errorf("create recovery state: %w", err)
	}
	record := journalRecord{
		Version: 1, Path: path, Mode: mode, PreExists: preExists, PostExists: postExists,
		Preimage: base64.StdEncoding.EncodeToString(preimage), Postimage: base64.StdEncoding.EncodeToString(postimage),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	journal, err := os.CreateTemp(w.stateDir, "native-*.json")
	if err != nil {
		return fmt.Errorf("create recovery journal: %w", err)
	}
	journalName := journal.Name()
	cleanupJournal := false
	defer func() {
		_ = journal.Close()
		if cleanupJournal {
			_ = os.Remove(journalName)
		}
	}()
	if err := journal.Chmod(0o600); err != nil {
		return err
	}
	if _, err := journal.Write(encoded); err != nil {
		return err
	}
	if err := journal.Sync(); err != nil {
		return err
	}
	if err := journal.Close(); err != nil {
		return err
	}
	if err := ensureCurrent(path, preExists, preimage); err != nil {
		return err
	}
	if postExists {
		if err := atomicWrite(path, postimage, fs.FileMode(mode)); err != nil {
			return err
		}
	} else if err := os.Remove(path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	cleanupJournal = true
	if err := os.Remove(journalName); err != nil {
		cleanupJournal = false
		return fmt.Errorf("remove completed recovery journal: %w", err)
	}
	return syncDirectory(w.stateDir)
}

func ensureCurrent(path string, expectedExists bool, expected []byte) error {
	disk, content, err := inspectPath(path)
	if err != nil {
		return err
	}
	exists := disk.Kind != ObjectMissing
	if exists != expectedExists || (exists && (disk.Kind != ObjectRegularText || !bytes.Equal(content, expected))) {
		return fmt.Errorf("commit precondition changed for %s", path)
	}
	return nil
}

func atomicWrite(path string, content []byte, mode fs.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".huyang-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	remove := true
	defer func() {
		_ = temp.Close()
		if remove {
			_ = os.Remove(name)
		}
	}()
	if _, err := temp.Write(content); err != nil {
		return err
	}
	if err := temp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	remove = false
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (w *Workspace) Recover() (RecoveryResult, error) {
	var result RecoveryResult
	if w.stateDir == "" {
		return result, errors.New("native mutation recovery state directory is required")
	}
	entries, err := os.ReadDir(w.stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "native-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		journalPath := filepath.Join(w.stateDir, entry.Name())
		data, readErr := os.ReadFile(journalPath)
		if readErr != nil {
			return result, readErr
		}
		var record journalRecord
		if jsonErr := json.Unmarshal(data, &record); jsonErr != nil || record.Version != 1 {
			return result, fmt.Errorf("invalid recovery journal %s", entry.Name())
		}
		target, confineErr := w.confinedPath(record.Path)
		if confineErr != nil {
			return result, fmt.Errorf("recovery journal target: %w", confineErr)
		}
		preimage, preErr := base64.StdEncoding.DecodeString(record.Preimage)
		postimage, postErr := base64.StdEncoding.DecodeString(record.Postimage)
		if preErr != nil || postErr != nil {
			return result, fmt.Errorf("invalid recovery journal payload %s", entry.Name())
		}
		if stateMatches(target, record.PreExists, preimage) {
			if removeErr := os.Remove(journalPath); removeErr != nil {
				return result, removeErr
			}
			result.Cleared = append(result.Cleared, target)
			continue
		}
		if !stateMatches(target, record.PostExists, postimage) {
			result.Conflicts = append(result.Conflicts, target)
			continue
		}
		if record.PreExists {
			if writeErr := atomicWrite(target, preimage, fs.FileMode(record.Mode)); writeErr != nil {
				return result, writeErr
			}
		} else if removeErr := os.Remove(target); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return result, removeErr
		}
		if syncErr := syncDirectory(filepath.Dir(target)); syncErr != nil {
			return result, syncErr
		}
		if removeErr := os.Remove(journalPath); removeErr != nil {
			return result, removeErr
		}
		result.Recovered = append(result.Recovered, target)
	}
	sort.Strings(result.Recovered)
	sort.Strings(result.Cleared)
	sort.Strings(result.Conflicts)
	return result, nil
}

func stateMatches(path string, exists bool, content []byte) bool {
	disk, current, err := inspectPath(path)
	if err != nil {
		return false
	}
	currentExists := disk.Kind != ObjectMissing
	if currentExists != exists {
		return false
	}
	return !exists || (disk.Kind == ObjectRegularText && bytes.Equal(current, content))
}

func displayPath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return path
}
