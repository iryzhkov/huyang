package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterSymbolHandleFromProviderRange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	content := []byte("package main\n\nfunc Answer() int { return 42 }\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(OpenOptions{
		Kind: KindProject, Root: root, StateDir: t.TempDir(),
		Limits: Limits{MaxFiles: 10, MaxBytes: 1 << 20, MaxDepth: 8, MaxMatches: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	start := len("package main\n\n")
	record, err := ws.RegisterSymbolHandle(path, "Answer", "function", start, len(content))
	if err != nil {
		t.Fatal(err)
	}
	if record.Kind != HandleSymbol || record.Locator.NamePath != "Answer" {
		t.Fatalf("record = %#v", record)
	}
	resolution, err := ws.ResolveHandle(record.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != ResolutionExact {
		t.Fatalf("status = %s", resolution.Status)
	}
}
