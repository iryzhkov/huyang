package service

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/iryzhkov/huyang/internal/provider"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

type frictionProofProvider struct{ stubProvider }

func (p *frictionProofProvider) Call(ctx context.Context, request provider.Request) (provider.Result, error) {
	if request.Operation == "huyang_diagnostic_evidence" {
		files, _ := request.Arguments["files"].([]string)
		batches := []any{}
		for _, file := range files {
			batches = append(batches, map[string]any{"kind": "lsp_push", "provider_id": "rust_analyzer#1", "producer": "rust_analyzer", "document": file, "complete": true, "selected": true, "dimension": "edited_documents"})
		}
		return provider.Result{Value: map[string]any{"batches": batches}}, nil
	}
	return p.stubProvider.Call(ctx, request)
}

// The real MCP boundary must retain diagnostic reasons even when the second
// edit suppresses repeated recovery prose. Applied bytes and semantic proof
// are independent facts; a provisional edit must not look like a failed edit.
func TestModernEditSpoolsDiagnosticStateOnRepeatedProvisionalResults(t *testing.T) {
	spool, root := t.TempDir(), t.TempDir()
	useFixedProvider(t, &frictionProofProvider{stubProvider: *newStubProvider()})
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "1")
	if err := os.WriteFile(filepath.Join(root, "main.rs"), []byte("pub fn value() -> u32 { 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, newDirectWorkspaces(t.TempDir()))
	defer cleanup()
	for _, replacement := range [][2]string{{"{ 1 }", "{ 2 }"}, {"{ 2 }", "{ 3 }"}} {
		result := callModern(t, session, "edit_apply", map[string]any{"root": root, "format": false, "operation": map[string]any{"kind": "replace_literal", "path": "main.rs", "old": replacement[0], "new": replacement[1]}})
		data := result["data"].(map[string]any)
		next := result["next"].([]any)
		if replacement[0] == "{ 1 }" {
			if len(next) != 2 {
				t.Fatalf("missing initial recovery: %#v", next)
			}
			recovery := next[1].(map[string]any)
			if !reflect.DeepEqual(recovery["stages"], []any{"diagnostics"}) || recovery["revision_or_transaction"] != data["revision"] {
				t.Fatalf("invalid exact-revision recovery: %#v", recovery)
			}
		} else {
			for _, step := range next {
				tool := step.(map[string]any)["tool"]
				if tool == "verify_run" || tool == "language_server_status" {
					t.Fatalf("repeated diagnostic recovery: %#v", next)
				}
			}
			verification := data["verification"].(map[string]any)
			if verification["reasons_unchanged"] != true || !reflect.DeepEqual(verification["reason_codes"], []any{"push_missing_current_document_proof"}) {
				t.Fatalf("compact reply lost its proof gap: %#v", verification)
			}
		}
	}
	files, err := filepath.Glob(filepath.Join(spool, frictionSource, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("spool files: %v %v", files, err)
	}
	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	count := 0
	for scan.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event["tool"] != "edit_apply" {
			continue
		}
		count++
		if event["canonical_changed"] != true || event["outcome"] != "provisional" || event["ok"] != true {
			t.Fatalf("mutation and diagnostics conflated: %#v", event)
		}
		if event["verification_confidence"] != "provisional" || !reflect.DeepEqual(event["reason_codes"], []any{"push_missing_current_document_proof"}) {
			t.Fatalf("missing diagnostic state on edit %d: %#v", count, event)
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("edit events = %d", count)
	}
}
