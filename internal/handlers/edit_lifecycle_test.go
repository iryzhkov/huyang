package handlers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
)

func lifecycleEdit(t *testing.T, handlers *Handlers, workspaceID, key string, operation map[string]any) map[string]any {
	t.Helper()
	return handlers.Execute(context.Background(), "req_"+key, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": key, "operation": operation,
	})
}

// move_file is one call: the destination holds the source's exact bytes,
// including gofmt drift and CRLF endings, the source is gone, and the
// reply names both paths. Outside a repository the Git state says so.
func TestMoveFileKeepsBytesExactAndSkipsTheFormatter(t *testing.T) {
	drift := "package p\n\nfunc  A()   {}\r\n"
	handlers, workspaceID, root := literalFixture(t, map[string]string{"old.go": drift})
	result := lifecycleEdit(t, handlers, workspaceID, "mv1", map[string]any{"kind": "move_file", "from": "old.go", "to": "pkg/new.go"})
	if result["outcome"] == "conflict" || result["outcome"] == "failed" {
		t.Fatalf("move = %#v", result)
	}
	moved, err := os.ReadFile(filepath.Join(root, "pkg", "new.go"))
	if err != nil || string(moved) != drift {
		t.Fatalf("moved bytes = %q, %v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(root, "old.go")); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	data := result["data"].(map[string]any)
	if paths := data["changed_paths"].([]string); len(paths) != 2 || paths[0] != "pkg/new.go" || paths[1] != "old.go" {
		t.Fatalf("changed paths = %#v", paths)
	}
	if _, formatted := data["format"]; formatted {
		t.Fatalf("move ran the formatter: %#v", data)
	}
	if git := data["git"].(map[string]string); git["old.go"] != "not_a_repository" {
		t.Fatalf("git state = %#v", git)
	}
	if next, _ := result["next"].([]any); len(next) > 0 {
		for _, step := range next {
			if step.(map[string]any)["tool"] == "shell" {
				t.Fatalf("move outside a repository suggested a git step: %#v", next)
			}
		}
	}
	again := lifecycleEdit(t, handlers, workspaceID, "mv2", map[string]any{"kind": "move_file", "from": "missing.go", "to": "x.go"})
	if again["outcome"] != "conflict" || again["code"] != "move_source_missing" {
		t.Fatalf("missing source = %#v", again)
	}
	onto := lifecycleEdit(t, handlers, workspaceID, "mv3", map[string]any{"kind": "move_file", "from": "pkg/new.go", "to": "pkg/new.go"})
	if onto["outcome"] != "conflict" || onto["code"] != "move_target_exists" {
		t.Fatalf("existing destination = %#v", onto)
	}
}

// In a repository the reply classifies both ends and names the one git
// command that records the move as a rename; running it makes Git see a
// 100 percent rename. Huyang itself never touches the index.
func TestMoveFileReportsTrackedStateAndTheStagingCommand(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"keep.txt": "same\n", "tracked.txt": "line one\nline two\n"})
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	indexBefore, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	result := lifecycleEdit(t, handlers, workspaceID, "mvgit", map[string]any{"kind": "move_file", "from": "tracked.txt", "to": "renamed.txt"})
	if result["outcome"] == "conflict" || result["outcome"] == "failed" {
		t.Fatalf("move = %#v", result)
	}
	states := result["data"].(map[string]any)["git"].(map[string]string)
	if states["tracked.txt"] != "tracked" || states["renamed.txt"] != "untracked" {
		t.Fatalf("git states = %#v", states)
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	if string(indexBefore) != string(indexAfter) {
		t.Fatal("the move wrote the Git index")
	}
	next := result["next"].([]any)
	step := next[0].(map[string]any)
	if step["tool"] != "shell" || step["command"] != "git add -A -- tracked.txt renamed.txt" {
		t.Fatalf("next = %#v", next)
	}
	git("add", "-A", "--", "tracked.txt", "renamed.txt")
	status := git("diff", "--cached", "-M", "--name-status")
	if !strings.HasPrefix(status, "R100\ttracked.txt\trenamed.txt") {
		t.Fatalf("git did not see a rename: %q", status)
	}
}

// copy_file reads its source server-side, from inside the workspace or
// from an absolute path outside it, and records what it copied.
func TestCopyFileFromInsideAndOutsideTheWorkspace(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"a.txt": "alpha\n"})
	inside := lifecycleEdit(t, handlers, workspaceID, "cp1", map[string]any{"kind": "copy_file", "from": "a.txt", "to": "b.txt"})
	if inside["outcome"] == "conflict" || inside["outcome"] == "failed" {
		t.Fatalf("inside copy = %#v", inside)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(content) != "alpha\n" {
		t.Fatalf("copied bytes = %q", content)
	}
	source := inside["data"].(map[string]any)["source"].(map[string]any)
	if source["path"] != "a.txt" || source["bytes"] != 6 || source["outside_workspace"] != nil {
		t.Fatalf("source record = %#v", source)
	}
	external := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(external, []byte("# outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := lifecycleEdit(t, handlers, workspaceID, "cp2", map[string]any{"kind": "copy_file", "from": external, "to": "docs/CLAUDE.md"})
	if outside["outcome"] == "conflict" || outside["outcome"] == "failed" {
		t.Fatalf("outside copy = %#v", outside)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "docs", "CLAUDE.md")); string(content) != "# outside\n" {
		t.Fatalf("copied bytes = %q", content)
	}
	if record := outside["data"].(map[string]any)["source"].(map[string]any); record["outside_workspace"] != true || record["sha256"] == "" {
		t.Fatalf("outside source record = %#v", record)
	}
	changed := lifecycleEdit(t, handlers, workspaceID, "cp3", map[string]any{"kind": "copy_file", "from": external, "to": "docs/again.md", "expected_sha256": "0000"})
	if changed["outcome"] != "conflict" || changed["code"] != "copy_source_changed" {
		t.Fatalf("hash mismatch = %#v", changed)
	}
	onto := lifecycleEdit(t, handlers, workspaceID, "cp4", map[string]any{"kind": "copy_file", "from": "a.txt", "to": "b.txt"})
	if onto["outcome"] != "conflict" || onto["code"] != "create_target_exists" {
		t.Fatalf("existing destination = %#v", onto)
	}
	missing := lifecycleEdit(t, handlers, workspaceID, "cp5", map[string]any{"kind": "copy_file", "from": "nope.txt", "to": "c.txt"})
	if missing["outcome"] != "conflict" || missing["code"] != "copy_source_missing" {
		t.Fatalf("missing source = %#v", missing)
	}
}

// delete_file without a guard changes nothing and hands back the revision
// and hash that make the retry one call; with either guard it removes the
// file, and a stale guard is refused.
func TestDeleteFileRequiresAGuard(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"gone.txt": "bye\n", "other.txt": "x\n"})
	unguarded := lifecycleEdit(t, handlers, workspaceID, "rm1", map[string]any{"kind": "delete_file", "path": "gone.txt"})
	if unguarded["outcome"] != "conflict" || unguarded["code"] != "delete_guard_required" {
		t.Fatalf("unguarded delete = %#v", unguarded)
	}
	data := unguarded["data"].(map[string]any)
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal("refused delete removed the file")
	}
	stale := lifecycleEdit(t, handlers, workspaceID, "rm2", map[string]any{"kind": "delete_file", "path": "gone.txt", "expected_sha256": "0000"})
	if stale["outcome"] != "conflict" || stale["code"] != "delete_target_changed" {
		t.Fatalf("stale hash = %#v", stale)
	}
	// A revision this service never issued is refused with the one it holds,
	// in a field and in a follow-up that spells out the retry. The sentence
	// named it before; a caller had to parse prose or read the file again.
	wrong := lifecycleEdit(t, handlers, workspaceID, "rm2b", map[string]any{"kind": "delete_file", "path": "gone.txt", "revision_id": "docrev_wrong"})
	if wrong["outcome"] != "conflict" {
		t.Fatalf("wrong revision = %#v", wrong)
	}
	if got := wrong["data"].(map[string]any)["document_revision"]; got != data["revision_id"] {
		t.Fatalf("refusal offers %#v as the current revision, want %#v", got, data["revision_id"])
	}
	retry, _ := wrong["next"].([]any)[0].(map[string]any)
	if retry["revision_id"] != data["revision_id"] || retry["path"] != "gone.txt" {
		t.Fatalf("the retry step does not carry the current revision: %#v", wrong["next"])
	}
	guarded := lifecycleEdit(t, handlers, workspaceID, "rm3", map[string]any{"kind": "delete_file", "path": "gone.txt", "revision_id": data["revision_id"]})
	if guarded["outcome"] == "conflict" || guarded["outcome"] == "failed" {
		t.Fatalf("guarded delete = %#v", guarded)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("file survived the delete: %v", err)
	}
	byHash := lifecycleEdit(t, handlers, workspaceID, "rm4", map[string]any{"kind": "delete_file", "path": "other.txt", "expected_sha256": contentHash([]byte("x\n"))})
	if byHash["outcome"] == "conflict" || byHash["outcome"] == "failed" {
		t.Fatalf("delete by hash = %#v", byHash)
	}
	missing := lifecycleEdit(t, handlers, workspaceID, "rm5", map[string]any{"kind": "delete_file", "path": "other.txt", "expected_sha256": "0"})
	if missing["code"] != "delete_target_missing" {
		t.Fatalf("delete of a missing file = %#v", missing)
	}
}

