package workspace

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTestRun(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test Author", "GIT_AUTHOR_EMAIL=author@example.invalid",
		"GIT_COMMITTER_NAME=Test Committer", "GIT_COMMITTER_EMAIL=committer@example.invalid",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeGitTestFile(t *testing.T, root, name string, content []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitGitTestFile(t *testing.T, root, name string, content []byte, subject string) {
	t.Helper()
	writeGitTestFile(t, root, name, content)
	gitTestRun(t, root, "add", "--", name)
	gitTestRun(t, root, "commit", "-m", subject, "-m", "private body must not be returned")
}

func newGitWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	gitTestRun(t, root, "init", "-q")
	commitGitTestFile(t, root, "note.txt", []byte("one\ntwo\n"), "initial note")
	workspace, err := Open(OpenOptions{Kind: KindProject, Root: root, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, root
}

func repositoryFingerprint(t *testing.T, root string) [32]byte {
	t.Helper()
	head := gitTestRun(t, root, "-c", "core.fsmonitor=false", "rev-parse", "HEAD")
	status := gitTestRun(t, root, "-c", "core.fsmonitor=false", "status", "--porcelain=v2", "--untracked-files=all")
	index, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	return sha256.Sum256(append(append([]byte(head+"\n"+status+"\n"), index...), 0))
}

func TestGitRecentHistoryDirtyPreparedAndSearch(t *testing.T) {
	workspace, root := newGitWorkspace(t)
	commitGitTestFile(t, root, "other.txt", []byte("needle\n"), "mention needle")
	commitGitTestFile(t, root, "note.txt", []byte("one\ntwo\nthree\n"), "extend note")
	commitGitTestFile(t, root, "last.txt", []byte("last\n"), "latest")

	recent, err := workspace.RecentCommits(99)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent.Commits) != 4 {
		t.Fatalf("recent commits = %d, want bounded available history", len(recent.Commits))
	}
	if recent.Commits[0].Handle == "" || recent.Commits[0].Subject != "latest" {
		t.Fatalf("unexpected recent commit: %+v", recent.Commits[0])
	}
	encoded, _ := json.Marshal(recent)
	for _, forbidden := range []string{"author@example.invalid", "private body", "note.txt"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("automatic overview leaked %q: %s", forbidden, encoded)
		}
	}

	writeGitTestFile(t, root, "note.txt", []byte("one\nchanged\nthree\n"))
	dirty, err := workspace.FileHistory(HistoryRequest{Path: "note.txt", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	origins := map[string]bool{}
	for _, span := range dirty.Spans {
		origins[span.Origin] = true
	}
	if !origins["committed"] || !origins["uncommitted"] {
		t.Fatalf("dirty provenance = %+v", dirty.Spans)
	}
	prepared, err := workspace.FileHistory(HistoryRequest{
		Path: "note.txt", Content: []byte("one\nprepared\nthree\n"), Source: "prepared", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	origins = map[string]bool{}
	for _, span := range prepared.Spans {
		origins[span.Origin] = true
	}
	if !origins["committed"] || !origins["derived_from_plan"] {
		t.Fatalf("prepared provenance = %+v", prepared.Spans)
	}
	if prepared.Age.Ref != "HEAD" || prepared.Age.Traversal != "first_parent" ||
		prepared.Age.RenamePolicy != "follow_bounded" {
		t.Fatalf("unnamed age metrics: %+v", prepared.Age)
	}

	search, err := workspace.SearchHistory(HistorySearchRequest{Query: "needle", Fields: []string{"message", "path", "diff"}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Hits) == 0 || search.ResultSet.Kind != ResultSetHistorical || search.ResultSet.AllMatchesEligible {
		t.Fatalf("historical search = %+v", search)
	}
	if search.Hits[0].Commit.ChangedPathCount != 1 {
		t.Fatalf("history search changed path count = %d, want 1", search.Hits[0].Commit.ChangedPathCount)
	}
	changes, err := workspace.CommitChanges(search.Hits[0].Commit.Handle, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Changes) == 0 {
		t.Fatal("commit change view has no paths")
	}
}

func TestGitRecentCommitsExposeFirstParentMergeShape(t *testing.T) {
	workspace, root := newGitWorkspace(t)
	mainBranch := gitTestRun(t, root, "symbolic-ref", "--short", "HEAD")
	gitTestRun(t, root, "checkout", "-q", "-b", "topic")
	commitGitTestFile(t, root, "topic.txt", []byte("topic\n"), "topic change")
	gitTestRun(t, root, "checkout", "-q", mainBranch)
	commitGitTestFile(t, root, "main.txt", []byte("main\n"), "main change")
	gitTestRun(t, root, "merge", "--no-ff", "topic", "-m", "merge topic")
	recent, err := workspace.RecentCommits(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent.Commits) != 3 || recent.Commits[0].ParentCount != 2 ||
		recent.Commits[1].Subject != "main change" {
		t.Fatalf("first-parent merge summaries = %+v", recent.Commits)
	}
	changes, err := workspace.CommitChanges(recent.Commits[0].Handle, 20)
	if err != nil {
		t.Fatal(err)
	}
	if changes.Coverage.Traversal != "first_parent_diff" || len(changes.Changes) != 1 ||
		changes.Changes[0].Path != "topic.txt" {
		t.Fatalf("merge changes = %+v", changes)
	}
}

func TestGitHistoryRenameShallowBinarySubmoduleAndMissingObjects(t *testing.T) {
	workspace, root := newGitWorkspace(t)
	gitTestRun(t, root, "mv", "note.txt", "renamed.txt")
	gitTestRun(t, root, "commit", "-m", "rename note")
	history, err := workspace.FileHistory(HistoryRequest{Path: "renamed.txt", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.RecentCommits) < 2 {
		t.Fatalf("rename history did not follow: %+v", history)
	}

	commitGitTestFile(t, root, "binary.dat", []byte{0, 1, 2}, "add binary")
	binary, err := workspace.FileHistory(HistoryRequest{Path: "binary.dat"})
	if err != nil || !binary.Coverage.Binary || binary.Coverage.Complete {
		t.Fatalf("binary coverage = %+v, err=%v", binary.Coverage, err)
	}

	head := gitTestRun(t, root, "rev-parse", "HEAD")
	gitTestRun(t, root, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor/sub")
	gitTestRun(t, root, "commit", "-m", "add gitlink")
	submodule, err := workspace.FileHistory(HistoryRequest{Path: "vendor/sub"})
	if err != nil || !submodule.Coverage.Submodule || submodule.Coverage.Complete {
		t.Fatalf("submodule coverage = %+v, err=%v", submodule.Coverage, err)
	}

	clone := t.TempDir()
	gitTestRun(t, clone, "clone", "-q", "--depth=1", "file://"+root, "shallow")
	shallowWorkspace, err := Open(OpenOptions{Kind: KindProject, Root: filepath.Join(clone, "shallow"), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	shallow, err := shallowWorkspace.FileHistory(HistoryRequest{Path: "renamed.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !shallow.Coverage.Shallow || shallow.Age.IntroductionEvidence != "provisional" {
		t.Fatalf("shallow coverage = %+v age=%+v", shallow.Coverage, shallow.Age)
	}

	broken, brokenRoot := newGitWorkspace(t)
	object := gitTestRun(t, brokenRoot, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(brokenRoot, ".git", "objects", object[:2], object[2:])); err != nil {
		t.Fatal(err)
	}
	list, err := broken.RecentCommits(3)
	if err == nil || !list.Coverage.MissingObjects || list.Coverage.Complete {
		t.Fatalf("missing object coverage = %+v err=%v", list.Coverage, err)
	}
}

func TestGitHistoryIgnoresRepositoryConfiguredExecutionAndIsInert(t *testing.T) {
	workspace, root := newGitWorkspace(t)
	marker := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(t.TempDir(), "malicious.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \""+marker+"\"\nexit 97\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, root, "config", "core.pager", script)
	gitTestRun(t, root, "config", "pager.log", "true")
	gitTestRun(t, root, "config", "diff.external", script)
	gitTestRun(t, root, "config", "core.fsmonitor", script)
	writeGitTestFile(t, root, ".gitattributes", []byte("*.txt diff=evil\n"))
	gitTestRun(t, root, "config", "diff.evil.command", script)
	gitTestRun(t, root, "add", ".gitattributes")
	gitTestRun(t, root, "commit", "-m", "malicious config fixture")
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	before := repositoryFingerprint(t, root)

	recent, err := workspace.RecentCommits(3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.FileHistory(HistoryRequest{Path: "note.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.CommitChanges(recent.Commits[0].Handle, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.SearchHistory(HistorySearchRequest{Query: "note", Fields: []string{"diff"}, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	after := repositoryFingerprint(t, root)
	if before != after {
		t.Fatal("read-only history changed HEAD, index, or worktree state")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("repository-configured executable ran: %v", err)
	}
}
