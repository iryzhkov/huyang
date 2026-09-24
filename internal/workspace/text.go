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
	// SkippedCount is how many documents were skipped, which is what the
	// bounded Skipped list is a sample of.
	SkippedCount int `json:"skipped_count,omitempty"`
	// SymlinksSkipped counts the symlinks that were passed over. They are
	// not skipped documents: a symlink has no text of its own, and its
	// target is either listed separately or outside the workspace, so it
	// does not make an answer incomplete.
	SymlinksSkipped int    `json:"symlinks_skipped,omitempty"`
	Capped          bool   `json:"capped"`
	Semantic        string `json:"semantic"`
}

// maxCoverageSkipped bounds the paths one coverage block names. A repository
// of binaries or unreadable files would otherwise answer a list as long as
// the tree; the count next to it stays exact whatever the list holds.
const maxCoverageSkipped = 20

// noteSkipped records one document the answer does not cover. Coverage stops
// being complete the moment anything is skipped, which is the whole point of
// the field: a reply that looked at less than it was asked about must not
// read like one that looked at everything.
func (c *Coverage) noteSkipped(path, reason string) {
	c.Complete = false
	c.SkippedCount++
	if len(c.Skipped) < maxCoverageSkipped {
		c.Skipped = append(c.Skipped, path+": "+reason)
	}
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
	// Paths scopes the search before it reads anything: a pattern is a path
	// substring, or a glob against the whole path or the base name. Scoping
	// has to happen here rather than over the answer, because a filter over
	// the answer runs after the match cap has already been spent on files
	// the caller excluded, and leaves those files inside the frozen result
	// set that an all-match replacement then rewrites.
	Paths []string
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
	// MatchTruncated marks a hit stored inside a frozen result set whose
	// matched text exceeded maxRetainedMatchBytes and was dropped; the
	// locator is intact and hydrateHits restores the text on demand.
	MatchTruncated bool `json:"match_truncated,omitempty"`
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
	// MatchLimit is how many matches this search collects before it stops.
	// When coverage says the search was capped, the hit count is this bound
	// rather than the number of matches in the workspace, and the caller
	// needs the bound to tell the two apart.
	MatchLimit int `json:"match_limit"`
	// Scope is the path scope the search ran under, kept so that a frozen
	// result set is revalidated against the files it was taken from rather
	// than against the whole tree.
	Scope []string `json:"scope,omitempty"`
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
	Before       []byte `json:"before,omitempty"`
	After        []byte `json:"after,omitempty"`
	Patch        string `json:"patch,omitempty"`
	// The sizes of what a summarised diff no longer carries. A reply that
	// drops the bodies says how big they were instead of leaving three
	// fields that read as "nothing changed".
	BeforeBytes int `json:"before_bytes,omitempty"`
	AfterBytes  int `json:"after_bytes,omitempty"`
	PatchBytes  int `json:"patch_bytes,omitempty"`
}

