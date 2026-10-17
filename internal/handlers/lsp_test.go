package handlers

import (
	"strings"
	"testing"
)

// A missing language is answered with how to install its server outside
// Huyang and the restart that picks it up, never with an install action.
func TestMissingLanguageServerPointsOutOfBand(t *testing.T) {
	next := outOfBandInstallNext(languageSupport{
		missing:        []string{"json", "markdown"},
		installOptions: map[string][]string{"json": {"jsonls"}},
	})
	if len(next) != 1 {
		t.Fatalf("out-of-band recovery = %#v", next)
	}
	action, _ := next[0].(map[string]any)
	prerequisite, _ := action["prerequisite"].(string)
	if action["tool"] != "language_server_setup" || action["action"] != "restart" ||
		!strings.Contains(prerequisite, "json, markdown") || !strings.Contains(prerequisite, "Mason") ||
		!strings.Contains(prerequisite, "install_options") {
		t.Fatalf("out-of-band recovery does not say how to install: %#v", next)
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
