package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Check the real transport catalog, not just descriptor counts: accidentally
// registering a full schema on a lean route would otherwise go unnoticed.
func assertProfileCatalog(t *testing.T, session *mcp.ClientSession, profile mcpapi.Profile) {
	t.Helper()
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	requireProfileCatalog(t, string(profile), listed.Tools, profile)
}

func TestProfileDiscoveryAndEditAliasOverUnix(t *testing.T) {
	base, err := os.MkdirTemp("", "h7-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	_, stop := startTestHuyangService(t, filepath.Join(base, "state"), filepath.Join(base, "sock"), "")
	defer stop()
	var full []byte
	for _, profile := range []mcpapi.Profile{mcpapi.ProfileFull, mcpapi.ProfileEdit, mcpapi.ProfileLean, mcpapi.ProfileOrient, mcpapi.ProfileExperimental} {
		session, cleanup := connectUnixOfficialClient(t, filepath.Join(base, "sock"), profile)
		assertProfileCatalog(t, session, profile)
		listed, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(listed.Tools)
		if err != nil {
			t.Fatal(err)
		}
		if profile == mcpapi.ProfileFull {
			full = encoded
		}
		if profile == mcpapi.ProfileEdit && string(full) != string(encoded) {
			t.Fatal("edit differs from full over Unix transport")
		}
		cleanup()
	}
}

func TestLeanProtocolValidationAndGuardedLifecycle(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("alpha alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(t.TempDir())
	defer direct.closeProviders()
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileLean, direct)
	defer cleanup()
	assertProfileCatalog(t, session, mcpapi.ProfileLean)

	read := func() map[string]any {
		t.Helper()
		result := callModern(t, session, "read", map[string]any{"root": root, "target": map[string]any{"path": "note.txt"}})
		if result["outcome"] != "ok" {
			t.Fatalf("read = %#v", result)
		}
		return result["data"].(map[string]any)
	}
	edit := func(op map[string]any) map[string]any {
		return callModern(t, session, "edit_apply", map[string]any{"root": root, "operation": op})
	}
	original := read()
	revision, ok := original["revision_id"].(string)
	if !ok || revision == "" {
		t.Fatalf("read lost copyable revision: %#v", original)
	}
	for _, args := range []map[string]any{
		{"root": root, "operation": map[string]any{"kind": "delete_file", "path": "note.txt", "force": true}},
		{"root": root, "operation": map[string]any{"kind": "move_file", "from": "note.txt", "to": "other.txt"}},
		{"root": root, "operation": map[string]any{"kind": "delete_file", "path": "note.txt"}, "preview_only": true},
		{"root": root, "operations": []any{map[string]any{"kind": "delete_file", "path": "note.txt"}}},
	} {
		refused := callModern(t, session, "edit_apply", args)
		if refused["code"] != "invalid_arguments" {
			t.Fatalf("full-only args escaped lean validation: %#v", refused)
		}
	}
	refused := callModern(t, session, "search", map[string]any{"root": root, "query": "alpha", "mode": "references"})
	if refused["code"] != "invalid_arguments" {
		t.Fatalf("lean accepted semantic search: %#v", refused)
	}
	searched := callModern(t, session, "search", map[string]any{"root": root, "query": "alpha", "mode": "literal"})
	if searched["outcome"] != "ok" {
		t.Fatalf("lean search = %#v", searched)
	}
	// Lean must retain M2's listing branch for both an omitted query and
	// an explicitly empty one; the profile schema must not require query.
	for _, omitQuery := range []bool{true, false} {
		arguments := map[string]any{"root": root, "paths": []string{"note.txt"}, "limit": 1}
		if !omitQuery {
			arguments["query"] = ""
		}
		listed := callModern(t, session, "search", arguments)
		if listed["outcome"] != "ok" {
			t.Fatalf("lean empty-query listing = %#v", listed)
		}
		files, _ := listed["data"].(map[string]any)["files"].([]any)
		if len(files) != 1 || files[0] != "note.txt" {
			t.Fatalf("lean listing did not reach M2: %#v", listed)
		}
	}

	for _, op := range []map[string]any{
		{"kind": "delete_file", "path": "note.txt"},
		{"kind": "create_file", "path": "note.txt", "content": "unsafe", "replace": true},
		{"kind": "create_file", "path": "note.txt", "content": "unsafe"},
		{"kind": "replace_literal", "path": "note.txt", "old": "alpha", "new": "beta"},
	} {
		refused := edit(op)
		if refused["outcome"] != "conflict" && refused["outcome"] != "failed" {
			t.Fatalf("unguarded/mismatched write accepted: %#v", refused)
		}
		if got := read()["content"]; got != original["content"] {
			t.Fatalf("refusal changed bytes: %v", got)
		}
	}
	replaced := edit(map[string]any{"kind": "create_file", "path": "note.txt", "content": "beta\n", "replace": true, "revision_id": revision})
	if replaced["outcome"] != "ok" {
		t.Fatalf("copyable read guard refused: %#v", replaced)
	}
	if replaced["data"].(map[string]any)["document_revision"] == nil {
		t.Fatalf("edit lost copyable guard: %#v", replaced)
	}

	stale := edit(map[string]any{"kind": "delete_file", "path": "note.txt", "revision_id": revision})
	if stale["outcome"] != "conflict" || read()["content"] != "beta\n" {
		t.Fatalf("stale delete = %#v", stale)
	}
	current := read()["revision_id"]
	deleted := edit(map[string]any{"kind": "delete_file", "path": "note.txt", "revision_id": current})
	if deleted["outcome"] != "ok" {
		t.Fatalf("guarded delete = %#v", deleted)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted path stat = %v", err)
	}
	created := edit(map[string]any{"kind": "create_file", "path": "note.txt", "content": "one\n"})
	if created["outcome"] != "ok" {
		t.Fatalf("missing-target create = %#v", created)
	}
	literal := edit(map[string]any{"kind": "replace_literal", "path": "note.txt", "old": "one", "new": "two"})
	if literal["outcome"] != "ok" || read()["content"] != "two\n" {
		t.Fatalf("literal = %#v", literal)
	}

	// A previous root-bearing read must never turn a relative mutation into
	// an implicit project write. M2's read-only default must retain this.
	unscoped := callModern(t, session, "edit_apply", map[string]any{"operation": map[string]any{
		"kind": "replace_literal", "path": "note.txt", "old": "two", "new": "unsafe",
	}})
	if !slices.Contains([]any{"failed", "conflict"}, unscoped["outcome"]) || read()["content"] != "two\n" {
		t.Fatalf("unscoped mutation defaulted a project: %#v", unscoped)
	}
}