// Summarize drops the bodies of a diff and records their sizes, for a reply
// that has to stay bounded. The hashes are untouched, so what the diff
// claims about the change is still independently checkable.
func (d *ExactDiff) Summarize() {
	d.BeforeBytes, d.AfterBytes, d.PatchBytes = len(d.Before), len(d.After), len(d.Patch)
	d.Before, d.After, d.Patch = nil, nil, ""
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
	canonical, err := workspaceRoot(options)
	if err != nil {
		return nil, err
	}
	id, err := workspaceIdentity(options.Identity)
	if err != nil {
		return nil, err
	}
	stateSeq := options.StateSeq
	if stateSeq == 0 {
		stateSeq = 1
	}
	limits := normalizeLimits(options.Limits)
	workspace := &Workspace{
		identity:        Identity{ID: id, Kind: options.Kind, Root: canonical, Epoch: options.ProviderEpoch, StateSeq: stateSeq},
		documents:       make(map[string]cachedDocument),
		revisions:       make(map[RevisionID]DocumentSnapshot),
		revisionHistory: make(map[string][]RevisionID),
		knownPaths:      make(map[string]struct{}),
		allowlist:       make(map[string]struct{}),
		limits:          limits,
		stateDir:        options.StateDir,
		sectioner:       options.Sectioner,
		plans:           make(map[string]PlanRecord),
		activePlans:     make(map[string]struct{}),
		commitFault:     options.CommitFault,
	}
	for _, name := range options.Files {
		absolute, err := allowlistedPath(canonical, name)
		if err != nil {
			return nil, err
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

// workspaceRoot validates the kind and returns the canonical absolute root, derived from
// the allowlisted files for a document workspace that named none.
func workspaceRoot(options OpenOptions) (string, error) {
	switch options.Kind {
	case KindProject, KindDocuments, KindTransactionSandbox:
	default:
		return "", fmt.Errorf("unknown workspace kind %q", options.Kind)
	}
	if options.Kind == KindDocuments && len(options.Files) == 0 {
		return "", errors.New("document workspace requires at least one allowlisted file")
	}
	root := options.Root
	if root == "" && options.Kind == KindDocuments {
		var err error
		root, err = commonRoot(options.Files)
		if err != nil {
			return "", err
		}
	}
	if root == "" {
		return "", errors.New("workspace root is required")
	}
	canonical, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	return canonical, nil
}

// workspaceIdentity returns a fresh ID, or validates the one a restored workspace names.
func workspaceIdentity(restored ID) (ID, error) {
	if restored == "" {
		return newID()
	}
	if !validID(restored) {
		return "", fmt.Errorf("invalid restored workspace ID %q", restored)
	}
	return restored, nil
}

// allowlistedPath resolves one allowlisted document to a clean absolute path inside root.
func allowlistedPath(root, name string) (string, error) {
	absolute := name
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, absolute)
	}
	absolute, err := filepath.Abs(absolute)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	rel, relErr := filepath.Rel(root, absolute)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("allowlisted document %s is outside workspace root %s", absolute, root)
	}
	return absolute, nil
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
			coverage.noteSkipped(displayPath(w.identity.Root, name), sanitizeText(inspectErr.Error(), 256))
			continue
		}
		entries = append(entries, Entry{Path: displayPath(w.identity.Root, name), Kind: disk.Kind, Size: disk.Size})
		coverage.FilesRead++
	}
	return Orientation{Workspace: w.Identity(), Entries: entries, Coverage: coverage}, nil
}

// Paths lists the workspace's documents as workspace-relative paths, with the
// coverage of the listing. Nothing is read or stat'ed beyond what the listing
// itself needs, so it is cheap enough to answer a read of a missing path.
func (w *Workspace) Paths() ([]string, Coverage, error) {
	files, coverage, err := w.collectFiles()
	if err != nil {
		return nil, coverage, err
	}
	paths := make([]string, len(files))
	for index, name := range files {
		paths[index] = displayPath(w.identity.Root, name)
	}
	return paths, coverage, nil
}

func (w *Workspace) collectFiles() ([]string, Coverage, error) {
	return w.collectScopedFiles(nil)
}

// collectScopedFiles lists the documents inside a path scope, in path order.
// The scope is applied before the file cap, so a scoped listing is capped only
// when a file inside the scope was dropped: a scope that names one file in a
// tree larger than the cap still lists that file and says it is complete. The
// coverage describes the scope alone; a file outside it is not a gap.
func (w *Workspace) collectScopedFiles(patterns []string) ([]string, Coverage, error) {
	coverage := Coverage{Complete: true, Semantic: w.semanticCoverage()}
	if w.identity.Kind == KindDocuments {
		files := w.collectAllowlisted(patterns, &coverage)
		return files, scopeCoverage(coverage, patterns), nil
	}
	// The inventory honours .gitignore through the sanitized Git runner so
	// ambient GIT_DIR, GIT_CONFIG_* injection and core.fsmonitor hooks never
	// reach the native text path. A truncated listing is not trusted; the
	// bounded walk below takes over instead.
	if output, truncated, err := runGitAt(w.identity.Root, nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard"); err == nil && !truncated {
		files := w.collectListedFiles(output, patterns, &coverage)
		return files, scopeCoverage(coverage, patterns), nil
	}
	files, err := w.walkFiles(patterns, &coverage)
	return files, scopeCoverage(coverage, patterns), err
}

