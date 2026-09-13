//go:build live

package livetest

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Every later execution test can retain this manifest with its evidence. A
// missing provider is recorded as missing, never as a version that was tested.
func executionManifest(t *testing.T, instance *live, root string) {
	t.Helper()
	content, err := os.ReadFile(instance.binary)
	if err != nil {
		t.Fatal(err)
	}
	info, err := buildinfo.ReadFile(instance.binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("execution tool_sha256=%x build=%s canonical=%v", sha256.Sum256(content), info, hashTree(t, root))
	for _, command := range [][]string{
		{"nvim", "--version"}, {"gopls", "version"},
		{"typescript-language-server", "--version"}, {"pyright", "--version"},
		{"lua-language-server", "--version"}, {"dlv", "version"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()
		cancel()
		t.Logf("execution provider=%s version=%q error=%v", command[0], strings.TrimSpace(string(output)), err)
	}
}

func TestExecutionFixturesRecordCanonicalIdentity(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	for _, language := range []string{"go", "typescript", "python", "lua"} {
		t.Run(language, func(t *testing.T) {
			root := fixture(t, filepath.Join("execution", language))
			before := hashTree(t, root)
			executionManifest(t, instance, root)
			opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
			if outcome(opened) != "ok" {
				t.Fatalf("open: %v", opened)
			}
			extension := map[string]string{"go": "go", "typescript": "ts", "python": "py", "lua": "lua"}[language]
			read := call(t, session, "read", map[string]any{
				"workspace_id": workspaceIdentity(t, opened),
				"target":       map[string]any{"path": "main." + extension},
			})
			if outcome(read) != "ok" {
				t.Fatalf("read: %v", read)
			}
			if !reflect.DeepEqual(before, hashTree(t, root)) {
				t.Fatal("read changed canonical fixture")
			}
		})
	}
}

func TestExecutionGoFixtureHasKnownDivergence(t *testing.T) {
	root := fixture(t, filepath.Join("execution", "go"))
	binary := filepath.Join(t.TempDir(), "execution-fixture")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-gcflags=all=-N -l", "-o", binary, ".")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, tc := range []struct {
		input, output string
		exit          int
	}{
		{"pass", "4 2", 0}, {"fail", "-1 2", 1},
	} {
		command := exec.CommandContext(ctx, binary, tc.input)
		output, err := command.CombinedOutput()
		if strings.TrimSpace(string(output)) != tc.output || command.ProcessState == nil || command.ProcessState.ExitCode() != tc.exit {
			t.Fatalf("%s: output=%q error=%v state=%v", tc.input, output, err, command.ProcessState)
		}
		t.Log(fmt.Sprintf("execution %s output=%q exit=%d", tc.input, output, tc.exit))
	}
}
