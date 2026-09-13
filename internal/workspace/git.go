package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxGitOutput = 2 << 20

type CommitHandle string
type CommitSummary struct {
	Handle           CommitHandle `json:"handle"`
	ObjectID         string       `json:"object_id"`
	AbbreviatedID    string       `json:"abbreviated_id"`
	Subject          string       `json:"subject"`
	AuthorName       string       `json:"author_name"`
	AuthoredAt       string       `json:"authored_at"`
	CommittedAt      string       `json:"committed_at"`
	ChangedPathCount int          `json:"changed_path_count"`
	ParentCount      int          `json:"parent_count"`
}
type GitCoverage struct {
	Complete        bool     `json:"complete"`
	Ref             string   `json:"ref"`
	Traversal       string   `json:"traversal"`
	RenamePolicy    string   `json:"rename_policy"`
	Shallow         bool     `json:"shallow"`
	RenameAmbiguous bool     `json:"rename_ambiguous"`
	MissingObjects  bool     `json:"missing_objects"`
	Binary          bool     `json:"binary"`
	Submodule       bool     `json:"submodule"`
	Truncated       bool     `json:"truncated"`
	Unavailable     []string `json:"unavailable,omitempty"`
}
type CommitList struct {
	Commits  []CommitSummary `json:"commits"`
	Coverage GitCoverage     `json:"coverage"`
}
type ProvenanceSpan struct {
	StartLine    int            `json:"start_line"`
	EndLine      int            `json:"end_line"`
	Origin       string         `json:"origin"`
	Commit       *CommitSummary `json:"commit,omitempty"`
	OriginalPath string         `json:"original_path,omitempty"`
}
type FileAge struct {
	Ref                               string `json:"ref"`
	Traversal                         string `json:"traversal"`
	RenamePolicy                      string `json:"rename_policy"`
	FirstParentCommitsSinceLastChange int    `json:"first_parent_commits_since_last_change"`
	LastChangeCommit                  string `json:"last_change_commit,omitempty"`
	LastChangeCommittedAt             string `json:"last_change_committed_at,omitempty"`
	IntroductionCommit                string `json:"introduction_commit,omitempty"`
	IntroductionEvidence              string `json:"introduction_evidence"`
}
type HistoryRequest struct {
	Path                      string
	StartLine, EndLine, Limit int
	Content                   []byte
	Source                    string
}
type FileHistory struct {
	Path          string           `json:"path"`
	Source        string           `json:"source"`
	Spans         []ProvenanceSpan `json:"spans"`
	RecentCommits []CommitSummary  `json:"recent_commits"`
	Age           FileAge          `json:"age"`
	Coverage      GitCoverage      `json:"coverage"`
}
type CommitChange struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	OldPath   string `json:"old_path,omitempty"`
	Binary    bool   `json:"binary"`
	Submodule bool   `json:"submodule"`
}
type CommitChanges struct {
	Commit   CommitSummary  `json:"commit"`
	Changes  []CommitChange `json:"changes"`
	Patch    string         `json:"patch,omitempty"`
	Coverage GitCoverage    `json:"coverage"`
}
type HistorySearchRequest struct {
	Query  string
	Fields []string
	Ref    string
	Limit  int
}
type HistorySearchHit struct {
	Commit  CommitSummary `json:"commit"`
	Fields  []string      `json:"fields"`
	Paths   []string      `json:"paths,omitempty"`
	Excerpt string        `json:"excerpt,omitempty"`
}
type HistorySearchResult struct {
	Query string             `json:"query"`
	Hits  []HistorySearchHit `json:"hits"`
	// ScannedCommits is how far back the search looked. limit is a window
	// over history, not a cap on results, so a search that found nothing in
	// five commits has to say five rather than imply the whole history.
	ScannedCommits int         `json:"scanned_commits"`
	ResultSet      ResultSet   `json:"result_set"`
	Coverage       GitCoverage `json:"coverage"`
}
type commitRecord struct {
	summary CommitSummary
	epoch   uint64
	expires time.Time
}
type gitState struct {
	mu           sync.Mutex
	root, prefix string
	commits      map[CommitHandle]commitRecord
}
type cappedBuffer struct {
	bytes.Buffer
	left      int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	total, n := len(p), len(p)
	if n > b.left {
		n = b.left
	}
	if n > 0 {
		_, _ = b.Buffer.Write(p[:n])
		b.left -= n
	}
	if n < total {
		b.truncated = true
	}
	return total, nil
}

