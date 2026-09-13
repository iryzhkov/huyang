//go:build live

package livetest

import (
	"maps"
	"strings"
	"testing"
)

// The scenario S21 names: start the daemon, connect through the adapter, open
// a workspace, prepare, inspect and discard a plan, and prove no canonical
// byte moved. This is the shape of every session an agent has, and it is the
// one path that in-process tests cannot cover: they never cross the socket.
func TestServiceBoundaryPreparesAndDiscardsWithoutTouchingCanonicalBytes(t *testing.T) {
	instance := start(t)
	session := instance.connect("full")
	root := fixture(t, "go")
	before := hashTree(t, root)

	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	if outcome(opened) != "ok" {
		t.Fatalf("open = %#v", opened)
	}
	workspaceID := workspaceIdentity(t, opened)
	// The rules for calling the server cheaply reach a session that has just
	// connected, which is the only moment they are of any use.
	if guide, _ := opened["guide"].([]any); len(guide) == 0 {
		t.Fatalf("the first reply of a session carried no guide: %#v", opened)
	}

	prepared := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "prepare", "idempotency_key": "live-prepare",
		"operations": []any{map[string]any{
			"op_id": "retitle", "kind": "replace_symbol", "content": "// Total is the amount left.\nfunc Total() int {\n\treturn 8\n}\n",
			"target": map[string]any{"symbol_locator": map[string]any{"path": "ledger.go", "name_path": "Total"}},
		}},
	})
	// A fresh installation has no language server, so the prepared plan is
	// provisional rather than ready; both are correct, and a prepare that
	// claimed "ok" with no diagnostics behind it would not be.
	if verdict := outcome(prepared); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("prepare = %#v", prepared)
	}
	plan := data(prepared)["plan"].(map[string]any)
	planID, planRevision := plan["plan_id"].(string), plan["plan_revision"]
	if state, _ := plan["state"].(string); state != "READY" && state != "PROVISIONAL" {
		t.Fatalf("prepared plan state = %q", state)
	}

	inspected := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "inspect", "idempotency_key": "live-inspect",
		"plan_id": planID, "plan_revision": planRevision,
	})
	// The plan's own state colours the reply, so an inspection of a
	// provisional plan is provisional rather than ok.
	if verdict := outcome(inspected); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("inspect = %#v", inspected)
	}

	discarded := call(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "discard", "idempotency_key": "live-discard",
		"plan_id": planID, "plan_revision": planRevision,
	})
	if verdict := outcome(discarded); verdict != "ok" && verdict != "provisional" {
		t.Fatalf("discard = %#v", discarded)
	}

	// Preparing stages the change in a sandbox and inspecting reads it; only
	// apply writes. The repository must be byte for byte what it was.
	if after := hashTree(t, root); !maps.Equal(before, after) {
		t.Fatalf("preparing and discarding a plan changed canonical bytes:\nbefore %v\nafter  %v", before, after)
	}
}

// A language with no server attached must answer "I could not tell" rather
// than "nothing is wrong". This is the false-clean case S21 exists to pin: an
// edit to a language nothing indexes is either plainly provisional with a
// named reason, or plainly ok because no server was ever expected for it.
func TestAbsentLanguageServerIsUnavailableRatherThanClean(t *testing.T) {
	instance := start(t)
	session := instance.connect("edit")
	root := fixture(t, "bash")

	applied := call(t, session, "edit_apply", map[string]any{
		"root": root, "idempotency_key": "live-bash-edit",
		"operation": map[string]any{
			"kind": "replace_literal", "path": "ledger.sh", "old": "echo 7", "new": "echo 8",
		},
	})
	switch outcome(applied) {
	case "provisional":
		// The honest answer when a server is configured but says nothing:
		// the reply has to name why the verdict is incomplete.
		if !strings.Contains(summary(applied), "lsp") && !strings.Contains(summary(applied), "diagnostic") {
			t.Fatalf("a provisional verdict does not say what is missing: %q", summary(applied))
		}
	case "ok":
		// Equally honest when nothing was expected: a shell script is not a
		// language this installation diagnoses, and the reply must not claim
		// a semantic verdict it never sought.
		if strings.Contains(summary(applied), "no new diagnostic") {
			t.Fatalf("an unindexed language reported a clean semantic verdict: %q", summary(applied))
		}
	default:
		t.Fatalf("edit of an unindexed language = %#v", applied)
	}
}
