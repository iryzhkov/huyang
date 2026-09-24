package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// One read call can carry several targets; each answers with its own
// content and revision, and a bad target is reported in place.
func TestReadTargetsAnswersSeveralFilesInOneCall(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{
		"a.go": "package p\n\nfunc A() {}\n", "b.go": "package p\n\n// B does b.\nfunc B() {}\n\nfunc C() {}\n",
	})
	result := handlers.Execute(context.Background(), "req_many", "read", map[string]any{
		"workspace_id": workspaceID,
		"targets": []any{
			map[string]any{"path": "a.go"},
			map[string]any{"path": "b.go", "start_line": 3, "end_line": 4},
			map[string]any{"symbol_locator": map[string]any{"path": "b.go", "name_path": "C"}},
			map[string]any{"path": "missing.go"},
		},
	})
	if result["outcome"] != "partial" {
		t.Fatalf("multi read = %#v", result)
	}
	files := result["data"].(map[string]any)["files"].([]map[string]any)
	if len(files) != 4 || files[0]["content"] != "package p\n\nfunc A() {}\n" || files[0]["revision_id"] == nil {
		t.Fatalf("files = %#v", files)
	}
	if files[1]["content"] != "// B does b.\nfunc B() {}\n" || files[1]["start_line"] != 3 {
		t.Fatalf("line window = %#v", files[1])
	}
	if files[2]["content"] != "func C() {}" || files[2]["name_path"] != "C" {
		t.Fatalf("symbol target = %#v", files[2])
	}
	if files[3]["code"] != "document_not_found" || files[3]["path"] != "missing.go" {
		t.Fatalf("missing target = %#v", files[3])
	}
}

// A read of a path that is not there names the paths the caller probably
// meant: the same base name first, the one sharing more of the requested
// directories ahead, then near spellings. Every view answers it the same way,
// a symbol read blames the path rather than a missing parser, and a
// multi-target read keeps each failed target's candidates and raises the
// first recovery to the top.
func TestReadOfAMissingPathOffersCandidates(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{
		"cmd/read.go": "package cmd\n", "pkg/handlers/read.go": "package handlers\n\nfunc Read() {}\n",
		"pkg/handlers/store.go": "package handlers\n", "notes.txt": "notes\n",
	})
	read := func(target map[string]any) map[string]any {
		return handlers.Execute(context.Background(), "req_missing", "read", map[string]any{"workspace_id": workspaceID, "target": target})
	}
	candidatesOf := func(result map[string]any) []string {
		candidates, _ := result["data"].(map[string]any)["candidates"].([]string)
		return candidates
	}
	moved := read(map[string]any{"path": "handlers/read.go"})
	if moved["code"] != "document_not_found" || strings.Contains(moved["summary"].(string), root) {
		t.Fatalf("missing path = %#v", moved)
	}
	if candidates := candidatesOf(moved); len(candidates) != 2 || candidates[0] != "pkg/handlers/read.go" || candidates[1] != "cmd/read.go" {
		t.Fatalf("candidates = %#v", moved)
	}
	if _, present := moved["data"].(map[string]any)["coverage"]; !present {
		t.Fatalf("absence is claimed without the listing it was checked against: %#v", moved)
	}
	if next := moved["next"].([]any); len(next) == 0 || next[0].(map[string]any)["path"] != "pkg/handlers/read.go" {
		t.Fatalf("next = %#v", moved["next"])
	}
	if misspelled := read(map[string]any{"path": "pkg/handlers/stor.go"}); len(candidatesOf(misspelled)) != 1 || candidatesOf(misspelled)[0] != "pkg/handlers/store.go" {
		t.Fatalf("near spelling = %#v", misspelled)
	}
	if outline := handlers.Execute(context.Background(), "req_missing_outline", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "handlers/read.go"}, "view": "outline",
	}); outline["code"] != "document_not_found" || len(candidatesOf(outline)) != 2 {
		t.Fatalf("missing outline = %#v", outline)
	}
	symbol := read(map[string]any{"symbol_locator": map[string]any{"path": "handlers/read.go", "name_path": "Read"}})
	if symbol["code"] != "document_not_found" || len(candidatesOf(symbol)) != 2 {
		t.Fatalf("symbol read of a missing file = %#v", symbol)
	}
	locator, _ := symbol["next"].([]any)[0].(map[string]any)["target"].(map[string]any)["symbol_locator"].(map[string]any)
	if locator["path"] != "pkg/handlers/read.go" || locator["name_path"] != "Read" {
		t.Fatalf("symbol recovery = %#v", symbol["next"])
	}
	if nowhere := read(map[string]any{"path": "zzz/absent.rs"}); nowhere["code"] != "document_not_found" || !strings.Contains(nowhere["summary"].(string), "anywhere in the workspace") {
		t.Fatalf("absent everywhere = %#v", nowhere)
	}
	many := handlers.Execute(context.Background(), "req_missing_many", "read", map[string]any{
		"workspace_id": workspaceID,
		"targets":      []any{map[string]any{"path": "notes.txt"}, map[string]any{"path": "handlers/read.go"}},
	})
	files := many["data"].(map[string]any)["files"].([]map[string]any)
	if candidates, _ := files[1]["candidates"].([]string); len(candidates) != 2 {
		t.Fatalf("failed target lost its candidates: %#v", files[1])
	}
	if next := many["next"].([]any); len(next) == 0 || next[0].(map[string]any)["path"] != "pkg/handlers/read.go" {
		t.Fatalf("multi-target next = %#v", many["next"])
	}
}

