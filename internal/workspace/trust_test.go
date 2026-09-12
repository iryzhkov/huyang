package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TrustRoot creates the policy when absent, appends to an existing
// multi-line array without disturbing comments or other tables, expands a
// single-line array, refuses a duplicate and a non-directory, and
// UntrustRoot removes exactly the listed line.
func TestTrustRootEditsThePolicyTextually(t *testing.T) {
	config := filepath.Join(t.TempDir(), "huyang", "config.toml")
	first := t.TempDir()
	path, resolved, err := TrustRoot(config, first)
	if err != nil || path != config {
		t.Fatalf("first trust: %v (%s)", err, path)
	}
	roots, err := TrustedRoots(config)
	if err != nil || len(roots) != 1 || roots[0] != resolved {
		t.Fatalf("roots after create = %v, %v", roots, err)
	}
	if info, _ := os.Stat(config); info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v", info.Mode())
	}
	if _, _, err := TrustRoot(config, first); err == nil || !strings.Contains(err.Error(), "already trusted") {
		t.Fatalf("duplicate trust error = %v", err)
	}
	nested := filepath.Join(first, "child")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TrustRoot(config, nested); err == nil || !strings.Contains(err.Error(), "already trusted through") {
		t.Fatalf("child of trusted root error = %v", err)
	}
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TrustRoot(config, file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file trust error = %v", err)
	}
	// A hand-written policy with comments, a trailing element without a
	// comma and another table keeps all of it.
	second, third := t.TempDir(), t.TempDir()
	handWritten := "# my policy\n[resource]\ntimeout_seconds = 30\n\n[trust]\nroots = [\n  " + quoteTOML(second) + "\n  # keep this comment\n]\n"
	if err := os.WriteFile(config, []byte(handWritten), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TrustRoot(config, third); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(config)
	if !strings.Contains(string(content), "# my policy") || !strings.Contains(string(content), "# keep this comment") || !strings.Contains(string(content), "timeout_seconds = 30") {
		t.Fatalf("edit lost user content:\n%s", content)
	}
	roots, _ = TrustedRoots(config)
	if len(roots) != 2 {
		t.Fatalf("roots after append = %v\n%s", roots, content)
	}
	// A single-line array is expanded.
	if err := os.WriteFile(config, []byte("[trust]\nroots = ["+quoteTOML(second)+"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TrustRoot(config, third); err != nil {
		t.Fatal(err)
	}
	if roots, _ = TrustedRoots(config); len(roots) != 2 {
		content, _ := os.ReadFile(config)
		t.Fatalf("roots after expanding = %v\n%s", roots, content)
	}
	if _, err := UntrustRoot(config, second); err != nil {
		t.Fatal(err)
	}
	if roots, _ = TrustedRoots(config); len(roots) != 1 || roots[0] != third {
		t.Fatalf("roots after untrust = %v", roots)
	}
	if _, err := UntrustRoot(config, second); err == nil {
		t.Fatal("untrusting an unlisted root succeeded")
	}
}
