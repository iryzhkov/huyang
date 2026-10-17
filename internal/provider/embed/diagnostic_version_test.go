package embed

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
)

func TestOwnedProviderAdvertisesDiagnosticPublicationVersions(t *testing.T) {
	backend, _, _ := testBackend(t, false)
	execLua(t, backend, `assert(vim.lsp.config["*"].capabilities.textDocument.publishDiagnostics.versionSupport == true)`)
}

// The real Neovim reload path used to send didOpen(version=0), while the
// diagnostic barrier expected changedtick=4. gopls omits version 0 in JSON.
func TestLangprobeVersionNegotiationAndBaselineLatency(t *testing.T) {
	if os.Getenv("HUYANG_LANGPROBE") != "1" {
		t.Skip("set HUYANG_LANGPROBE=1 for real server negotiation")
	}
	home, _ := os.UserHomeDir()
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+filepath.Join(home, ".local/share/nvim/mason/bin"))
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls unavailable")
	}
	root := t.TempDir()
	for name, content := range map[string]string{"go.mod": "module example.com/probe\n\ngo 1.25\n", "main.go": "package main\nvar X int = \"bad\"\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "main.go")
	backend, _, _ := testBackend(t, false, func(c *Config) { c.Root = root; c.InitFile = "" })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	call := func(op string, args map[string]any) provider.Result {
		t.Helper()
		result, err := backend.Call(ctx, provider.Request{Operation: op, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	call("workspace_support", map[string]any{"root": root})
	before := call("huyang_diagnostic_evidence", map[string]any{"files": []string{path}, "wait_ms": 3000})
	assertProof := func(result provider.Result, wantFindings bool) {
		t.Helper()
		if len(result.Evidence) != 1 {
			t.Fatalf("batches=%+v", result)
		}
		b := result.Evidence[0]
		if !b.Complete || b.DocumentVersion == nil || b.ExpectedVersion == nil || *b.DocumentVersion != *b.ExpectedVersion || *b.ExpectedVersion == 0 {
			t.Fatalf("no actual version proof: %+v", b)
		}
		if (len(b.Findings) > 0) != wantFindings {
			t.Fatalf("wrong findings: %+v", b)
		}
	}
	assertProof(before, true)
	started := time.Now()
	call("huyang_diagnostic_evidence", map[string]any{"baseline": true, "wait_ms": 0})
	t.Logf("warm pre-edit snapshot: %s local RPC, no LSP requests or waits", time.Since(started))
	if err := os.WriteFile(path, []byte("package main\nvar X int = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call("workspace_resync", map[string]any{"root": root})
	after := call("huyang_diagnostic_evidence", map[string]any{"files": []string{path}, "wait_ms": 3000})
	assertProof(after, false)
}
