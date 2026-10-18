package embed

import (
	"os"
	"path/filepath"
	"testing"
)

// This checks bootstrap before workspace_support or any buffer-loading tool:
// background workspace warming must not be required to select the interpreter.
func TestBootstrapConfiguresCanonicalPythonEnvironment(t *testing.T) {
	root := t.TempDir()
	python := filepath.Join(root, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(python), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, []byte("# fixture interpreter; never executed\n"), 0755); err != nil {
		t.Fatal(err)
	}
	backend, _, _ := testBackend(t, func(config *Config) { config.PythonEnvironmentRoot = root })
	backend.mu.Lock()
	g := backend.generation
	backend.mu.Unlock()
	var configured string
	if err := g.nvim.ExecLua(`return require("huyang.python_environment").interpreter()`, &configured); err != nil {
		t.Fatal(err)
	}
	if configured != python {
		t.Fatalf("first-buffer environment = %q, want %q", configured, python)
	}
}
