package bridge

import "testing"

func TestTerminalLanguageServerInstallFailureDoesNotRetryImpossibleServer(t *testing.T) {
	next := failedLanguageServerInstallNext("unknown", "markdown", "definitely_missing_server_r12")
	if len(next) != 1 {
		t.Fatalf("terminal installation failure offered an impossible retry: %#v", next)
	}
	action, _ := next[0].(map[string]any)
	if action["tool"] != "language_server_status" {
		t.Fatalf("terminal installation recovery is not an inspection: %#v", next)
	}
}

func TestTransientLanguageServerInstallFailureKeepsRetry(t *testing.T) {
	next := failedLanguageServerInstallNext("failed", "cmake", "cmake-language-server")
	if len(next) != 2 {
		t.Fatalf("transient installation failure omitted retry: %#v", next)
	}
	retry, _ := next[0].(map[string]any)
	if retry["tool"] != "language_server_setup" || retry["action"] != "install" ||
		retry["server"] != "cmake-language-server" {
		t.Fatalf("transient installation recovery is not actionable: %#v", next)
	}
}

func TestDebugFailureRecoveryUsesExposedToolsAndOriginalAction(t *testing.T) {
	next := debugFailureNext("debug_session", "start")
	if len(next) != 2 {
		t.Fatalf("debug failure recovery is incomplete: %#v", next)
	}
	status, _ := next[0].(map[string]any)
	retry, _ := next[1].(map[string]any)
	if status["tool"] != "language_server_status" ||
		retry["tool"] != "debug_session" || retry["action"] != "start" ||
		retry["use_new_idempotency_key"] != true {
		t.Fatalf("debug failure recovery names unavailable operations: %#v", next)
	}
}