// After the exact base names come files of the same stem - a test beside
// the source, the same name in another language - first in a directory of
// the requested name, then elsewhere, and only then near spellings. The
// whole list is still capped.
func TestMissingPathCandidatesRankSameStemAfterExactNames(t *testing.T) {
	listed := []string{
		"other/store_test.go", "pkg/handlers/store_test.go", "web/handlers/store.test.ts",
		"pkg/handlers/stare.go", "lib/store.go", "web/store.spec.ts", "pkg/handlers/unrelated.go",
	}
	named, near := pathCandidates(listed, "src/handlers/store.go")
	if len(named) != 1 || named[0] != "lib/store.go" {
		t.Fatalf("named = %#v", named)
	}
	want := []string{"pkg/handlers/store_test.go", "web/handlers/store.test.ts", "other/store_test.go", "web/store.spec.ts", "pkg/handlers/stare.go"}
	if strings.Join(near, " ") != strings.Join(want, " ") {
		t.Fatalf("near = %#v, want %#v", near, want)
	}
	handlers, workspaceID, _ := literalFixture(t, map[string]string{"pkg/handlers/store_test.go": "package handlers\n"})
	result := handlers.Execute(context.Background(), "req_stem", "read", map[string]any{"workspace_id": workspaceID, "target": map[string]any{"path": "pkg/handlers/store.go"}})
	if candidates, _ := result["data"].(map[string]any)["candidates"].([]string); len(candidates) != 1 || candidates[0] != "pkg/handlers/store_test.go" {
		t.Fatalf("a source whose test exists offers = %#v", result)
	}
}

// A listing stopped at its cap cannot say a file exists nowhere, so absence
// is worded within the files it did list.
func TestMissingPathAbsenceIsWordedWithinACappedListing(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workspace, err := workspacecore.Open(workspacecore.OpenOptions{Kind: workspacecore.KindProject, Root: root, StateDir: t.TempDir(), Limits: workspacecore.Limits{MaxFiles: 1}})
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := workspace.Read("absent.go")
	result := readFailure("req_capped", workspace, readErr, "")
	summary, _ := result["summary"].(string)
	if result["code"] != "document_not_found" || strings.Contains(summary, "anywhere") || !strings.Contains(summary, "among the 1 files listed") {
		t.Fatalf("absence under a capped listing = %#v", result)
	}
}

