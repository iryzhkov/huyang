package handlers

import (
	"errors"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestProviderFailuresAreClassifiedByKernelCode(t *testing.T) {
	workspace, err := workspacecore.New(workspacecore.KindProject, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	busy := modernProviderFailure("req", workspace, "language_server_unavailable", &provider.ProviderError{Code: "workspace_busy", Message: "staging"})
	if busy["outcome"] != "conflict" || busy["code"] != "workspace_busy" {
		t.Fatalf("workspace_busy = %#v", busy)
	}
	unconfigured := modernProviderFailure("req", workspace, "language_server_unavailable", &provider.ProviderError{Code: "lsp_not_configured", Message: "no server"})
	if unconfigured["outcome"] != "unavailable" || unconfigured["code"] != "language_server_unavailable" || len(unconfigured["next"].([]any)) != 1 {
		t.Fatalf("lsp_not_configured = %#v", unconfigured)
	}
	cancelled := modernProviderFailure("req", workspace, "language_server_unavailable", &provider.ProviderError{Code: "provider_cancelled", Message: "cancelled"})
	if cancelled["outcome"] != "failed" || cancelled["code"] != "request_cancelled" {
		t.Fatalf("provider_cancelled = %#v", cancelled)
	}
	plain := modernProviderFailure("req", workspace, "language_server_probe_failed", errors.New("boom"))
	if plain["code"] != "language_server_probe_failed" {
		t.Fatalf("uncoded failure = %#v", plain)
	}
}
