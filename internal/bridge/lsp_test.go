package bridge

import "testing"

// A provider install that reports failure yields a failed tool result with
// recovery; it is never promoted to an attached language server.
func TestLanguageServerSetupInstallFailureIsNeverReportedAttached(t *testing.T) {
	backend := newStubProvider()
	useStubProvider(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"CMakeLists.txt": "project(sample)\n"})
	defer direct.closeProviders()

	session, cleanup := connectOfficialClient(t, profileFull, direct)
	defer cleanup()
	result := callModern(t, session, "language_server_setup", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "install-failure",
		"action": "install", "language": "cmake", "server": "cmake-language-server",
	})
	if result["outcome"] != "failed" || result["code"] != "language_server_install_failed" {
		t.Fatalf("failed provider installation was promoted: %#v", result)
	}
	if len(result["next"].([]any)) == 0 {
		t.Fatalf("failed installation omitted recovery: %#v", result)
	}
}

// Attachment is confirmed by a server name or an explicit true, never by
// an empty, "none" or "false" value.
func TestLanguageServerAttachmentConfirmedAcceptsServerNameOrTrue(t *testing.T) {
	if !languageServerAttachmentConfirmed("rust_analyzer") || !languageServerAttachmentConfirmed(true) {
		t.Fatal("positive provider attachment was not recognized")
	}
	for _, value := range []any{nil, false, "", "none", "false"} {
		if languageServerAttachmentConfirmed(value) {
			t.Fatalf("false attachment %#v was recognized", value)
		}
	}
}

// A terminal install status (unknown, unsupported) offers inspection only;
// retrying an impossible server is not a recovery.
func TestLanguageServerInstallTerminalFailureOffersInspectionOnly(t *testing.T) {
	next := failedLanguageServerInstallNext("unknown", "markdown", "definitely_missing_server")
	if len(next) != 1 {
		t.Fatalf("terminal installation failure offered an impossible retry: %#v", next)
	}
	action, _ := next[0].(map[string]any)
	if action["tool"] != "language_server_status" {
		t.Fatalf("terminal installation recovery is not an inspection: %#v", next)
	}
}

// A transient install failure keeps the retry with the requested server.
func TestLanguageServerInstallTransientFailureKeepsRetry(t *testing.T) {
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

// An installed Tree-sitter parser is reported as installed for every
// language, but counts as a verification parser only for the languages the
// exact parser stage supports.
func TestReconcileParserSupportDistinguishesInstalledFromVerification(t *testing.T) {
	ruby := map[string]any{"filetype": "ruby", "treesitter_parser": true}
	reconcileParserSupport(ruby)
	if ruby["treesitter_parser_installed"] != true || ruby["verification_parser"] != false || ruby["treesitter_parser"] != true || ruby["verification_parser_recovery"] == "" {
		t.Fatalf("ruby parser support = %#v", ruby)
	}

	jsonEntry := map[string]any{"filetype": "json", "treesitter_parser": true}
	reconcileParserSupport(jsonEntry)
	if jsonEntry["treesitter_parser_installed"] != true || jsonEntry["verification_parser"] != true || jsonEntry["treesitter_parser"] != true {
		t.Fatalf("json parser support = %#v", jsonEntry)
	}
}
