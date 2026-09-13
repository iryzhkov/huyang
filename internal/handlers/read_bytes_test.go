package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadByteWindowsReassembleMinifiedUTF8(t *testing.T) {
	source := strings.Repeat("a😀é", 20000)
	h, id, _ := literalFixture(t, map[string]string{"min.json": source})
	args := map[string]any{"workspace_id": id, "target": map[string]any{"path": "min.json"}, "max_lines": 1, "max_bytes": 1024}
	var joined strings.Builder
	for {
		result := h.Execute(context.Background(), "byte-window", "read", args)
		if result["outcome"] != "ok" {
			t.Fatalf("read: %#v", result)
		}
		data := result["data"].(map[string]any)
		chunk := data["content"].(string)
		if len(chunk) > 1024 || !utf8.ValidString(chunk) {
			t.Fatal("invalid byte bound or UTF-8")
		}
		joined.WriteString(chunk)
		if data["byte_truncated"] != true {
			break
		}
		args["byte_offset"] = data["next_byte_offset"]
		args["expected_revision_id"] = fmt.Sprint(data["revision_id"])
	}
	if joined.String() != source {
		t.Fatal("continuation lost or repeated bytes")
	}
}

func TestReadByteWindowRefusesChangedRevisionAndMissingGuard(t *testing.T) {
	h, id, root := literalFixture(t, map[string]string{"data.txt": "abcdefghijklmnop"})
	args := map[string]any{"workspace_id": id, "target": map[string]any{"path": "data.txt"}, "max_bytes": 4}
	first := h.Execute(context.Background(), "first", "read", args)["data"].(map[string]any)
	args["byte_offset"] = 4
	if result := h.Execute(context.Background(), "missing-guard", "read", args); result["code"] != "read_revision_required" {
		t.Fatalf("%#v", result)
	}
	args["expected_revision_id"] = fmt.Sprint(first["revision_id"])
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("changed content!"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := h.Execute(context.Background(), "changed", "read", args); result["code"] != "read_revision_changed" {
		t.Fatalf("%#v", result)
	}
}

func TestReadByteBoundsCoverSymbolsAndManyTargets(t *testing.T) {
	source := "package p\nfunc Large() { /*" + strings.Repeat("x", 200) + "*/ }\n"
	h, id, _ := literalFixture(t, map[string]string{"a.go": source, "min.txt": strings.Repeat("z", 100)})
	args := map[string]any{"workspace_id": id, "max_bytes": 16, "targets": []any{
		map[string]any{"symbol_locator": map[string]any{"path": "a.go", "name_path": "Large"}},
		map[string]any{"path": "min.txt", "max_bytes": 8},
	}}
	result := h.Execute(context.Background(), "many-bytes", "read", args)
	files := result["data"].(map[string]any)["files"].([]map[string]any)
	for i, limit := range []int{16, 8} {
		if len(files[i]["content"].(string)) > limit || files[i]["byte_truncated"] != true || files[i]["continuation"] == nil {
			t.Fatalf("%#v", files[i])
		}
	}
}

func TestReadByteWindowValidatesOffsetsAndViews(t *testing.T) {
	h, id, _ := literalFixture(t, map[string]string{"min.txt": "😀abcdefgh"})
	base := map[string]any{"workspace_id": id, "target": map[string]any{"path": "min.txt"}, "max_bytes": 4}
	data := h.Execute(context.Background(), "first", "read", base)["data"].(map[string]any)
	base["expected_revision_id"] = fmt.Sprint(data["revision_id"])
	for _, offset := range []int{-1, 1, 100} {
		base["byte_offset"] = offset
		if result := h.Execute(context.Background(), "offset", "read", base); result["outcome"] == "ok" {
			t.Fatalf("accepted offset %d", offset)
		}
	}
	base["byte_offset"], base["view"] = 0, "outline"
	if result := h.Execute(context.Background(), "outline", "read", base); result["code"] != "byte_window_requires_source" {
		t.Fatalf("%#v", result)
	}
}

func TestCompactReadDefaultAndRevisionOnlyInspect(t *testing.T) {
	h, id, root := literalFixture(t, map[string]string{"min.txt": strings.Repeat("x", DefaultReadBytes+100)})
	result := h.Execute(context.Background(), "compact", "read", map[string]any{"workspace_id": id, "target": map[string]any{"path": "min.txt"}, "response_mode": "compact"})
	data := result["data"].(map[string]any)
	if len(data["content"].(string)) != DefaultReadBytes || data["byte_truncated"] != true {
		t.Fatal("compact read is unbounded")
	}
	// A revision lookup must neither load pipeline configuration nor depend on its validity.
	if err := os.WriteFile(filepath.Join(root, ".huyang.toml"), []byte("["), 0600); err != nil {
		t.Fatal(err)
	}
	result = h.Execute(context.Background(), "revision", "workspace_inspect", map[string]any{"workspace_id": id, "view": "revision"})
	if result["outcome"] != "ok" {
		t.Fatalf("%#v", result)
	}
	data = result["data"].(map[string]any)
	if len(data) != 1 || data["revision"] == nil {
		t.Fatalf("verbose revision: %#v", data)
	}
}
