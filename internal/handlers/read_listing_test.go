package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// listingPaths reads a listing's entries as "path kind files" lines, in
// order, so a test compares one string.
func listingPaths(t *testing.T, result map[string]any) string {
	t.Helper()
	data, _ := result["data"].(map[string]any)
	entries, ok := data["entries"].([]map[string]any)
	if !ok {
		t.Fatalf("no listing in %#v", result)
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		line := fmt.Sprintf("%v %v", entry["path"], entry["kind"])
		if files, ok := entry["files"]; ok {
			line += fmt.Sprintf(" %v", files)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "; ")
}

func TestReadOfADirectoryListsItsImmediateChildren(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{
		"a.go": "package a\n", "pkg/b.go": "package pkg\n", "pkg/sub/c.go": "package sub\n",
		"pkg/sub/d.go": "package sub\n", "other/pkg/x.go": "package pkg\n",
	})
	result := handlers.Execute(context.Background(), "req_dir", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "pkg"},
	})
	if result["outcome"] != "ok" {
		t.Fatalf("directory read = %#v", result)
	}
	// other/pkg/x.go contains "pkg/" too; only the directory itself is listed.
	if got, want := listingPaths(t, result), "pkg/b.go file; pkg/sub directory 2"; got != want {
		t.Fatalf("listing = %q, want %q", got, want)
	}
	data := result["data"].(map[string]any)
	if data["kind"] != "directory" || data["entry_count"] != 2 || data["truncated"] != nil {
		t.Fatalf("listing data = %#v", data)
	}
	root := handlers.Execute(context.Background(), "req_root", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "."},
	})
	if got, want := listingPaths(t, root), "a.go file; other directory 1; pkg directory 3"; got != want {
		t.Fatalf("root listing = %q, want %q (%#v)", got, want, root)
	}
}

func TestDirectoryListingIsBoundedAndSaysSo(t *testing.T) {
	files := map[string]string{}
	for index := 0; index < maxDirectoryEntries+5; index++ {
		files[fmt.Sprintf("many/f%03d.txt", index)] = "x\n"
	}
	handlers, workspaceID, _ := literalFixture(t, files)
	result := handlers.Execute(context.Background(), "req_many", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "many"},
	})
	data, _ := result["data"].(map[string]any)
	if result["outcome"] != "ok" || data["truncated"] != true || data["listed_count"] != maxDirectoryEntries || data["entry_count"] != maxDirectoryEntries+5 {
		t.Fatalf("bounded listing = %#v", data)
	}
	if !strings.Contains(fmt.Sprint(result["summary"]), "truncated") || len(mcpapi.AnySlice(result["next"])) == 0 {
		t.Fatalf("a cut listing must say so and name a narrower call: %#v", result)
	}
}

// A directory whose name is a suffix of a crowded one is still listed: the
// directory scope is an exact prefix applied before the file cap, so the
// cap's worth of files under a/pkg/ cannot crowd out pkg/.
func TestDirectoryListingIsNotStarvedByASubstringCollision(t *testing.T) {
	files := map[string]string{"pkg/only.go": "package pkg\n"}
	for index := 0; index < 2000; index++ {
		files[fmt.Sprintf("a/pkg/f%04d.go", index)] = "package pkg\n"
	}
	handlers, workspaceID, _ := literalFixture(t, files)
	result := handlers.Execute(context.Background(), "req_collision", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "pkg"},
	})
	data, _ := result["data"].(map[string]any)
	if result["outcome"] != "ok" || listingPaths(t, result) != "pkg/only.go file" || data["entry_count"] != 1 || data["coverage"] != nil {
		t.Fatalf("listing of pkg beside 2000 files under a/pkg = %#v", data)
	}
	// The crowded directory still counts all 2000 of its files and is cut
	// only at the listing's own entry bound.
	crowded := handlers.Execute(context.Background(), "req_crowded", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "a/pkg"},
	})
	if data := crowded["data"].(map[string]any); data["entry_count"] != 2000 || data["listed_count"] != maxDirectoryEntries || data["truncated"] != true {
		t.Fatalf("listing of a/pkg = %#v", data)
	}
}