// A new file whose parent directory does not exist yet is written, parent
// and all. There is no directory operation in the API, so the alternative is
// a shell mkdir in the middle of a Huyang edit, which is what two unattended
// runs did before this: create_file failed on the temporary file it writes
// beside the target, with no next step.
func TestCreateFileMakesItsParentDirectory(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"go.mod": "module fixture\n"})
	created := lifecycleEdit(t, handlers, workspaceID, "mkparent", map[string]any{
		"kind": "create_file", "path": "docs/friction/note.md", "content": "written in one call\n",
	})
	if created["outcome"] == "conflict" || created["outcome"] == "failed" {
		t.Fatalf("create into a missing directory = %#v", created)
	}
	content, err := os.ReadFile(filepath.Join(root, "docs", "friction", "note.md"))
	if err != nil || string(content) != "written in one call\n" {
		t.Fatalf("file after create = %q, %v", content, err)
	}
}

// create_file on an existing path is refused with the revision that a
// replace needs; with replace and that revision the file is overwritten in
// one call, and a stale revision is refused.
func TestCreateFileReplaceIsGuardedByRevision(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"cfg.txt": "v1\n"})
	refused := lifecycleEdit(t, handlers, workspaceID, "cr1", map[string]any{"kind": "create_file", "path": "cfg.txt", "content": "v2\n"})
	if refused["code"] != "create_target_exists" || refused["data"].(map[string]any)["revision_id"] == nil {
		t.Fatalf("create over existing = %#v", refused)
	}
	noRevision := lifecycleEdit(t, handlers, workspaceID, "cr2", map[string]any{"kind": "create_file", "path": "cfg.txt", "content": "v2\n", "replace": true})
	if noRevision["code"] != "replace_revision_required" {
		t.Fatalf("replace without revision = %#v", noRevision)
	}
	revision := refused["data"].(map[string]any)["revision_id"]
	replaced := lifecycleEdit(t, handlers, workspaceID, "cr3", map[string]any{"kind": "create_file", "path": "cfg.txt", "content": "v2\n", "replace": true, "revision_id": revision})
	if replaced["outcome"] == "conflict" || replaced["outcome"] == "failed" {
		t.Fatalf("replace = %#v", replaced)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "cfg.txt")); string(content) != "v2\n" {
		t.Fatalf("replaced content = %q", content)
	}
	stale := lifecycleEdit(t, handlers, workspaceID, "cr4", map[string]any{"kind": "create_file", "path": "cfg.txt", "content": "v3\n", "replace": true, "revision_id": revision})
	if stale["outcome"] != "conflict" {
		t.Fatalf("stale replace = %#v", stale)
	}
}

