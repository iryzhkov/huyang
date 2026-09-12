package handlers

import (
	"context"
	"strings"
	"testing"
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
	if files[3]["code"] != "read_failed" || files[3]["path"] != "missing.go" {
		t.Fatalf("missing target = %#v", files[3])
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