func sanitizedGitEnvironment() []string {
	blocked := []string{
		"GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR=", "GIT_OBJECT_DIRECTORY=",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_INDEX_FILE=", "GIT_REPLACE_REF_BASE=",
		"GIT_GRAFT_FILE=", "GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_",
		"GIT_SSH=", "GIT_SSH_COMMAND=", "GIT_ASKPASS=",
	}
	environment := make([]string, 0, len(os.Environ())+10)
	for _, value := range os.Environ() {
		rejected := false
		for _, prefix := range blocked {
			if strings.HasPrefix(value, prefix) {
				rejected = true
				break
			}
		}
		if !rejected {
			environment = append(environment, value)
		}
	}
	return append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat", "PAGER=cat", "GIT_EXTERNAL_DIFF=", "NO_COLOR=1")
}

func runGitAt(root string, stdin []byte, args ...string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	safe := []string{"--no-pager", "-C", root, "-c", "core.hooksPath=/dev/null",
		"-c", "core.pager=cat", "-c", "pager.log=false", "-c", "pager.show=false",
		"-c", "pager.diff=false", "-c", "diff.external=", "-c", "diff.trustExitCode=false",
		"-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "protocol.allow=never",
		"-c", "submodule.recurse=false"}
	cmd := exec.CommandContext(ctx, "git", append(safe, args...)...)
	cmd.Env = sanitizedGitEnvironment()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, errout := &cappedBuffer{left: maxGitOutput}, &cappedBuffer{left: 16 << 10}
	cmd.Stdout, cmd.Stderr = out, errout
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(errout.String())
		if message == "" {
			message = err.Error()
		}
		return out.String(), out.truncated, errors.New(sanitizeText(message, 2048))
	}
	return out.String(), out.truncated, nil
}

