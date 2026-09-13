package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestCompactOutlineAndNumberedContinuation(t *testing.T) {
	h, id, _ := literalFixture(t, map[string]string{"a.go": "package p\nfunc Large() {}\n"})
	args := map[string]any{"workspace_id": id, "target": map[string]any{"path": "a.go"}, "view": "outline", "response_mode": "compact"}
	if result := h.Execute(context.Background(), "outline", "read", args); result["outcome"] != "ok" {
		t.Fatalf("%#v", result)
	}
	args["view"], args["numbered"], args["max_bytes"] = "source", true, 4
	args["start_line"], args["end_line"] = 2, 2
	var joined strings.Builder
	for {
		result := h.Execute(context.Background(), "numbered", "read", args)
		if result["outcome"] != "ok" {
			t.Fatalf("%#v", result)
		}
		data := result["data"].(map[string]any)
		joined.WriteString(data["content"].(string))
		if data["byte_truncated"] != true {
			break
		}
		args["byte_offset"], args["expected_revision_id"] = data["next_byte_offset"], fmt.Sprint(data["revision_id"])
	}
	if joined.String() != "2\tfunc Large() {}\n" {
		t.Fatalf("%q", joined.String())
	}
}

func TestPreparedByteReadUsesStagedContentAndRevision(t *testing.T) {
	h, id, _ := literalFixture(t, map[string]string{"a.txt": "canonical"})
	w := h.registry.Lookup(workspacecore.ID(id))
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "a.txt"), []byte("staged-content"), 0600); err != nil {
		t.Fatal(err)
	}
	view := providerpool.PreparedView{Tree: tree, PreparedRevision: "prep_1234567890123456", PlanID: "plan_bytes", PlanRevision: 1}
	request := readRequest{Path: "a.txt", HasPath: true, MaxBytes: 4}
	read := func(r readRequest) map[string]any { return h.readPrepared("prepared", w, view, r) }
	first := readWithByteWindow("prepared", w, request, read)
	data := first["data"].(map[string]any)
	if data["content"] != "stag" || data["revision_id"] != view.PreparedRevision {
		t.Fatalf("%#v", first)
	}
	request.ByteOffset, request.ExpectedRevision = 4, view.PreparedRevision
	second := readWithByteWindow("prepared", w, request, read)
	if second["data"].(map[string]any)["content"] != "ed-c" {
		t.Fatalf("%#v", second)
	}
	request.ExpectedRevision = "prep_old"
	if result := readWithByteWindow("prepared", w, request, read); result["code"] != "read_revision_changed" {
		t.Fatalf("%#v", result)
	}
}
