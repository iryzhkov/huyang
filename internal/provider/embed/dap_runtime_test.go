package embed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
)

// nvimDapForTests resolves the nvim-dap checkout the debugger suites use, in
// the same order as tests/minimal_init.lua: the HUYANG_NVIM_DAP_PATH
// override, the pinned clone under tests/.deps, then the lazy.nvim clone of
// the user's Neovim. Empty when none exists.
func nvimDapForTests(t *testing.T) string {
	t.Helper()
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	candidates := []string{}
	if override := os.Getenv("HUYANG_NVIM_DAP_PATH"); override != "" {
		candidates = append(candidates, override)
	} else {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			if home, err := os.UserHomeDir(); err == nil {
				data = filepath.Join(home, ".local", "share")
			}
		}
		candidates = append(candidates, filepath.Join(repo, "tests", ".deps", "nvim-dap"))
		if data != "" {
			candidates = append(candidates, filepath.Join(data, "nvim", "lazy", "nvim-dap"))
		}
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "lua", "dap.lua")); err == nil {
			return candidate
		}
	}
	return ""
}

func TestDapRuntimeIsDiscoveredFromHost(t *testing.T) {
	path := nvimDapForTests(t)
	if path == "" {
		t.Skip("nvim-dap not found; run tests/fetch-nvim-dap.sh or set HUYANG_NVIM_DAP_PATH")
	}
	t.Setenv("HUYANG_NVIM_DAP_PATH", path)
	backend, _, _ := testBackend(t, false)
	runtime := backend.DapRuntime()
	if !runtime.Available || runtime.Path != path || runtime.Source != "HUYANG_NVIM_DAP_PATH" {
		t.Fatalf("dap runtime = %#v, want %s discovered through the override", runtime, path)
	}
	health := backend.Health(context.Background())
	if health.State != provider.HealthHealthy || !strings.Contains(health.Detail, "nvim-dap runtime: "+path) {
		t.Fatalf("health = %#v, want healthy with the nvim-dap path in its detail", health)
	}
	_, err := backend.Call(context.Background(), provider.Request{
		Context: provider.RequestContext{RequestID: "threads"}, Operation: "debug_threads",
	})
	if provider.ErrorCode(err) == "dap_runtime_unavailable" {
		t.Fatalf("debugger reported the runtime unavailable although it was discovered: %v", err)
	}
}

func TestDapRuntimeAbsenceIsReportedWithoutKillingProvider(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("HUYANG_NVIM_DAP_PATH", empty)
	backend, _, _ := testBackend(t, false)
	runtime := backend.DapRuntime()
	if runtime.Available || !strings.Contains(runtime.Detail, "HUYANG_NVIM_DAP_PATH") ||
		len(runtime.Searched) != 1 || !strings.Contains(runtime.Searched[0], empty) {
		t.Fatalf("dap runtime = %#v, want absent with the override named as searched", runtime)
	}
	health := backend.Health(context.Background())
	if health.State != provider.HealthHealthy || !strings.Contains(health.Detail, "nvim-dap runtime: absent") {
		t.Fatalf("health = %#v, want healthy with an absent nvim-dap runtime in its detail", health)
	}
	epoch := backend.Descriptor().Epoch
	for _, operation := range []string{"debug_threads", "debug_breakpoints", "debug_stop"} {
		_, err := backend.Call(context.Background(), provider.Request{
			Context: provider.RequestContext{RequestID: operation}, Operation: operation,
		})
		var failure *provider.ProviderError
		if !errors.As(err, &failure) || failure.Code != "dap_runtime_unavailable" {
			t.Fatalf("%s error = %#v, want the dap_runtime_unavailable code", operation, err)
		}
		if !strings.Contains(failure.Message, "HUYANG_NVIM_DAP_PATH") || !strings.Contains(failure.Message, "runtime") {
			t.Fatalf("%s message %q should name the override and the missing runtime", operation, failure.Message)
		}
		if !strings.Contains(failure.Detail, empty) {
			t.Fatalf("%s detail %q should list the searched override directory", operation, failure.Detail)
		}
	}
	if _, err := callTree(context.Background(), backend, backend.config.Root, "tree-after"); err != nil {
		t.Fatalf("provider should keep serving after an unavailable debugger call: %v", err)
	}
	if backend.Descriptor().Epoch != epoch {
		t.Fatalf("epoch changed from %d to %d: the generation was replaced", epoch, backend.Descriptor().Epoch)
	}
	if health := backend.Health(context.Background()); health.State != provider.HealthHealthy {
		t.Fatalf("health after debugger calls = %#v, want healthy", health)
	}
}