// scopeCoverage drops the skipped entries a scope excludes. A walk notes an
// unreadable directory or a depth limit before it can know whether anything
// inside the scope lies below it; an entry whose path is outside the scope is
// not a gap in a search that was told not to look there.
func scopeCoverage(coverage Coverage, patterns []string) Coverage {
	if len(patterns) == 0 {
		return coverage
	}
	scoped := coverage
	scoped.Skipped, scoped.SkippedCount = nil, 0
	for _, skipped := range coverage.Skipped {
		path, _, found := strings.Cut(skipped, ": ")
		if found && !MatchesPathScope(path, patterns) {
			continue
		}
		scoped.Skipped = append(scoped.Skipped, skipped)
		scoped.SkippedCount++
	}
	// Entries past the bounded sample were counted but not named, so they
	// cannot be placed inside or outside the scope; they stay counted.
	scoped.SkippedCount += coverage.SkippedCount - len(coverage.Skipped)
	scoped.Complete = scoped.SkippedCount == 0 && !scoped.Capped
	return scoped
}

// collectAllowlisted returns the allowlisted documents in path order, keeping a missing
// document (it may be created) and skipping one that cannot be stat'ed.
func (w *Workspace) collectAllowlisted(patterns []string, coverage *Coverage) []string {
	w.mu.Lock()
	candidates := make([]string, 0, len(w.allowlist))
	for name := range w.allowlist {
		candidates = append(candidates, name)
	}
	w.mu.Unlock()
	sort.Strings(candidates)
	if len(patterns) > 0 {
		scoped := candidates[:0]
		for _, name := range candidates {
			if MatchesPathScope(displayPath(w.identity.Root, name), patterns) {
				scoped = append(scoped, name)
			}
		}
		candidates = scoped
	}
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
			coverage.noteSkipped(displayPath(w.identity.Root, name), sanitizeText(err.Error(), 256))
			continue
		}
		files = append(files, name)
	}
	return files
}

// collectListedFiles turns a NUL-separated git ls-files listing into bounded absolute
// paths, keeping only those inside the scope.
func (w *Workspace) collectListedFiles(output string, patterns []string, coverage *Coverage) []string {
	var files []string
	previous := ""
	for _, relative := range strings.Split(output, "\x00") {
		// A path with a merge conflict is listed once per index stage, one
		// after another; it is still one file on disk.
		if relative == "" || relative == previous {
			continue
		}
		previous = relative
		if len(patterns) > 0 && !MatchesPathScope(relative, patterns) {
			continue
		}
		coverage.FilesConsidered++
		depth := len(strings.Split(filepath.Clean(relative), string(filepath.Separator)))
		if depth > w.limits.MaxDepth {
			coverage.Capped = true
			coverage.noteSkipped(relative, "depth limit")
			continue
		}
		if len(files) >= w.limits.MaxFiles {
			coverage.Complete = false
			coverage.Capped = true
			break
		}
		files = append(files, filepath.Join(w.identity.Root, filepath.FromSlash(relative)))
	}
	sort.Strings(files)
	return files
}

// walkFiles is the bounded filesystem walk used when Git cannot list the tree.
func (w *Workspace) walkFiles(patterns []string, coverage *Coverage) ([]string, error) {
	var files []string
	err := filepath.WalkDir(w.identity.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			coverage.noteSkipped(displayPath(w.identity.Root, path), sanitizeText(walkErr.Error(), 256))
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
					coverage.Capped = true
					coverage.noteSkipped(rel, "depth limit")
				}
				return filepath.SkipDir
			}
			return nil
		}
		if len(patterns) > 0 && !MatchesPathScope(filepath.ToSlash(rel), patterns) {
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
	return files, err
}