func (w *Workspace) gitRepository() (*gitState, error) {
	w.handlesMu.Lock()
	defer w.handlesMu.Unlock()
	if w.git != nil {
		return w.git, nil
	}
	id := w.Identity()
	if id.Kind == KindDocuments {
		return nil, errors.New("git history unavailable for document workspaces")
	}
	out, _, err := runGitAt(id.Root, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("git history unavailable: %w", err)
	}
	root := filepath.Clean(strings.TrimSpace(out))
	prefix, err := filepath.Rel(root, id.Root)
	if err != nil || prefix == ".." || strings.HasPrefix(prefix, ".."+string(filepath.Separator)) {
		return nil, errors.New("workspace is outside Git repository")
	}
	if prefix == "." {
		prefix = ""
	}
	w.git = &gitState{root: root, prefix: filepath.ToSlash(prefix), commits: make(map[CommitHandle]commitRecord)}
	return w.git, nil
}
func bounded(n, fallback, maximum int) int {
	if n <= 0 {
		n = fallback
	}
	if n > maximum {
		n = maximum
	}
	return n
}
func coverageFor(root string) GitCoverage {
	c := GitCoverage{Complete: true, Ref: "HEAD", Traversal: "first_parent", RenamePolicy: "follow_bounded"}
	if out, _, err := runGitAt(root, nil, "rev-parse", "--is-shallow-repository"); err == nil {
		c.Shallow = strings.TrimSpace(out) == "true"
	}
	if out, _, err := runGitAt(root, nil, "replace", "-l"); err == nil && strings.TrimSpace(out) != "" {
		c.Complete = false
		c.Unavailable = []string{"replace_or_graft_history"}
	}
	return c
}
func parseCommit(line string) (CommitSummary, error) {
	f := strings.Split(line, "\x00")
	if len(f) < 7 {
		return CommitSummary{}, errors.New("malformed Git commit record")
	}
	parents := 0
	if strings.TrimSpace(f[6]) != "" {
		parents = len(strings.Fields(f[6]))
	}
	return CommitSummary{ObjectID: f[0], AbbreviatedID: f[1], Subject: f[2], AuthorName: f[3], AuthoredAt: f[4], CommittedAt: f[5], ParentCount: parents}, nil
}
func (w *Workspace) rememberCommit(g *gitState, s CommitSummary) (CommitSummary, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now, epoch := time.Now(), w.Identity().Epoch
	for h, r := range g.commits {
		if now.After(r.expires) || r.epoch != epoch {
			delete(g.commits, h)
			continue
		}
		if r.summary.ObjectID == s.ObjectID {
			s.Handle = h
			r.summary = s
			g.commits[h] = r
			return s, nil
		}
	}
	h, err := randomOpaque("commit_")
	if err != nil {
		return CommitSummary{}, err
	}
	ttl := defaultHandleTTL
	hs := w.handleRegistry()
	hs.mu.Lock()
	ttl = hs.ttl
	hs.mu.Unlock()
	s.Handle = CommitHandle(h)
	g.commits[s.Handle] = commitRecord{summary: s, epoch: epoch, expires: now.Add(ttl)}
	return s, nil
}
func (w *Workspace) ResolveCommit(h CommitHandle) (CommitSummary, error) {
	g, err := w.gitRepository()
	if err != nil {
		return CommitSummary{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.commits[h]
	if !ok {
		return CommitSummary{}, fmt.Errorf("unknown commit handle %s", h)
	}
	if r.epoch != w.Identity().Epoch {
		delete(g.commits, h)
		return CommitSummary{}, &Conflict{Code: ConflictWorkspaceEpoch}
	}
	if time.Now().After(r.expires) {
		delete(g.commits, h)
		return CommitSummary{}, fmt.Errorf("commit handle %s expired", h)
	}
	return r.summary, nil
}
func within(path, prefix string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}
func (w *Workspace) summary(g *gitState, oid string) (CommitSummary, error) {
	out, _, err := runGitAt(g.root, nil, "show", "-s", "--no-show-signature", "--format=%H%x00%h%x00%s%x00%an%x00%aI%x00%cI%x00%P", oid)
	if err != nil {
		return CommitSummary{}, err
	}
	s, err := parseCommit(strings.TrimRight(out, "\n"))
	if err != nil {
		return CommitSummary{}, err
	}
	if paths, _, e := runGitAt(g.root, nil, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", oid); e == nil {
		for _, p := range strings.Split(strings.TrimSpace(paths), "\n") {
			if p != "" && within(p, g.prefix) {
				s.ChangedPathCount++
			}
		}
	}
	return w.rememberCommit(g, s)
}
func (w *Workspace) RecentCommits(limit int) (CommitList, error) {
	g, err := w.gitRepository()
	if err != nil {
		return CommitList{Coverage: GitCoverage{Complete: false, Unavailable: []string{"git_repository"}}}, err
	}
	limit = bounded(limit, 3, 10)
	args := []string{"log", "--first-parent", "-n", strconv.Itoa(limit + 1), "--format=%H"}
	if g.prefix != "" {
		args = append(args, "--", g.prefix)
	}
	out, truncated, err := runGitAt(g.root, nil, args...)
	r := CommitList{Coverage: coverageFor(g.root)}
	r.Coverage.Truncated = truncated
	if truncated || err != nil {
		r.Coverage.Complete = false
	}
	if err != nil {
		r.Coverage.MissingObjects = true
		return r, err
	}
	objects := strings.Fields(out)
	if len(objects) > limit {
		objects = objects[:limit]
		r.Coverage.Complete = false
		r.Coverage.Truncated = true
	}
	for _, oid := range objects {
		s, e := w.summary(g, oid)
		if e != nil {
			r.Coverage.Complete = false
			r.Coverage.MissingObjects = true
			continue
		}
		r.Commits = append(r.Commits, s)
	}
	return r, nil
}

// Tracked states reported by TrackedState.
const (
	TrackedStateTracked       = "tracked"
	TrackedStateUntracked     = "untracked"
	TrackedStateIgnored       = "ignored"
	TrackedStateNotRepository = "not_a_repository"
)

// TrackedState classifies workspace paths for the Git step that follows a
// file-lifecycle edit: tracked (in the index, whether or not the file still
// exists), ignored, or untracked. It is read-only through the sanitized
// runner; Huyang never stages anything itself. Outside a repository every
// path is not_a_repository.
func (w *Workspace) TrackedState(paths []string) map[string]string {
	states := make(map[string]string, len(paths))
	g, err := w.gitRepository()
	if err != nil {
		for _, path := range paths {
			states[path] = TrackedStateNotRepository
		}
		return states
	}
	relative := make(map[string]string, len(paths))
	args := make([]string, 0, len(paths))
	for _, path := range paths {
		states[path] = TrackedStateUntracked
		if _, rel, err := w.gitPath(g, path); err == nil {
			relative[rel] = path
			args = append(args, rel)
		} else {
			states[path] = TrackedStateNotRepository
		}
	}
	if len(args) == 0 {
		return states
	}
	if out, _, err := runGitAt(g.root, nil, append([]string{"ls-files", "-z", "--cached", "--"}, args...)...); err == nil {
		for _, rel := range strings.Split(out, "\x00") {
			if path, ok := relative[rel]; ok {
				states[path] = TrackedStateTracked
			}
		}
	}
	// check-ignore exits 1 when nothing is ignored; its output is authoritative
	// either way. -z requires --stdin, so the paths go through it.
	out, _, _ := runGitAt(g.root, []byte(strings.Join(args, "\x00")+"\x00"), "check-ignore", "-z", "--stdin")
	for _, rel := range strings.Split(out, "\x00") {
		if path, ok := relative[rel]; ok && states[path] == TrackedStateUntracked {
			states[path] = TrackedStateIgnored
		}
	}
	return states
}

func (w *Workspace) gitPath(g *gitState, path string) (string, string, error) {
	abs, err := w.confinedPath(path)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(g.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", errors.New("history target is outside Git repository")
	}
	return abs, filepath.ToSlash(rel), nil
}

var blameHeader = regexp.MustCompile(`^([0-9a-f]{40,64}|0{40,64}) ([0-9]+) ([0-9]+)(?: ([0-9]+))?$`)

func parseBlame(out, zero string, summaries map[string]CommitSummary) []ProvenanceSpan {
	sc := bufio.NewScanner(strings.NewReader(out))
	var spans []ProvenanceSpan
	var oid, path string
	lineNo := 0
	for sc.Scan() {
		line := sc.Text()
		if m := blameHeader.FindStringSubmatch(line); m != nil {
			oid = m[1]
			lineNo, _ = strconv.Atoi(m[3])
			path = ""
			continue
		}
		if strings.HasPrefix(line, "filename ") {
			path = strings.TrimPrefix(line, "filename ")
			continue
		}
		if !strings.HasPrefix(line, "\t") || lineNo == 0 {
			continue
		}
		origin := "committed"
		var commit *CommitSummary
		if strings.Trim(oid, "0") == "" {
			origin = zero
		} else if s, ok := summaries[oid]; ok {
			x := s
			commit = &x
		}
		if len(spans) > 0 {
			last := &spans[len(spans)-1]
			same := (last.Commit == nil && commit == nil) || (last.Commit != nil && commit != nil && last.Commit.ObjectID == commit.ObjectID)
			if last.EndLine+1 == lineNo && last.Origin == origin && last.OriginalPath == path && same {
				last.EndLine = lineNo
				continue
			}
		}
		spans = append(spans, ProvenanceSpan{StartLine: lineNo, EndLine: lineNo, Origin: origin, Commit: commit, OriginalPath: path})
	}
	return spans
}
func mergeCoverage(a *GitCoverage, b GitCoverage) {
	a.Complete = a.Complete && b.Complete
	a.Shallow = a.Shallow || b.Shallow
	a.MissingObjects = a.MissingObjects || b.MissingObjects
	a.Truncated = a.Truncated || b.Truncated
	a.Unavailable = append(a.Unavailable, b.Unavailable...)
}
func (w *Workspace) touching(g *gitState, path string, limit int) ([]CommitSummary, GitCoverage) {
	c := coverageFor(g.root)
	c.Traversal = "date_order"
	out, tr, err := runGitAt(g.root, nil, "log", "--follow", "-n", strconv.Itoa(limit), "--format=%H", "--", path)
	c.Truncated = tr
	if err != nil {
		c.Complete = false
		c.MissingObjects = true
		return nil, c
	}
	var result []CommitSummary
	for _, oid := range strings.Fields(out) {
		s, e := w.summary(g, oid)
		if e != nil {
			c.Complete = false
			c.MissingObjects = true
		} else {
			result = append(result, s)
		}
	}
	if len(result) == limit {
		c.Complete = false
		c.Truncated = true
	}
	return result, c
}
func (w *Workspace) age(g *gitState, path string, c *GitCoverage) FileAge {
	a := FileAge{Ref: "HEAD", Traversal: "first_parent", RenamePolicy: "follow_bounded", IntroductionEvidence: "complete"}
	if out, _, e := runGitAt(g.root, nil, "log", "--first-parent", "-1", "--format=%H%x00%cI", "--", path); e == nil && strings.TrimSpace(out) != "" {
		f := strings.Split(strings.TrimSpace(out), "\x00")
		a.LastChangeCommit = f[0]
		if len(f) > 1 {
			a.LastChangeCommittedAt = f[1]
		}
		if n, _, x := runGitAt(g.root, nil, "rev-list", "--first-parent", "--count", f[0]+"..HEAD"); x == nil {
			a.FirstParentCommitsSinceLastChange, _ = strconv.Atoi(strings.TrimSpace(n))
		}
	}
	intro, tr, e := runGitAt(g.root, nil, "log", "--follow", "--diff-filter=A", "-1", "--format=%H", "--", path)
	if e == nil {
		a.IntroductionCommit = strings.TrimSpace(intro)
	}
	renames, renameTruncated, renameErr := runGitAt(g.root, nil, "log", "--follow", "--format=", "--name-status", "--diff-filter=R", "--", path)
	if strings.TrimSpace(renames) != "" && (c.Shallow || renameTruncated || renameErr != nil) {
		c.RenameAmbiguous = true
		c.Complete = false
	}
	if c.Shallow || c.RenameAmbiguous || tr || e != nil || a.IntroductionCommit == "" {
		a.IntroductionEvidence = "provisional"
		c.Complete = false
		if c.Shallow {
			c.Unavailable = append(c.Unavailable, "history_before_shallow_boundary")
		}
	}
	return a
}
func (w *Workspace) FileHistory(req HistoryRequest) (FileHistory, error) {
	g, err := w.gitRepository()
	if err != nil {
		return FileHistory{}, err
	}
	abs, path, err := w.gitPath(g, req.Path)
	if err != nil {
		return FileHistory{}, err
	}
	c := coverageFor(g.root)
	c.Traversal = "blame_first_parent_age"
	source := req.Source
	if source == "" {
		source = "canonical"
	}
	content := req.Content
	if content == nil {
		content, err = os.ReadFile(abs)
		if err != nil {
			if mode, _, e := runGitAt(g.root, nil, "ls-files", "-s", "--", path); e == nil && strings.HasPrefix(mode, "160000 ") {
				c.Complete = false
				c.Submodule = true
				return FileHistory{Path: req.Path, Source: source, Coverage: c}, nil
			}
			return FileHistory{}, err
		}
	}
	if bytes.IndexByte(content, 0) >= 0 {
		c.Complete = false
		c.Binary = true
		return FileHistory{Path: req.Path, Source: source, Coverage: c}, nil
	}
	start, end := req.StartLine, req.EndLine
	if start <= 0 {
		start = 1
	}
	if end <= 0 {
		end = start + bounded(req.Limit, 20, 100) - 1
	}
	out, tr, e := runGitAt(g.root, content, "blame", "--line-porcelain", "--no-progress", "--contents", "-", "-L", fmt.Sprintf("%d,%d", start, end), "HEAD", "--", path)
	c.Truncated = tr
	if tr || e != nil {
		c.Complete = false
	}
	if e != nil {
		c.MissingObjects = strings.Contains(strings.ToLower(e.Error()), "missing") || strings.Contains(strings.ToLower(e.Error()), "bad object")
		return FileHistory{Path: req.Path, Source: source, Coverage: c}, e
	}
	sums := make(map[string]CommitSummary)
	for _, m := range blameHeader.FindAllStringSubmatch(out, -1) {
		oid := m[1]
		if strings.Trim(oid, "0") == "" {
			continue
		}
		if _, ok := sums[oid]; !ok {
			if s, x := w.summary(g, oid); x == nil {
				sums[oid] = s
			} else {
				c.Complete = false
				c.MissingObjects = true
			}
		}
	}
	zero := "uncommitted"
	if source == "prepared" {
		zero = "derived_from_plan"
	}
	touch, tc := w.touching(g, path, bounded(req.Limit, 20, 100))
	mergeCoverage(&c, tc)
	age := w.age(g, path, &c)
	return FileHistory{Path: req.Path, Source: source, Spans: parseBlame(out, zero, sums), RecentCommits: touch, Age: age, Coverage: c}, nil
}
func parseChanges(out string) []CommitChange {
	var r []CommitChange
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(line, "\t")
		if !strings.HasPrefix(line, ":") || len(parts) < 2 {
			continue
		}
		meta := strings.Fields(parts[0])
		if len(meta) < 5 {
			continue
		}
		status := meta[4]
		x := CommitChange{Path: parts[len(parts)-1], Status: status, Submodule: meta[0] == ":160000" || meta[1] == "160000"}
		if (strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C")) && len(parts) >= 3 {
			x.OldPath, x.Path = parts[1], parts[2]
		}
		r = append(r, x)
	}
	return r
}

func binaryPaths(numstat string) map[string]bool {
	paths := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) >= 3 && fields[0] == "-" && fields[1] == "-" {
			paths[fields[len(fields)-1]] = true
		}
	}
	return paths
}
func (w *Workspace) CommitChanges(handle CommitHandle, limit int) (CommitChanges, error) {
	s, err := w.ResolveCommit(handle)
	if err != nil {
		return CommitChanges{}, err
	}
	g, err := w.gitRepository()
	if err != nil {
		return CommitChanges{}, err
	}
	c := coverageFor(g.root)
	rawArgs := []string{"diff-tree", "--root", "--raw", "-r", "--no-commit-id", s.ObjectID}
	patchArgs := []string{"show", "--format=", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=3", s.ObjectID, "--"}
	numstatArgs := []string{"show", "--format=", "--numstat", "--no-ext-diff", "--no-textconv", "--no-renames", s.ObjectID, "--"}
	if s.ParentCount > 1 {
		c.Traversal = "first_parent_diff"
		rawArgs = []string{"diff", "--raw", "--no-ext-diff", "--no-renames", s.ObjectID + "^1", s.ObjectID, "--"}
		patchArgs = []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=3", s.ObjectID + "^1", s.ObjectID, "--"}
		numstatArgs = []string{"diff", "--numstat", "--no-ext-diff", "--no-textconv", "--no-renames", s.ObjectID + "^1", s.ObjectID, "--"}
	}
	raw, tr, err := runGitAt(g.root, nil, rawArgs...)
	if err != nil {
		c.Complete = false
		c.MissingObjects = true
		return CommitChanges{Commit: s, Coverage: c}, err
	}
	changes := parseChanges(raw)
	max := bounded(limit, 20, 100)
	if len(changes) > max {
		changes = changes[:max]
		c.Complete = false
		c.Truncated = true
	}
	patch, ptr, pe := runGitAt(g.root, nil, patchArgs...)
	if pe != nil {
		c.Complete = false
		c.MissingObjects = true
	}
	numstat, ntr, ne := runGitAt(g.root, nil, numstatArgs...)
	if ne != nil {
		c.Complete = false
		c.MissingObjects = true
	}
	if ptr || tr || ntr {
		c.Complete = false
		c.Truncated = true
	}
	binaries := binaryPaths(numstat)
	for i := range changes {
		if changes[i].Submodule {
			c.Submodule = true
		}
		if binaries[changes[i].Path] {
			changes[i].Binary = true
			c.Binary = true
		}
	}
	return CommitChanges{Commit: s, Changes: changes, Patch: patch, Coverage: c}, nil
}
func unique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
func (w *Workspace) RecentRenameDestination(path string) (string, bool) {
	g, err := w.gitRepository()
	if err != nil {
		return "", false
	}
	_, target, err := w.gitPath(g, path)
	if err != nil {
		return "", false
	}
	out, _, err := runGitAt(g.root, nil, "log", "-n", "50", "--format=", "--name-status", "--find-renames", "HEAD")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.HasPrefix(fields[0], "R") || fields[1] != target || !within(fields[2], g.prefix) {
			continue
		}
		info, statErr := os.Stat(filepath.Join(g.root, filepath.FromSlash(fields[2])))
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		destination := fields[2]
		if g.prefix != "" {
			destination = strings.TrimPrefix(destination, g.prefix+"/")
		}
		return destination, true
	}
	return "", false
}

