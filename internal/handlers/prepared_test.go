package handlers

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// A path inside a prepared revision is a path inside its tree, and nothing
// else. This is the one place a crafted path could read the machine through
// a sandbox, so it is checked rather than assumed.
func TestPreparedPathsStayInsideTheSandbox(t *testing.T) {
	view := providerpool.PreparedView{Tree: filepath.Join(t.TempDir(), "tree")}
	for _, escape := range []string{
		"../outside.go", "../../etc/passwd", "/etc/passwd", "sub/../../outside.go",
	} {
		if _, err := preparedPath(view, escape); err == nil {
			t.Fatalf("%q was accepted as a path inside the prepared revision", escape)
		}
	}
	inside, err := preparedPath(view, "pkg/ledger.go")
	if err != nil || !strings.HasPrefix(inside, view.Tree) {
		t.Fatalf("an ordinary path was refused: %q, %v", inside, err)
	}
}

// Whatever a language server answers, the reply speaks in the paths the agent
// knows. A path the service cannot place inside the tree is dropped rather
// than handed back.
func TestSandboxPathsAreRewrittenOnTheWayOut(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "sandbox-7", "tree")
	view := providerpool.PreparedView{Tree: tree}
	answer := map[string]any{
		"locations": []any{
			map[string]any{"file": filepath.Join(tree, "ledger.go"), "line": 4},
			map[string]any{"file": filepath.Join(tree, "sub", "report.go"), "line": 9},
		},
		"note": "read " + filepath.Join(tree, "ledger.go"),
	}
	rewritten := redactSandboxPaths(view, answer).(map[string]any)
	locations := rewritten["locations"].([]any)
	if locations[0].(map[string]any)["file"] != "ledger.go" {
		t.Fatalf("a sandbox path survived: %#v", locations[0])
	}
	if locations[1].(map[string]any)["file"] != "sub/report.go" {
		t.Fatalf("a nested path was not made workspace-relative: %#v", locations[1])
	}
	if strings.Contains(rewritten["note"].(string), tree) {
		t.Fatalf("a sandbox path survived inside prose: %q", rewritten["note"])
	}
}

// The selector distinguishes the canonical workspace from a prepared
// revision, and says which is which without guessing.
func TestPreparedSelectorReadsEveryWayOfNamingARevision(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{"a.go": "package p\n"})
	workspace := handlers.registry.Lookup(workspacecore.ID(workspaceID))
	current := "wsrev_1"
	for _, canonical := range []map[string]any{
		{}, {"revision": ""}, {"revision": "current"}, {"revision": current},
	} {
		if decodePreparedSelector(canonical).wantsPrepared(workspace) {
			t.Fatalf("%#v was read as a prepared revision", canonical)
		}
	}
	for _, prepared := range []map[string]any{
		{"revision": "prep_abc"}, {"plan_id": "plan_abc"}, {"revision": "wsrev_99"},
	} {
		if !decodePreparedSelector(prepared).wantsPrepared(workspace) {
			t.Fatalf("%#v was read as the canonical workspace", prepared)
		}
	}
}

// A prepared revision nobody is holding is refused with what happened to it,
// and the refusal never becomes a canonical answer.
func TestAnAbsentPreparedRevisionIsRefusedRatherThanSubstituted(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{"a.go": "package p\n"})
	workspace := handlers.registry.Lookup(workspacecore.ID(workspaceID))
	_, failure := handlers.resolvePrepared("req", workspace, preparedSelector{Revision: "prep_gone"})
	if failure == nil {
		t.Fatal("an unknown prepared revision resolved")
	}
	if failure["code"] != "prepared_revision_unavailable" {
		t.Fatalf("refusal = %#v", failure)
	}
	summary, _ := failure["summary"].(string)
	if !strings.Contains(summary, "applied, discarded") {
		t.Fatalf("the refusal does not say what could have happened: %q", summary)
	}
}
