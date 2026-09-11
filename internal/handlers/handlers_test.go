package handlers

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// TestMain installs the output-schema validator on every envelope the
// handlers produce, so any test that drives a handler fails when the result
// would violate the advertised schema.
func TestMain(m *testing.M) {
	mcpapi.SetEnvelopeAudit(func(tool string, envelope map[string]any) {
		if err := mcpapi.ValidateOutput(envelope); err != nil {
			panic(fmt.Sprintf("tool %s produced an envelope that violates the output schema: %v\n%#v", tool, err, envelope))
		}
	})
	os.Exit(m.Run())
}

func assertModernOutputValid(t *testing.T, value map[string]any) {
	t.Helper()
	if err := mcpapi.ValidateOutput(value); err != nil {
		t.Fatalf("invalid modern output: %v\n%#v", err, value)
	}
}

// memoryRegistry is an in-memory WorkspaceLookup: nothing is persisted and
// every adopted workspace is new.
type memoryRegistry struct {
	mu    sync.Mutex
	items map[workspacecore.ID]*workspacecore.Workspace
}

func (r *memoryRegistry) Lookup(id workspacecore.ID) *workspacecore.Workspace {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.items[id]
}

func (r *memoryRegistry) Adopt(opened *workspacecore.Workspace, _ []string) (*workspacecore.Workspace, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = make(map[workspacecore.ID]*workspacecore.Workspace)
	}
	r.items[opened.Identity().ID] = opened
	return opened, true, nil
}

func (r *memoryRegistry) PersistIdentity(workspacecore.ID) error { return nil }

// noProvenance is a RevisionProvenance with no receipts.
type noProvenance struct{}

func (noProvenance) RecordedRevisionDiffs(string, uint64, uint64) []RecordedRevisionDiff { return nil }
func (noProvenance) CanonicalChangedPaths(workspacecore.ID, uint64) ([]string, error) {
	return nil, fmt.Errorf("changed_file_evidence_incomplete: no receipts")
}

// fixedFactory hands out one provider for every open.
type fixedFactory struct{ backend provider.Provider }

func (f fixedFactory) Open(providerpool.OpenConfig) (provider.Provider, error) {
	return f.backend, nil
}

// newTestHandlers builds handlers over an in-memory registry, no receipts
// and a pool that opens providers through factory.
func newTestHandlers(t *testing.T, factory providerpool.Factory) *Handlers {
	t.Helper()
	stateDir := t.TempDir()
	pool := providerpool.New(stateDir+"/sandboxes", factory)
	t.Cleanup(pool.Close)
	return New(Config{
		Registry: &memoryRegistry{}, Provenance: noProvenance{}, Pool: pool,
		StateDir: stateDir, ToolTimeout: providerpool.DefaultCallTimeout,
		SchedulerInfo: func() map[string]any { return map[string]any{} },
	})
}