func (w *Workspace) SearchHistory(req HistorySearchRequest) (HistorySearchResult, error) {
	g, err := w.gitRepository()
	if err != nil {
		return HistorySearchResult{}, err
	}
	if req.Query == "" {
		return HistorySearchResult{}, errors.New("history query is required")
	}
	ref := req.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if len(ref) > 256 || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n") {
		return HistorySearchResult{}, errors.New("invalid local history ref")
	}
	limit := bounded(req.Limit, 20, 100)
	fields := historyFields(req.Fields)
	c := coverageFor(g.root)
	c.Ref = ref
	c.Traversal = "date_order"
	args := []string{"log", "-n", strconv.Itoa(limit + 1), "--format=--HUYANG-COMMIT--%H%x00%h%x00%s%x00%an%x00%aI%x00%cI%x00%P", "--name-only"}
	if fields["diff"] {
		args = append(args, "--patch", "--no-ext-diff", "--no-textconv", "--no-renames")
	}
	args = append(args, ref)
	out, tr, e := runGitAt(g.root, nil, args...)
	c.Truncated = tr
	if tr || e != nil {
		c.Complete = false
	}
	if e != nil {
		c.MissingObjects = true
		return HistorySearchResult{Query: req.Query, Coverage: c}, e
	}
	needle := strings.ToLower(req.Query)
	var hits []HistorySearchHit
	blocks := strings.Split(out, "--HUYANG-COMMIT--")[1:]
	if len(blocks) > limit {
		blocks = blocks[:limit]
		c.Complete = false
		c.Truncated = true
	}
	for _, block := range blocks {
		hit, ok, hitErr := w.historyHit(g, block, fields, needle)
		if hitErr != nil {
			return HistorySearchResult{}, hitErr
		}
		if ok {
			hits = append(hits, hit)
		}
	}
	set, err := w.FreezeHistoricalResultSet(len(hits), len(hits), Coverage{Complete: c.Complete, Capped: c.Truncated})
	if err != nil {
		return HistorySearchResult{}, err
	}
	return HistorySearchResult{Query: req.Query, Hits: hits, ScannedCommits: len(blocks), ResultSet: set, Coverage: c}, nil
}

