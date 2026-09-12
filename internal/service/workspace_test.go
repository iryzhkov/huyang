package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Every workspace_inspect view names itself and the current revision, only
// the overview and map views carry the orientation, and a canonical edit
// advances the revision the next inspection reports.
func TestWorkspaceInspectViewsReportRevisionAndAdvanceAfterEdit(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"a.go": "package sample\n\nvar Before = 1\n",
	})
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()

	for _, view := range []string{"status", "overview", "map"} {
		inspected := callModern(t, session, "workspace_inspect", map[string]any{
			"workspace_id": workspaceID, "view": view,
		})
		data := inspected["data"].(map[string]any)
		if data["view"] != view || data["revision"] != "wsrev_1" {
			t.Fatalf("%s inspection omitted its view/revision: %#v", view, inspected)
		}
		_, hasOverview := data["overview"]
		if view == "status" && hasOverview {
			t.Fatalf("status inspection unexpectedly returned repository overview: %#v", inspected)
		}
		if view != "status" && !hasOverview {
			t.Fatalf("%s inspection omitted repository orientation: %#v", view, inspected)
		}
	}

	applied := applyLiteralProbeEdit(t, direct, workspaceID, "Before", "After", "inspect-revision-edit")
	if applied["outcome"] != "provisional" {
		t.Fatalf("edit failed: %#v", applied)
	}
	if !strings.Contains(applied["summary"].(string), "diagnostic refresh failed") {
		t.Fatalf("edit misreported provider refresh failure: %#v", applied)
	}
	verification := applied["data"].(map[string]any)["verification"].(map[string]any)
	if verification["confidence"] != "unavailable" {
		t.Fatalf("edit invented diagnostic timeout evidence: %#v", applied)
	}
	next := applied["next"].([]any)
	if len(next) != 2 || next[0].(map[string]any)["tool"] != "language_server_status" ||
		next[1].(map[string]any)["revision_or_transaction"] != "wsrev_2" {
		t.Fatalf("edit provider failure is not actionable: %#v", applied)
	}
	inspected := callModern(t, session, "workspace_inspect", map[string]any{
		"workspace_id": workspaceID, "view": "status",
	})
	if revision := inspected["data"].(map[string]any)["revision"]; revision != "wsrev_2" {
		t.Fatalf("inspection revision did not advance after edit: %#v", inspected)
	}
}

// A read-only inspection that observes an external write to a file the
// service never read still advances the revision and leaves an explicit gap.
func TestWorkspaceInspectRecordsExternalWriteToUnreadFileAsRevisionGap(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		"main.go":   "package sample\n",
		"README.md": "before\n",
	})
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspected := callModern(t, session, "workspace_inspect", map[string]any{
		"workspace_id": workspaceID, "view": "status",
	})
	if revision := inspected["data"].(map[string]any)["revision"]; revision != "wsrev_2" {
		t.Fatalf("read-only inspection absorbed an external change without a revision: %#v", inspected)
	}
	diff := callModern(t, session, "revision_diff", map[string]any{
		"workspace_id": workspaceID, "from_revision": "wsrev_1", "to_revision_or_current": "wsrev_2",
	})
	gaps := diff["data"].(map[string]any)["gaps"].([]any)
	if len(gaps) != 1 {
		t.Fatalf("external read-only change did not produce one explicit gap: %#v", diff)
	}
}

// Reopening a project after a file appeared externally advances the revision
// and revision_diff reports the addition as one gap.
func TestWorkspaceReopenRecordsExternalNewFileAsRevisionGap(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "known.txt"), []byte("known\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(t.TempDir())
	defer direct.closeProviders()
	opened := direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
	identity := opened["workspace"].(workspacecore.Identity)
	workspaceID := string(identity.ID)
	from := fmt.Sprintf("wsrev_%d", identity.StateSeq)
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
	toIdentity := reopened["workspace"].(workspacecore.Identity)
	to := fmt.Sprintf("wsrev_%d", toIdentity.StateSeq)
	if to == from {
		t.Fatalf("external inventory addition left revision unchanged at %s", from)
	}
	diff := direct.call(context.Background(), "revision_diff", map[string]any{
		"workspace_id": workspaceID, "from_revision": from, "to_revision_or_current": to,
	})
	gaps := mcpapi.AnySlice(diff["data"].(map[string]any)["gaps"])
	if len(gaps) != 1 {
		t.Fatalf("revision gaps = %#v", gaps)
	}
}

// A configured but untrusted pipeline is reported as such, with the exact
// user config file the caller must edit to trust the root.
func TestWorkspaceInspectExplainsUntrustedPipelineWithRecovery(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		".huyang.toml": "version = 1\n[[check]]\nname = \"check\"\ncommand = [\"go\", \"test\", \"./...\"]\n",
		"main.go":      "package sample\n",
	})
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()
	inspected := callModern(t, session, "workspace_inspect", map[string]any{"workspace_id": workspaceID})
	state := inspected["data"].(map[string]any)["pipeline_state"].(map[string]any)
	if state["state"] != "configured_untrusted" || state["configured"] != true || state["trusted"] != false {
		t.Fatalf("pipeline state is ambiguous: %#v", inspected)
	}
	next := inspected["next"].([]any)
	if len(next) != 1 || next[0].(map[string]any)["root"] != root ||
		next[0].(map[string]any)["user_config"] != filepath.Join(configHome, "huyang", "config.toml") {
		t.Fatalf("pipeline trust recovery is not actionable: %#v", inspected)
	}
}