// A symlink that leaves the root is not followed into a listing, so the
// listing shows no more than search would read.
func TestDirectoryListingDoesNotFollowASymlinkOutOfTheRoot(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handlers, workspaceID, root := literalFixture(t, map[string]string{"docs/readme.md": "# r\n"})
	if err := os.Symlink(outside, filepath.Join(root, "docs", "inputs")); err != nil {
		t.Fatal(err)
	}
	result := handlers.Execute(context.Background(), "req_link", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "docs"},
	})
	if result["outcome"] != "ok" || strings.Contains(listingPaths(t, result), "secret") {
		t.Fatalf("listing followed a symlink out of the root: %#v", result)
	}
	escaped := handlers.Execute(context.Background(), "req_link_dir", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "docs/inputs/secret.txt"},
	})
	if escaped["outcome"] != "failed" {
		t.Fatalf("read through an escaping symlink = %#v", escaped)
	}
}

func TestReadTargetsListsADirectoryTargetInPlace(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{"a.go": "package a\n", "pkg/b.go": "package pkg\n"})
	result := handlers.Execute(context.Background(), "req_many_dir", "read", map[string]any{
		"workspace_id": workspaceID, "targets": []any{map[string]any{"path": "a.go"}, map[string]any{"path": "pkg"}},
	})
	data, _ := result["data"].(map[string]any)
	entries, _ := data["entries"].([]map[string]any)
	if result["outcome"] != "ok" || len(entries) != 2 || entries[1]["kind"] != "directory" || entries[1]["entry_count"] != 1 {
		t.Fatalf("batch with a directory target = %#v", result)
	}
}

func TestStartLinePastTheEndIsAnEmptyWindow(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{"three.txt": "one\ntwo\nthree\n", "empty.txt": ""})
	result := handlers.Execute(context.Background(), "req_eof", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "three.txt"}, "start_line": 10, "end_line": 20,
	})
	data, _ := result["data"].(map[string]any)
	if result["outcome"] != "ok" || data["content"] != "" || data["line_count"] != 3 || data["lines"] != 3 ||
		data["start_line"] != 10 || data["past_end"] != true || data["end_line"] != nil {
		t.Fatalf("past-the-end window = %#v", result)
	}
	if warnings := mcpapi.Warnings(result); len(warnings) != 1 || !strings.Contains(warnings[0], "past the last line, 3") {
		t.Fatalf("past-the-end warning = %#v", warnings)
	}
	batch := handlers.Execute(context.Background(), "req_eof_batch", "read", map[string]any{
		"workspace_id": workspaceID, "targets": []any{map[string]any{"path": "three.txt", "start_line": 7}},
	})
	if warnings := mcpapi.Warnings(batch); batch["outcome"] != "ok" || len(warnings) != 1 || !strings.HasPrefix(warnings[0], "three.txt: ") {
		t.Fatalf("a batch target past its end lost its warning: %#v", batch)
	}
	batchData, _ := batch["data"].(map[string]any)
	if files, _ := batchData["files"].([]map[string]any); len(files) != 1 || files[0]["line_count"] != 3 || files[0]["content"] != "" {
		t.Fatalf("a batch target past its end lost its line count: %#v", batchData)
	}
	// The last line itself is still an ordinary one-line window.
	last := handlers.Execute(context.Background(), "req_last", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "three.txt"}, "start_line": 3,
	})
	if data := last["data"].(map[string]any); data["content"] != "three\n" || data["past_end"] != nil || len(mcpapi.Warnings(last)) != 0 {
		t.Fatalf("read of the last line = %#v", last)
	}
	// An empty document has no line to start at, and says so.
	empty := handlers.Execute(context.Background(), "req_empty", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "empty.txt"}, "start_line": 1,
	})
	if data, _ := empty["data"].(map[string]any); empty["outcome"] != "ok" || data["line_count"] != 0 ||
		len(mcpapi.Warnings(empty)) != 1 || !strings.Contains(mcpapi.Warnings(empty)[0], "empty document") {
		t.Fatalf("window into an empty document = %#v", empty)
	}
	// A reversed window is still a malformed request.
	reversed := handlers.Execute(context.Background(), "req_reversed", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "three.txt"}, "start_line": 3, "end_line": 2,
	})
	if reversed["code"] != "invalid_line_range" {
		t.Fatalf("reversed window = %#v", reversed)
	}
}

