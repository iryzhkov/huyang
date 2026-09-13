//go:build live

package livetest

// Deploying Huyang restarts the service under everything that is talking to
// it, and what talks to it is agent sessions that cannot be restarted: an
// interactive one loses its tools for good, an unattended one loses the run.
// So a deploy has been something to do at the end of a stretch, when nothing
// is connected, which is exactly when a fix is least likely to be deployed.
//
// The adapter is built to survive it: it reconnects, replays the initialize
// handshake and resends the requests that were in flight. Nothing proved that
// end to end until these tests, which restart the daemon process the way a
// deployment does and then keep using the same session.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A session that was working before the service was replaced is still working
// after it, on the same workspace, without the client knowing anything
// happened.
func TestASessionSurvivesTheServiceBeingRedeployed(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)
	before := call(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "ledger.go"},
	})
	if outcome(before) != "ok" {
		t.Fatalf("read before the redeploy = %#v", before)
	}

	instance.redeploy()

	after := call(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"path": "ledger.go"},
	})
	if outcome(after) != "ok" {
		t.Fatalf("the session did not survive the redeploy: %#v", after)
	}
	if data(after)["content"] != data(before)["content"] {
		t.Fatalf("the same read answered different bytes across the redeploy")
	}
	if got := workspaceIdentity(t, after); got != workspaceID {
		t.Fatalf("the workspace id changed across the redeploy: %s then %s", workspaceID, got)
	}

	// And the session can still write, which is the half that matters: a
	// reconnected reader that cannot commit is not a surviving session.
	edited := call(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "after-redeploy",
		"operation": map[string]any{
			"kind": "create_file", "path": "after-redeploy.txt", "content": "written after the service was replaced\n",
		},
	})
	if verdict := outcome(edited); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("edit after the redeploy = %s: %s", verdict, summary(edited))
	}
	if _, err := os.Stat(filepath.Join(root, "after-redeploy.txt")); err != nil {
		t.Fatalf("the edit did not reach the disk after the redeploy: %v", err)
	}
}

// A handle taken before the redeploy names bytes the new process cannot
// vouch for by the same revision, and it says so in a typed refusal rather
// than failing as transport or answering as though nothing had happened.
func TestAHandleFromBeforeTheRedeployIsAnsweredHonestly(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := workspaceIdentity(t, opened)
	found := call(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "Total", "include_handles": true,
	})
	hits, _ := data(found)["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("no hits to take a handle from: %#v", found)
	}
	first, _ := hits[0].(map[string]any)
	handle, _ := first["handle"].(string)
	if handle == "" {
		t.Fatalf("hit carries no handle: %#v", first)
	}

	instance.redeploy()

	answered := call(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"handle": handle},
	})
	switch outcome(answered) {
	case "ok":
		// The handle store is durable, so resolving it is the better answer:
		// it must be the bytes it always named.
		if content, _ := data(answered)["content"].(string); !strings.Contains(content, "Total") {
			t.Fatalf("a handle from before the redeploy resolved to other bytes: %#v", data(answered))
		}
	case "conflict", "failed":
		if code, _ := answered["code"].(string); code == "" {
			t.Fatalf("a handle from before the redeploy was refused with no code: %#v", answered)
		}
		if next, _ := answered["next"].([]any); len(next) == 0 {
			t.Fatalf("a handle from before the redeploy was refused with nothing to do next: %#v", answered)
		}
	default:
		t.Fatalf("a handle from before the redeploy answered %s: %#v", outcome(answered), answered)
	}
}
