package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// Two identical spans in one file: after the later one is replaced, the
// earlier handle still resolves to its own occurrence because its offset
// and the bytes before it are unchanged, instead of being refused as
// ambiguous just because the bytes after it moved.
func TestHandleAtUnchangedOffsetSurvivesNeighbouringEdit(t *testing.T) {
	workspace := literalWorkspace(t, map[string]string{"sum.go": "var total int\ntotal += 1\nreturn total\n"})
	result, err := workspace.Search(SearchRequest{Query: "total", Mode: "literal"})
	if err != nil || len(result.Hits) != 3 {
		t.Fatalf("search = %d hits, %v", len(result.Hits), err)
	}
	last := result.Hits[2]
	if _, _, err := workspace.ApplyReplace(workspace.Identity().ID, last.Range, []byte("sum")); err != nil {
		t.Fatal(err)
	}
	resolution, err := workspace.ResolveHandle(result.Hits[1].MatchHandle.Handle)
	if err != nil || resolution.Status == ResolutionConflicted || resolution.Current == nil || resolution.Current.ByteStart != result.Hits[1].ByteStart {
		t.Fatalf("earlier handle after a later edit = %#v, %v", resolution, err)
	}
	handle, _ := resolution.RangeHandle()
	if _, _, err := workspace.ApplyReplace(workspace.Identity().ID, handle, []byte("sum")); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(workspace.Identity().Root, "sum.go"))
	if string(content) != "var total int\nsum += 1\nreturn sum\n" {
		t.Fatalf("file = %q", content)
	}
}