func TestSymlinkEscapeRefusalNamesTheResolvedDocument(t *testing.T) {
	outside := t.TempDir()
	task := filepath.Join(outside, "task.md")
	if err := os.WriteFile(task, []byte("# task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handlers, workspaceID, root := literalFixture(t, map[string]string{"real.md": "# real\n"})
	if err := os.Symlink(outside, filepath.Join(root, "inputs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.md"), filepath.Join(root, "alias.md")); err != nil {
		t.Fatal(err)
	}
	refused := handlers.Execute(context.Background(), "req_escape", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "inputs/task.md"},
	})
	resolved, _ := filepath.EvalSymlinks(task)
	next := mcpapi.AnySlice(refused["next"])
	if refused["outcome"] != "failed" || len(next) != 1 {
		t.Fatalf("escape refusal = %#v", refused)
	}
	step := next[0].(map[string]any)
	if target, _ := step["target"].(map[string]any); target["path"] != resolved || step["workspace_id"] != nil {
		t.Fatalf("escape next = %#v, want a workspace-free read of %s", step, resolved)
	}
	// The step it names works, and opens nothing but that document.
	followed := handlers.Execute(context.Background(), "req_follow", "read", map[string]any{"target": step["target"]})
	identity, _ := followed["workspace"].(workspacecore.Identity)
	if followed["outcome"] != "ok" || identity.Kind != workspacecore.KindDocuments || followed["data"].(map[string]any)["content"] != "# task\n" {
		t.Fatalf("following the escape next = %#v", followed)
	}
	// A link inside the root that leads inside it is read by its target's name.
	aliased := handlers.Execute(context.Background(), "req_alias", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "alias.md"},
	})
	// Its next may also carry the generic hint a second single-target read
	// gets; the link's own step must be among them.
	namedTarget := false
	for _, raw := range mcpapi.AnySlice(aliased["next"]) {
		step, _ := raw.(map[string]any)
		if step["tool"] == "read" && step["action"] == "read_the_link_target_by_its_own_path" && step["path"] == "real.md" {
			namedTarget = true
		}
	}
	if aliased["outcome"] != "failed" || !namedTarget {
		t.Fatalf("in-root link refusal = %#v", aliased)
	}
}

func TestReadTargetsWithAbsolutePathsOpenOneDocumentsWorkspace(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "one.conf"), filepath.Join(dir, "nested", "two.md")
	for path, content := range map[string]string{first: "one=1\n", second: "# two\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "unnamed.txt"), []byte("u\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: dir}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	result := handlers.Execute(context.Background(), "req_targets", "read", map[string]any{
		"targets": []any{map[string]any{"path": first}, map[string]any{"path": second}},
	})
	identity, _ := result["workspace"].(workspacecore.Identity)
	data, _ := result["data"].(map[string]any)
	if result["outcome"] != "ok" || identity.Kind != workspacecore.KindDocuments || data["implicit_workspace"] != true {
		t.Fatalf("implicit batch read = %#v", result)
	}
	if entries, _ := data["files"].([]map[string]any); len(entries) != 2 || entries[0]["content"] != "one=1\n" || entries[1]["content"] != "# two\n" {
		t.Fatalf("implicit batch contents = %#v", data)
	}
	// Only the named files are in it.
	unnamed := handlers.Execute(context.Background(), "req_unnamed", "read", map[string]any{
		"workspace_id": string(identity.ID), "target": map[string]any{"path": filepath.Join(dir, "unnamed.txt")},
	})
	if unnamed["outcome"] == "ok" {
		t.Fatalf("the implicit documents workspace holds a file nobody named: %#v", unnamed)
	}
	relative := handlers.Execute(context.Background(), "req_rel", "read", map[string]any{
		"targets": []any{map[string]any{"path": first}, map[string]any{"path": "two.md"}},
	})
	if step := mcpapi.AnySlice(relative["next"]); relative["code"] != "invalid_target" || len(step) != 1 || step[0].(map[string]any)["action"] != "resend_with_root" {
		t.Fatalf("relative path in an implicit batch = %#v", relative)
	}
	directory := handlers.Execute(context.Background(), "req_dir_batch", "read", map[string]any{
		"targets": []any{map[string]any{"path": first}, map[string]any{"path": filepath.Join(dir, "nested")}},
	})
	if directory["code"] != "invalid_target" || len(mcpapi.AnySlice(directory["next"])) != 1 {
		t.Fatalf("directory in an implicit batch = %#v", directory)
	}
}