// compileLineRegex compiles a search expression the way grep reads one: ^ and
// $ match at every line boundary, not only at the ends of the file, because
// a search reports lines and a caller anchoring a pattern means the line.
func compileLineRegex(expression string) (*regexp.Regexp, error) {
	return regexp.Compile("(?m)" + expression)
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
	if snapshot.Disk.Kind == ObjectMissing {
		return TextRead{}, Coded(CodeDocumentNotFound, &DocumentNotFoundError{Path: displayPath(w.identity.Root, absolute)})
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
	mode, expression, err := searchExpression(request)
	if err != nil {
		return SearchResult{}, err
	}
	files, coverage, err := w.collectScopedFiles(request.Paths)
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
		read, ok := w.readSearchable(name, &coverage)
		if !ok {
			continue
		}
		revisions[read.Path] = read.Snapshot.Revision
		hits, err = w.matchHits(read, request.Query, expression, hits, &coverage)
		if err != nil {
			return SearchResult{}, err
		}
	}
	result := SearchResult{
		Workspace: w.Identity(), Hits: hits, Coverage: coverage, Query: request.Query, Mode: mode,
		DocumentRevisions: revisions, SourceFiles: append([]string(nil), files...),
		MatchLimit: w.limits.MaxMatches, Scope: append([]string(nil), request.Paths...),
	}
	set, err := w.FreezeSearch(result)
	if err != nil {
		return SearchResult{}, err
	}
	result.ResultSet = &set
	return result, nil
}

// MatchesPathScope reports whether a workspace-relative path is inside a
// scope: a pattern matches as a path substring, as a glob over the whole
// path, or as a glob over the base name.
func MatchesPathScope(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(path, pattern) {
			return true
		}
		if ok, _ := filepath.Match(pattern, path); ok {
			return true
		}
		if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok {
			return true
		}
	}
	return false
}

// searchExpression resolves the search mode and compiles the regular expression for it.
func searchExpression(request SearchRequest) (SearchMode, *regexp.Regexp, error) {
	mode := request.Mode
	if mode == "" {
		mode = SearchLiteral
	}
	if mode == SearchRegex {
		expression, err := compileLineRegex(request.Query)
		if err != nil {
			return mode, nil, fmt.Errorf("invalid regular expression: %w", err)
		}
		return mode, expression, nil
	}
	if mode != SearchLiteral {
		return mode, nil, fmt.Errorf("unknown search mode %q", mode)
	}
	return mode, nil, nil
}

// readSearchable reads one candidate document for search, recording in coverage why a
// binary, unreadable or over-budget document was skipped.
func (w *Workspace) readSearchable(name string, coverage *Coverage) (TextRead, bool) {
	disk, _, inspectErr := inspectPath(name)
	if inspectErr == nil && disk.Kind == ObjectSymlink {
		// A symlink holds a path, not text of its own. When it points inside
		// the workspace the target is listed and searched under its own name,
		// and when it points outside it, confinement is the reason it is not
		// searched rather than a gap in this answer. Counting it as an
		// unreadable document made every search of a checkout with scaffolding
		// symlinks in it incomplete, and an incomplete search cannot back an
		// all-match replacement, so two of them vetoed the feature repository
		// wide.
		coverage.SymlinksSkipped++
		return TextRead{}, false
	}
	if inspectErr == nil && disk.Kind == ObjectBinary {
		// A file the search never opened is not a file with no matches in it,
		// and nothing else in the reply tells the two apart: a search that
		// skipped a binary looked exactly like one that read the whole tree.
		coverage.noteSkipped(displayPath(w.identity.Root, name), "binary, not searched")
		return TextRead{}, false
	}
	read, readErr := w.Read(name)
	if readErr != nil {
		coverage.noteSkipped(displayPath(w.identity.Root, name), sanitizeText(readErr.Error(), 256))
		return TextRead{}, false
	}
	if coverage.BytesRead+int64(len(read.Content)) > w.limits.MaxBytes {
		coverage.Capped = true
		coverage.noteSkipped(displayPath(w.identity.Root, name), "byte limit")
		return TextRead{}, false
	}
	coverage.FilesRead++
	coverage.BytesRead += int64(len(read.Content))
	return read, true
}

