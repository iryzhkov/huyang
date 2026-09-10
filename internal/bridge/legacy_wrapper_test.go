package bridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent99/internal/provider"
	workspacecore "agent99/internal/workspace"
)

func legacyTestSession(t *testing.T, root, stateDir string, direct bool) session {
	t.Helper()
	backend, err := referenceProviders.Open(providerOpenConfig{
		Root: root, InitFile: os.Getenv("AGENT99_HEADLESS_INIT"), RuntimePath: shippedRuntimePath(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close(context.Background()) })
	profile, err := provider.NewAnalysisProfile("default", []provider.Registration{{
		Provider: backend, Languages: []string{"*"}, Capabilities: backend.Descriptor().Capabilities,
		Role: provider.RolePrimary,
	}})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := workspacecore.Open(workspacecore.OpenOptions{
		Kind: workspacecore.KindProject, Root: root, StateDir: stateDir, ProviderEpoch: backend.Descriptor().Epoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session{
		Context: context.Background(), Root: root, Providers: profile, Provider: backend,
		Workspace: workspace, Headless: true, Client: "snapshot-client", LegacyDirect: direct,
	}
}

func TestLegacyCreateWrapperMatchesDirectSnapshotAndCommitsJournal(t *testing.T) {
	previousFactory := referenceProviders
	referenceProviders = configuredProviderFactory{backend: "embed"}
	defer func() { referenceProviders = previousFactory }()
	runtimeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT99_RUNTIME_PATH", runtimeRoot)
	t.Setenv("AGENT99_HEADLESS_INIT", filepath.Join(runtimeRoot, "tests", "minimal_init.lua"))
	t.Setenv("HUYANG_LEGACY_STATE_DIR", t.TempDir())

	direct := legacyTestSession(t, t.TempDir(), t.TempDir(), true)
	transactionalState := t.TempDir()
	transactional := legacyTestSession(t, t.TempDir(), transactionalState, false)
	t.Cleanup(func() { cleanupLegacyWorkspace(transactional) })
	arguments := map[string]any{"file": "created.lua", "text": "local value = 1\nreturn value\n"}
	directOut, err := callTool("create_file", arguments, direct)
	if err != nil {
		t.Fatal(err)
	}
	if err := headlessSaveAll(direct); err != nil {
		t.Fatal(err)
	}
	transactionalOut, err := callTool("create_file", arguments, transactional)
	if err != nil {
		t.Fatal(err)
	}
	if directOut != transactionalOut {
		t.Fatalf("transactional snapshot differs\ndirect: %s\nwrapped: %s", directOut, transactionalOut)
	}
	directBytes, err := os.ReadFile(filepath.Join(direct.Root, "created.lua"))
	if err != nil {
		t.Fatal(err)
	}
	transactionalBytes, err := os.ReadFile(filepath.Join(transactional.Root, "created.lua"))
	if err != nil {
		t.Fatal(err)
	}
	if string(directBytes) != string(transactionalBytes) {
		t.Fatalf("committed bytes differ: direct=%q wrapped=%q", directBytes, transactionalBytes)
	}
	journals, err := filepath.Glob(filepath.Join(transactionalState, "commit-journals", "*", "legacy_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 1 {
		t.Fatalf("committed journals = %v", journals)
	}
}

func TestLegacyWrapperEscapeFlagsRetainDirectPath(t *testing.T) {
	t.Setenv("HUYANG_LEGACY_WRAPPERS", "direct")
	if legacyTransactionsEnabled("create_file", map[string]any{}) {
		t.Fatal("global direct escape did not disable wrapper")
	}
	t.Setenv("HUYANG_LEGACY_WRAPPERS", "")
	t.Setenv("HUYANG_LEGACY_CREATE_FILE_PATH", "direct")
	if legacyTransactionsEnabled("create_file", map[string]any{}) {
		t.Fatal("per-tool direct escape did not disable wrapper")
	}
	if legacyTransactionsEnabled("create_file", map[string]any{"dry_run": true}) {
		t.Fatal("dry run entered transactional wrapper")
	}
	if legacyTransactionsEnabled("replace_symbol_body", map[string]any{"wait": false}) {
		t.Fatal("wait=false entered synchronous transactional wrapper")
	}
}
