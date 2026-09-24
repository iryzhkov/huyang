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
	// A language server that has not attached yet is not a failure of the
	// call: it is unavailable for now, and the same call can succeed.
	for _, code := range []string{"lsp_starting", "lsp_attach_deadline_exceeded"} {
		notReady := modernProviderFailure("req", workspace, "language_server_unavailable", &provider.ProviderError{Code: code, Message: "not attached"})
		next, _ := notReady["next"].([]any)
		if notReady["outcome"] != "unavailable" || notReady["retryable"] != true || notReady["code"] != "language_server_unavailable" || len(next) != 2 {
			t.Fatalf("%s = %#v", code, notReady)
		}
		// A server still starting is retried first; one that never began
		// attaching is inspected first.
		statusAt := 1
		if code == "lsp_attach_deadline_exceeded" {
			statusAt = 0
		}
		if status, _ := next[statusAt].(map[string]any); status["tool"] != "language_server_status" {
			t.Fatalf("%s next = %#v", code, next)
		}
		if notReady["data"].(map[string]any)["reason"] != code {
			t.Fatalf("%s does not keep the provider's reason: %#v", code, notReady["data"])
		}
	}
	plain := modernProviderFailure("req", workspace, "language_server_probe_failed", errors.New("boom"))
	if plain["code"] != "language_server_probe_failed" {
		t.Fatalf("uncoded failure = %#v", plain)
	}
}
