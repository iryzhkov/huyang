package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clientPathTree makes a directory holding one repository with a
// subdirectory, and a plain directory beside it.
func clientPathTree(t *testing.T) (repository, subdirectory, plain string) {
	t.Helper()
	base := t.TempDir()
	repository = filepath.Join(base, "repo")
	subdirectory = filepath.Join(repository, "src")
	plain = filepath.Join(base, "home")
	for _, dir := range []string{filepath.Join(repository, ".git"), subdirectory, plain} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repository, subdirectory, plain
}

func TestResolveClientPathsDefaultsToTheRepositoryTheClientStartedIn(t *testing.T) {
	repository, _, _ := clientPathTree(t)
	arguments := map[string]any{"query": "x", "paths": []any{"src"}}
	warnings := resolveClientPaths("search", arguments, repository)
	if arguments["root"] != repository {
		t.Fatalf("root = %v, want %s", arguments["root"], repository)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], repository) {
		t.Fatalf("warnings = %v, want one naming %s", warnings, repository)
	}
}

func TestResolveClientPathsResolvesARelativeRoot(t *testing.T) {
	repository, subdirectory, _ := clientPathTree(t)
	arguments := map[string]any{"root": "."}
	resolveClientPaths("workspace_open", arguments, repository)
	if arguments["root"] != repository {
		t.Fatalf("root = %v, want %s", arguments["root"], repository)
	}
	arguments = map[string]any{"root": "..", "target": map[string]any{"path": "a.go"}}
	resolveClientPaths("read", arguments, subdirectory)
	if arguments["root"] != repository {
		t.Fatalf("root from a subdirectory = %v, want %s", arguments["root"], repository)
	}
}

func TestResolveClientPathsLeavesAmbiguousCallsAlone(t *testing.T) {
	repository, subdirectory, plain := clientPathTree(t)
	cases := []struct {
		name      string
		tool      string
		arguments map[string]any
		cwd       string
	}{
		{"no client directory", "search", map[string]any{"query": "x"}, ""},
		{"client outside any repository", "search", map[string]any{"query": "x"}, plain},
		{"relative root from outside any repository", "workspace_open", map[string]any{"root": "../repo"}, plain},
		{"client in a subdirectory", "read", map[string]any{"target": map[string]any{"path": "a.go"}}, subdirectory},
		{"workspace already named", "search", map[string]any{"query": "x", "workspace_id": "ws_1"}, repository},
		{"absolute read path", "read", map[string]any{"target": map[string]any{"path": "/etc/hosts"}}, repository},
		{"absolute path among targets", "read", map[string]any{"targets": []any{map[string]any{"path": "a.go"}, map[string]any{"path": "/etc/hosts"}}}, repository},
		{"absolute edit path", "edit_apply", map[string]any{"operation": map[string]any{"kind": "create_file", "path": "/tmp/x"}}, repository},
		{"debugger", "debug_session", map[string]any{"action": "start"}, repository},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, hadRoot := tc.arguments["root"]
			if warnings := resolveClientPaths(tc.tool, tc.arguments, tc.cwd); len(warnings) != 0 {
				t.Fatalf("warnings = %v, want none", warnings)
			}
			if after, hasRoot := tc.arguments["root"]; hasRoot != hadRoot || after != before {
				t.Fatalf("root changed from %v to %v", before, after)
			}
		})
	}
}

func TestResolveClientPathsFollowsSymlinksBeforeParentSteps(t *testing.T) {
	base := t.TempDir()
	repoA := filepath.Join(base, "A")
	repoB := filepath.Join(base, "B")
	for _, dir := range []string{filepath.Join(repoA, ".git"), filepath.Join(repoB, ".git"), filepath.Join(repoB, "sub")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(repoB, "sub"), filepath.Join(repoA, "link")); err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{"root": "link/.."}
	resolveClientPaths("workspace_open", arguments, repoA)
	want, _ := filepath.EvalSymlinks(repoB)
	if arguments["root"] != want {
		t.Fatalf("root = %v, want %s (where the filesystem leads, not %s)", arguments["root"], want, repoA)
	}
}

func TestResolveClientPathsNeverDefaultsAMutation(t *testing.T) {
	repository, _, _ := clientPathTree(t)
	for _, tool := range []string{"edit_apply", "change_plan", "verify_run"} {
		arguments := map[string]any{"operation": map[string]any{"kind": "create_file", "path": "a.go"}}
		if warnings := resolveClientPaths(tool, arguments, repository); len(warnings) != 0 || arguments["root"] != nil {
			t.Fatalf("%s was given a default root: %v", tool, arguments)
		}
	}
	arguments := map[string]any{"root": ".", "operation": map[string]any{"kind": "create_file", "path": "a.go"}}
	resolveClientPaths("edit_apply", arguments, repository)
	if arguments["root"] != repository {
		t.Fatalf("an explicit relative root on an edit was not resolved: %v", arguments["root"])
	}
}
