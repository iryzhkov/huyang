package bridge

import "testing"

func TestDiagnosticSourcePathExcludesProjectMetadata(t *testing.T) {
	for _, path := range []string{".huyang.toml", ".huyang/pipeline.json", ".gitignore"} {
		if diagnosticSourcePath(path) {
			t.Fatalf("%s unexpectedly requires LSP diagnostic coverage", path)
		}
	}
	for _, path := range []string{"README.md", "data/schema.json", "src/main.ts", "lib/service.rb", "pkg/risk.py", "main.go"} {
		if !diagnosticSourcePath(path) {
			t.Fatalf("%s unexpectedly excluded from LSP diagnostic coverage", path)
		}
	}
}