// The operations list carries lifecycle kinds alongside literal edits; a
// move followed by an edit of the destination composes.
func TestOperationsListCarriesLifecycleKinds(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"a.txt": "one\n", "tmp.txt": "scratch\n"})
	snapshot := handlers.Execute(context.Background(), "req_read", "read", map[string]any{"workspace_id": workspaceID, "target": map[string]any{"path": "tmp.txt"}})
	tmpRevision := snapshot["data"].(map[string]any)["revision_id"]
	result := handlers.Execute(context.Background(), "req_ops", "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "ops",
		"operations": []any{
			map[string]any{"kind": "move_file", "from": "a.txt", "to": "b.txt"},
			map[string]any{"kind": "replace_literal", "path": "b.txt", "old": "one", "new": "two"},
			map[string]any{"kind": "copy_file", "from": "b.txt", "to": "c.txt"},
			map[string]any{"kind": "delete_file", "path": "tmp.txt", "revision_id": tmpRevision},
		},
	})
	if result["outcome"] == "conflict" || result["outcome"] == "failed" {
		t.Fatalf("operations = %#v", result)
	}
	for name, want := range map[string]string{"b.txt": "two\n", "c.txt": "two\n"} {
		if content, _ := os.ReadFile(filepath.Join(root, name)); string(content) != want {
			t.Fatalf("%s = %q", name, content)
		}
	}
	for _, gone := range []string{"a.txt", "tmp.txt"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists", gone)
		}
	}
	if !strings.Contains(result["summary"].(string), "Applied 4 operations") {
		t.Fatalf("summary = %q", result["summary"])
	}
}

// Without a workspace, a copy into a directory that is no repository is
// one call: the destination opens an implicit documents workspace and the
// source is read from wherever it is.
func TestCopyFileWithoutWorkspaceOpensTheDestinationImplicitly(t *testing.T) {
	source := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(source, []byte("# rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "checkout", "CLAUDE.md")
	backend := &stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: filepath.Dir(destination)}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	result := handlers.Execute(context.Background(), "req_icp", "edit_apply", map[string]any{
		"idempotency_key": "icp", "operation": map[string]any{"kind": "copy_file", "from": source, "to": destination},
	})
	if result["outcome"] == "conflict" || result["outcome"] == "failed" {
		t.Fatalf("implicit copy = %#v", result)
	}
	if content, _ := os.ReadFile(destination); string(content) != "# rules\n" {
		t.Fatalf("copied = %q", content)
	}
	if result["data"].(map[string]any)["implicit_workspace"] != true {
		t.Fatalf("implicit copy did not say so: %#v", result)
	}
}
