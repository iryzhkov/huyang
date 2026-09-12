package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
)

// The rules for calling the server cheaply reach the agent once, on whatever
// call first opened the workspace, and never again for that workspace.
func TestGuideIsSentOnceWhenAWorkspaceIsFirstOpened(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ledger.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: root}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})

	// The cheapest way in names the root on an ordinary call, so that call
	// is the one that carries the rules.
	first := handlers.Execute(context.Background(), "req_first", "search", map[string]any{"root": root, "query": "package"})
	guide, _ := first["guide"].([]string)
	if first["outcome"] != "ok" || len(guide) == 0 {
		t.Fatalf("first call by root = %#v", first)
	}

	second := handlers.Execute(context.Background(), "req_second", "search", map[string]any{"root": root, "query": "package"})
	if _, repeated := second["guide"]; repeated {
		t.Fatalf("the guide repeated on a reopened workspace: %#v", second["guide"])
	}

	// An explicit open of a workspace that is already registered does not
	// repeat them either.
	opened := handlers.Execute(context.Background(), "req_open", "workspace_open", map[string]any{"kind": "project", "root": root})
	if _, repeated := opened["guide"]; repeated {
		t.Fatalf("the guide repeated on workspace_open: %#v", opened["guide"])
	}

	// A workspace opened for the first time through workspace_open carries
	// them there instead.
	other := t.TempDir()
	fresh := handlers.Execute(context.Background(), "req_other", "workspace_open", map[string]any{"kind": "project", "root": other})
	if rules, _ := fresh["guide"].([]string); len(rules) == 0 {
		t.Fatalf("workspace_open of a new root = %#v", fresh)
	}

	// The registry outlives a session, so "first" is per client and not per
	// workspace: the next agent to work in this repository is told the rules
	// even though the workspace has been open for days.
	nextSession := WithClientIdentity(context.Background(), "session-2")
	later := handlers.Execute(nextSession, "req_later", "search", map[string]any{"root": root, "query": "package"})
	if rules, _ := later["guide"].([]string); len(rules) == 0 {
		t.Fatalf("a second session in an open workspace = %#v", later)
	}
	if again := handlers.Execute(nextSession, "req_again", "search", map[string]any{"root": root, "query": "package"}); again["guide"] != nil {
		t.Fatalf("the guide repeated within a session: %#v", again["guide"])
	}
}
