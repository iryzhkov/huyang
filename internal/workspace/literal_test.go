package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func literalWorkspace(t *testing.T, files map[string]string) *Workspace {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workspace, err := Open(OpenOptions{Kind: KindProject, Root: root, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return workspace
}

// An exact occurrence is found with its line and column, in one file or
// across the workspace, and never reported as normalised.
func TestLocateLiteralFindsExactOccurrences(t *testing.T) {
	workspace := literalWorkspace(t, map[string]string{
		"a.go": "package a\n\nconst Max = 10\n", "b.go": "package a\n\nvar limit = Max\n",
	})
	match, err := workspace.LocateLiteral("", "Max")
	if err != nil || match.Normalised || len(match.Hits) != 2 {
		t.Fatalf("workspace-wide literal = %#v, %v", match, err)
	}
	if match.Hits[0].Path != "a.go" || match.Hits[0].Line != 3 || match.Hits[0].Column != 7 {
		t.Fatalf("first hit = %#v", match.Hits[0])
	}
	scoped, err := workspace.LocateLiteral("b.go", "Max")
	if err != nil || len(scoped.Hits) != 1 || scoped.Hits[0].Path != "b.go" {
		t.Fatalf("scoped literal = %#v, %v", scoped, err)
	}
}

// Text that differs from the document only by a uniform indentation is
// found, reported as normalised, and the prefix lets the replacement be
// adjusted the same way.
func TestLocateLiteralAdaptsUniformIndentation(t *testing.T) {
	workspace := literalWorkspace(t, map[string]string{
		"main.go": "func f() {\n\tif a {\n\t\treturn 1\n\t}\n}\n",
	})
	match, err := workspace.LocateLiteral("main.go", "if a {\n\treturn 1\n}\n")
	if err != nil || !match.Normalised || len(match.Hits) != 1 {
		t.Fatalf("normalised literal = %#v, %v", match, err)
	}
	if match.IndentPrefix != "\t" || !match.IndentAdded {
		t.Fatalf("indentation delta = %q added=%v hint=%s", match.IndentPrefix, match.IndentAdded, match.Hint)
	}
	if got := match.AdjustIndentation("if b {\n\treturn 2\n}\n"); got != "\tif b {\n\t\treturn 2\n\t}\n" {
		t.Fatalf("adjusted replacement = %q", got)
	}
	content, _ := os.ReadFile(filepath.Join(workspace.Identity().Root, "main.go"))
	if string(content[match.Hits[0].ByteStart:match.Hits[0].ByteEnd]) != "\tif a {\n\t\treturn 1\n\t}\n" {
		t.Fatalf("matched bytes = %q", content[match.Hits[0].ByteStart:match.Hits[0].ByteEnd])
	}
}

// Spaces against tabs is not a uniform prefix: the match is reported with
// the document's exact text and a hint, and no prefix is offered.
func TestLocateLiteralReportsTabSpaceMismatch(t *testing.T) {
	workspace := literalWorkspace(t, map[string]string{
		"main.go": "func f() {\n\tif a {\n\t\treturn 1\n\t}\n}\n",
	})
	match, err := workspace.LocateLiteral("main.go", "    if a {\n        return 1\n    }\n")
	if err != nil || !match.Normalised || len(match.Hits) != 1 || match.IndentPrefix != "" {
		t.Fatalf("mismatch = %#v, %v", match, err)
	}
	if !strings.Contains(match.Hint, "tabs") || match.Actual != "\tif a {\n\t\treturn 1\n\t}\n" {
		t.Fatalf("hint = %q actual = %q", match.Hint, match.Actual)
	}
}

// Occurrences are replaced later-first so earlier offsets stay valid, the
// after image is the whole file, and the patch names every occurrence.
func TestReplaceLiteralReplacesEveryOccurrenceInOrder(t *testing.T) {
	workspace := literalWorkspace(t, map[string]string{
		"sum.go": "func Sum() int {\n\tvar total int\n\ttotal += 1\n\treturn total\n}\n",
	})
	match, err := workspace.LocateLiteral("sum.go", "total")
	if err != nil || len(match.Hits) != 3 {
		t.Fatalf("hits = %#v, %v", match, err)
	}
	change, err := workspace.ReplaceLiteral(workspace.Identity().ID, match.Hits, []byte("sum"), false)
	if err != nil || change.Replacements != 3 || len(change.Files) != 1 {
		t.Fatalf("change = %#v, %v", change, err)
	}
	want := "func Sum() int {\n\tvar sum int\n\tsum += 1\n\treturn sum\n}\n"
	content, _ := os.ReadFile(filepath.Join(workspace.Identity().Root, "sum.go"))
	if string(content) != want || string(change.Files[0].After) != want {
		t.Fatalf("after = %q, disk = %q", change.Files[0].After, content)
	}
	if strings.Count(change.Files[0].Patch, "@@ bytes") != 3 || change.Revisions["sum.go"] == "" {
		t.Fatalf("patch/revision = %q %#v", change.Files[0].Patch, change.Revisions)
	}
}

// A preview simulates the after image without touching the file.
func TestReplaceLiteralPreviewLeavesBytesAlone(t *testing.T) {
	workspace := literalWorkspace(t, map[string]string{"a.txt": "one two one\n"})
	match, _ := workspace.LocateLiteral("a.txt", "one")
	change, err := workspace.ReplaceLiteral(workspace.Identity().ID, match.Hits, []byte("1"), true)
	if err != nil || string(change.Files[0].After) != "1 two 1\n" {
		t.Fatalf("preview = %#v, %v", change, err)
	}
	content, _ := os.ReadFile(filepath.Join(workspace.Identity().Root, "a.txt"))
	if string(content) != "one two one\n" {
		t.Fatalf("preview changed the file: %q", content)
	}
}