// historyFields selects the searched fields: message only by default, otherwise the
// recognised names among those requested.
func historyFields(requested []string) map[string]bool {
	if len(requested) == 0 {
		return map[string]bool{"message": true}
	}
	fields := make(map[string]bool)
	for _, f := range requested {
		if f == "message" || f == "path" || f == "diff" {
			fields[f] = true
		}
	}
	return fields
}

// historyHit scans one commit block of the log output for the needle in the selected
// fields. ok is false when the block is unparsable or matches nowhere.
func (w *Workspace) historyHit(g *gitState, block string, fields map[string]bool, needle string) (HistorySearchHit, bool, error) {
	end := strings.IndexByte(block, '\n')
	if end < 0 {
		return HistorySearchHit{}, false, nil
	}
	s, x := parseCommit(strings.TrimSpace(block[:end]))
	if x != nil {
		return HistorySearchHit{}, false, nil
	}
	s, x = w.summary(g, s.ObjectID)
	if x != nil {
		return HistorySearchHit{}, false, x
	}
	hit := HistorySearchHit{Commit: s}
	if fields["message"] && strings.Contains(strings.ToLower(s.Subject), needle) {
		hit.Fields = append(hit.Fields, "message")
	}
	for _, line := range strings.Split(block[end+1:], "\n") {
		trim := strings.TrimSpace(line)
		if fields["path"] && !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") && within(trim, g.prefix) && strings.Contains(strings.ToLower(trim), needle) {
			hit.Fields = unique(hit.Fields, "path")
			hit.Paths = unique(hit.Paths, trim)
		}
		if fields["diff"] && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) && !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") && strings.Contains(strings.ToLower(line), needle) {
			hit.Fields = unique(hit.Fields, "diff")
			if hit.Excerpt == "" {
				hit.Excerpt = sanitizeText(line, 240)
			}
		}
	}
	return hit, len(hit.Fields) > 0, nil
}