func TestReadOfAnAbsoluteDirectoryListsItThroughItsRepository(t *testing.T) {
	repository := t.TempDir()
	for _, dir := range []string{filepath.Join(repository, ".git"), filepath.Join(repository, "src")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: repository}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	result := handlers.Execute(context.Background(), "req_abs_dir", "read", map[string]any{
		"target": map[string]any{"path": filepath.Join(repository, "src")},
	})
	identity, _ := result["workspace"].(workspacecore.Identity)
	if result["outcome"] != "ok" || identity.Kind != workspacecore.KindProject || listingPaths(t, result) != "src/main.go file" {
		t.Fatalf("absolute directory read = %#v", result)
	}
	if warnings := mcpapi.Warnings(result); len(warnings) == 0 || !strings.Contains(warnings[len(warnings)-1], "listed through the repository") {
		t.Fatalf("absolute directory read did not say which workspace it used: %#v", warnings)
	}
	plain := t.TempDir()
	outside := handlers.Execute(context.Background(), "req_plain_dir", "read", map[string]any{"target": map[string]any{"path": plain}})
	if outside["code"] != "invalid_target" || len(mcpapi.AnySlice(outside["next"])) != 1 {
		t.Fatalf("directory outside any working tree = %#v", outside)
	}
}

func TestSearchWithPathsAndNoQueryListsTheFiles(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{
		"cmd/main.go": "package main\n", "cmd/tool/tool.go": "package tool\n", "lib/lib.go": "package lib\n",
	})
	result := handlers.Execute(context.Background(), "req_list", "search", map[string]any{
		"workspace_id": workspaceID, "paths": []any{"cmd/"},
	})
	data, _ := result["data"].(map[string]any)
	files, _ := data["files"].([]string)
	if result["outcome"] != "ok" || strings.Join(files, ",") != "cmd/main.go,cmd/tool/tool.go" || data["total"] != 2 {
		t.Fatalf("empty-query listing = %#v", result)
	}
	if warnings := mcpapi.Warnings(result); len(warnings) != 1 || !strings.Contains(warnings[0], "query was empty") {
		t.Fatalf("empty-query listing did not say what it did: %#v", warnings)
	}
	limited := handlers.Execute(context.Background(), "req_list_limited", "search", map[string]any{
		"workspace_id": workspaceID, "paths": []any{"*.go"}, "limit": 1,
	})
	if data := limited["data"].(map[string]any); data["returned"] != 1 || data["total"] != 3 {
		t.Fatalf("bounded empty-query listing = %#v", limited)
	}
	bare := handlers.Execute(context.Background(), "req_bare", "search", map[string]any{"workspace_id": workspaceID})
	if bare["code"] != "search_failed" || len(mcpapi.AnySlice(bare["next"])) != 2 {
		t.Fatalf("search with neither query nor paths = %#v", bare)
	}
}