// max_lines caps a delivery, the reply says so with the total and the
// next line to continue from, and a multi-target reply lists every
// target's size ahead of the bodies.
func TestReadMaxLinesTruncatesAndListsEntries(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{
		"big.txt": "l1\nl2\nl3\nl4\nl5\n", "small.txt": "one\n",
	})
	single := handlers.Execute(context.Background(), "req_cap", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "big.txt"}, "max_lines": 2, "numbered": true,
	})
	data := single["data"].(map[string]any)
	if data["content"] != "1\tl1\n2\tl2\n" || data["truncated"] != true || data["lines"] != 5 || data["delivered_end_line"] != 2 {
		t.Fatalf("capped read = %#v", data)
	}
	if next := single["next"].([]any); len(next) != 2 || next[1].(map[string]any)["start_line"] != 3 {
		t.Fatalf("capped read next = %#v", single["next"])
	}
	if !strings.Contains(single["summary"].(string), "truncated at max_lines") {
		t.Fatalf("capped read summary = %q", single["summary"])
	}
	uncapped := handlers.Execute(context.Background(), "req_uncapped", "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "big.txt"}, "max_lines": 5,
	})
	if _, present := uncapped["data"].(map[string]any)["truncated"]; present {
		t.Fatalf("read within the cap reports truncation: %#v", uncapped)
	}
	many := handlers.Execute(context.Background(), "req_many_cap", "read", map[string]any{
		"workspace_id": workspaceID, "max_lines": 2,
		"targets": []any{map[string]any{"path": "big.txt"}, map[string]any{"path": "small.txt"}, map[string]any{"path": "big.txt", "max_lines": 4}},
	})
	manyData := many["data"].(map[string]any)
	entries := manyData["entries"].([]map[string]any)
	if len(entries) != 3 || entries[0]["lines"] != 5 || entries[0]["truncated"] != true || entries[1]["path"] != "small.txt" || entries[1]["truncated"] != nil {
		t.Fatalf("entries = %#v", entries)
	}
	files := manyData["files"].([]map[string]any)
	if files[0]["content"] != "l1\nl2\n" || files[2]["content"] != "l1\nl2\nl3\nl4\n" {
		t.Fatalf("per-target cap = %#v", files)
	}
	if !strings.Contains(many["summary"].(string), "2 truncated") {
		t.Fatalf("multi summary = %q", many["summary"])
	}
}

// Every option of a single-target read applies per target, so one call can
// outline one file while windowing another.
func TestReadTargetsAcceptPerTargetViewAndNumbering(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{
		"a.go": "package p\n\nfunc A() {}\n\nfunc B() {}\n", "notes.txt": "one\ntwo\nthree\n",
	})
	result := handlers.Execute(context.Background(), "req_views", "read", map[string]any{
		"workspace_id": workspaceID,
		"targets": []any{
			map[string]any{"path": "a.go", "view": "outline"},
			map[string]any{"path": "notes.txt", "start_line": 1, "end_line": 2, "numbered": true},
		},
	})
	if result["outcome"] != "ok" {
		t.Fatalf("mixed views = %#v", result)
	}
	files := result["data"].(map[string]any)["files"].([]map[string]any)
	sections, ok := files[0]["sections"].([]map[string]any)
	if !ok || len(sections) != 2 || sections[0]["name"] != "A" {
		t.Fatalf("outline target = %#v", files[0])
	}
	if files[1]["content"] != "1\tone\n2\ttwo\n" {
		t.Fatalf("numbered window = %#v", files[1])
	}
}

// A Go declaration is read by name natively, without a provider, and the
// response is compact: content, revision, handle and line span.
func TestReadSymbolResolvesNativelyForGo(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{
		"report.go": "package p\n\nfunc a() {}\n\n// SummarizeQuarter aggregates.\nfunc SummarizeQuarter() int {\n\treturn 1\n}\n",
	})
	result := handlers.Execute(context.Background(), "req_symbol", "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       map[string]any{"symbol_locator": map[string]any{"path": "report.go", "name_path": "SummarizeQuarter"}},
	})
	if result["outcome"] != "ok" {
		t.Fatalf("symbol read = %#v", result)
	}
	data := result["data"].(map[string]any)
	if !strings.HasPrefix(data["content"].(string), "// SummarizeQuarter aggregates.\nfunc SummarizeQuarter()") || data["start_line"] != 5 || data["end_line"] != 8 {
		t.Fatalf("symbol data = %#v", data)
	}
	if _, present := data["snapshot"]; present {
		t.Fatalf("symbol read carries the full snapshot: %#v", data)
	}
	if data["handle"] == nil {
		t.Fatalf("symbol read lacks an editable handle: %#v", data)
	}
}