// matchHits registers a match handle for every non-empty match in read, stopping at the
// match cap.
func (w *Workspace) matchHits(read TextRead, query string, expression *regexp.Regexp, hits []SearchHit, coverage *Coverage) ([]SearchHit, error) {
	for _, span := range matchRanges(read.Content, []byte(query), expression) {
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
			return nil, handleErr
		}
		line, column := bytePosition(read.Content, span[0])
		record, registerErr := w.RegisterRangeHandle(handle, HandleMatch,
			fmt.Sprintf("%s:%d:%d exact match", displayPath(w.identity.Root, read.Path), line, column))
		if registerErr != nil {
			return nil, registerErr
		}
		hits = append(hits, SearchHit{
			Path: read.Path, ByteStart: span[0], ByteEnd: span[1], Line: line, Column: column,
			Match: string(read.Content[span[0]:span[1]]), Range: handle, MatchHandle: &record,
		})
	}
	return hits, nil
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
	coverage.Semantic = "parser_sections"
	if len(sections) == 0 {
		// The parser read the document and found nothing to declare, which is
		// an answer rather than a gap - and it still leaves the caller with a
		// document and no handle to address it by, the same position a file
		// no parser understands is in.
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
	// The patch quotes the span that actually differs, not the span the
	// caller replaced. A whole-file write - which is how a plan records every
	// file it commits - would otherwise quote the file twice, so a fourteen
	// byte change in a seven kilobyte file produced a sixteen kilobyte patch
	// and a two-file migration produced eighty kilobytes of receipt that said
	// nothing a reader could use.
	if start == 0 && end == len(before) && len(replacement) == len(after) {
		start, end, replacement = changedSpan(before, after)
	}
	return ExactDiff{
		Path: path, BeforeSHA256: hashBytes(before), AfterSHA256: hashBytes(after),
		Before: append([]byte(nil), before...), After: append([]byte(nil), after...),
		Patch: fmt.Sprintf("--- %s\n+++ %s\n@@ bytes %d:%d @@\n-%q\n+%q\n", path, path, start, end, before[start:end], replacement),
	}
}

// changedSpan is the narrowest byte range that differs between two versions
// of a document, with the bytes that replace it: the common prefix and the
// common suffix are dropped. It is exact rather than a heuristic - the range
// and the replacement reconstruct after from before - and it costs one pass
// in each direction.
func changedSpan(before, after []byte) (int, int, []byte) {
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix &&
		before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	return prefix, len(before) - suffix, after[prefix : len(after)-suffix]
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
		// A new file in a directory that does not exist yet is an ordinary
		// thing to write, and there is no directory operation in the API to
		// make the parent with, so creating it here is the difference between
		// one call and a shell reach. The path is already confined to the
		// workspace root. A rollback removes the file and leaves the empty
		// directory, which is inert and untracked.
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			err = atomicWriteFile(path, postimage, fs.FileMode(mode))
		}
	} else if err = os.Remove(path); err == nil {
		err = syncDirectory(filepath.Dir(path))
	}
	if err != nil {
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
	if exists != expectedExists || (exists && (!regularFile(disk) || !bytes.Equal(content, expected))) {
		return fmt.Errorf("commit precondition changed for %s", path)
	}
	return nil
}

// regularFile reports whether a snapshot is a regular file whose bytes the
// native journal can compare: text or binary, never a directory or symlink.
func regularFile(disk DiskSnapshot) bool {
	return disk.Kind == ObjectRegularText || disk.Kind == ObjectBinary
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
		var restoreErr error
		if record.PreExists {
			restoreErr = atomicWriteFile(target, preimage, fs.FileMode(record.Mode))
		} else if restoreErr = os.Remove(target); restoreErr == nil || errors.Is(restoreErr, os.ErrNotExist) {
			restoreErr = syncDirectory(filepath.Dir(target))
		}
		if restoreErr != nil {
			return result, restoreErr
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
	return !exists || (regularFile(disk) && bytes.Equal(current, content))
}

func displayPath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return path
}
