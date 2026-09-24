package service

import (
	"context"
	"fmt"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// A binary file no sectioner could declare anything in does not make a
// search for a name that exists nowhere "could not establish": the answer is
// a complete zero.
func TestSymbolFindIgnoresUnreadableFilesThatDeclareNothing(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"model.go":  "package dispatch\n\ntype Shipment struct{}\n",
		"image.png": "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x01",
	})
	defer direct.closeProviders()
	result := direct.call(context.Background(), "symbol_find", map[string]any{"workspace_id": workspaceID, "query": "NowhereAtAll"})
	if result["outcome"] != "ok" || result["summary"] != "0 symbols found" {
		t.Fatalf("symbol_find with only an irrelevant skip = %#v", result)
	}
	coverage := result["data"].(map[string]any)["coverage"].(workspacecore.Coverage)
	if !coverage.Complete || coverage.SkippedCount != 0 {
		t.Fatalf("coverage = %#v, want complete", coverage)
	}
}

// A provider answer that says its optional language-server symbols are still
// pending is incomplete coverage, not a complete one.
func TestSymbolFindFoldsProviderIncompletenessIntoCoverage(t *testing.T) {
	backend := newStubProvider()
	backend.findSymbol = map[string]any{
		"matches":    []any{map[string]any{"file": "model.go", "name_path": "Shipment", "kind": "struct", "lines": "3-3"}},
		"complete":   false,
		"enrichment": map[string]any{"status": "pending", "reason": "optional_lsp_document_symbols_unavailable"},
	}
	useStubProvider(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"model.go": "package dispatch\n\ntype Shipment struct{}\n",
		"lib.rs":   "pub struct Shipment;\n",
	})
	defer direct.closeProviders()
	result := direct.call(context.Background(), "symbol_find", map[string]any{"workspace_id": workspaceID, "query": "Shipment"})
	if result["outcome"] != "partial" || result["code"] != "semantic_coverage_partial" {
		t.Fatalf("provider-incomplete answer = %#v", result)
	}
	coverage := result["data"].(map[string]any)["coverage"].(workspacecore.Coverage)
	if coverage.Complete || fmt.Sprint(coverage.Skipped) != "[provider_enrichment_pending: optional_lsp_document_symbols_unavailable]" {
		t.Fatalf("coverage = %#v", coverage)
	}
}
