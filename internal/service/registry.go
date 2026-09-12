package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// registryStateVersion 2 holds workspace definitions only. Version 1 also
// embedded every idempotency receipt with its full result payload, which is
// why the file grew without bound; a version 1 file is migrated on load into
// per-workspace receipt files and rewritten as version 2.
const (
	registryStateVersion       = 2
	legacyRegistryStateVersion = 1
)

type persistedRegistry struct {
	Version    int
	Workspaces []persistedWorkspace
	// Replays is read from version 1 files only and never written again.
	Replays []persistedReplay `json:",omitempty"`
}

type persistedWorkspace struct {
	ID            workspacecore.ID
	Kind          workspacecore.Kind
	Root          string
	Files         []string
	ProviderEpoch uint64
	StateSeq      uint64
}

// workspaceRegistry owns the canonical workspace definitions: the open
// workspace objects, their durable records and the registry.json file that
// makes the same workspace ID survive a service restart.
type workspaceRegistry struct {
	mu       sync.RWMutex
	items    map[workspacecore.ID]*workspacecore.Workspace
	records  map[workspacecore.ID]persistedWorkspace
	stateDir string
	path     string

	// persistMu serialises writers of registry.json.
	persistMu sync.Mutex
}

func newWorkspaceRegistry(stateDir string) *workspaceRegistry {
	return &workspaceRegistry{
		items:    make(map[workspacecore.ID]*workspacecore.Workspace),
		records:  make(map[workspacecore.ID]persistedWorkspace),
		stateDir: stateDir,
		path:     filepath.Join(stateDir, "registry.json"),
	}
}

// load restores every recorded workspace. It returns the receipts a version
// 1 registry embedded, with migrate set, so the caller can move them into
// the receipt store and rewrite the registry at version 2.
func (r *workspaceRegistry) load() (legacy []persistedReplay, migrate bool, err error) {
	content, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read registry: %w", err)
	}
	var state persistedRegistry
	if err := json.Unmarshal(content, &state); err != nil {
		return nil, false, fmt.Errorf("decode registry: %w", err)
	}
	if state.Version != registryStateVersion && state.Version != legacyRegistryStateVersion {
		return nil, false, fmt.Errorf("unsupported registry version %d", state.Version)
	}
	for _, record := range state.Workspaces {
		opened, err := workspacecore.Open(workspacecore.OpenOptions{
			Kind: record.Kind, Root: record.Root, Files: record.Files,
			ProviderEpoch: record.ProviderEpoch, StateSeq: record.StateSeq,
			StateDir: r.stateDir, Identity: record.ID, Sectioner: workspacecore.NativeSectioner{},
		})
		if err != nil {
			return nil, false, fmt.Errorf("restore workspace %s: %w", record.ID, err)
		}
		r.items[record.ID] = opened
		r.records[record.ID] = record
	}
	return state.Replays, state.Version == legacyRegistryStateVersion, nil
}

// Lookup returns the open workspace with the given ID, or nil.
func (r *workspaceRegistry) Lookup(id workspacecore.ID) *workspacecore.Workspace {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.items[id]
}

// Adopt records a freshly opened workspace unless an equivalent definition
// (same kind, root and file allowlist) is already registered, in which case
// the registered workspace is returned and created is false. A new record
// is persisted before it is reported; a persist failure leaves the registry
// unchanged.
func (r *workspaceRegistry) Adopt(opened *workspacecore.Workspace, files []string) (*workspacecore.Workspace, bool, error) {
	identity := opened.Identity()
	record := persistedWorkspace{
		ID: identity.ID, Kind: identity.Kind, Root: identity.Root, Files: append([]string(nil), files...),
		ProviderEpoch: identity.Epoch, StateSeq: identity.StateSeq,
	}
	r.mu.Lock()
	for id, existing := range r.records {
		if samePersistedWorkspace(existing, record) {
			reused := r.items[id]
			r.mu.Unlock()
			return reused, false, nil
		}
	}
	r.items[identity.ID] = opened
	r.records[identity.ID] = record
	r.mu.Unlock()
	if err := r.persist(); err != nil {
		r.mu.Lock()
		delete(r.items, identity.ID)
		delete(r.records, identity.ID)
		r.mu.Unlock()
		return nil, false, err
	}
	return opened, true, nil
}

// persist writes the workspace definitions. Receipts are persisted
// separately per workspace by the receipt store.
func (r *workspaceRegistry) persist() error {
	r.mu.Lock()
	records := make([]persistedWorkspace, 0, len(r.records))
	for id, record := range r.records {
		record.Files = append([]string(nil), record.Files...)
		if opened := r.items[record.ID]; opened != nil {
			identity := opened.Identity()
			record.ProviderEpoch = identity.Epoch
			record.StateSeq = identity.StateSeq
		}
		r.records[id] = record
		records = append(records, record)
	}
	r.mu.Unlock()
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })

	content, err := json.MarshalIndent(persistedRegistry{Version: registryStateVersion, Workspaces: records}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode registry: %w", err)
	}
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	if err := os.MkdirAll(r.stateDir, 0o700); err != nil {
		return err
	}
	return writeDurableFile(r.path, append(content, '\n'))
}

// PersistIdentity rewrites the registry when the workspace's epoch or state
// sequence moved since the last write; an unchanged identity is a no-op.
func (r *workspaceRegistry) PersistIdentity(id workspacecore.ID) error {
	r.mu.Lock()
	record, ok := r.records[id]
	opened := r.items[id]
	if !ok || opened == nil {
		r.mu.Unlock()
		return nil
	}
	identity := opened.Identity()
	if record.ProviderEpoch == identity.Epoch && record.StateSeq == identity.StateSeq {
		r.mu.Unlock()
		return nil
	}
	record.ProviderEpoch = identity.Epoch
	record.StateSeq = identity.StateSeq
	r.records[id] = record
	r.mu.Unlock()
	return r.persist()
}

func (r *workspaceRegistry) syncProviderEpoch(id workspacecore.ID, epoch uint64) error {
	opened := r.Lookup(id)
	if opened == nil {
		return fmt.Errorf("workspace %s not found", id)
	}
	opened.SyncProviderEpoch(epoch)
	return r.PersistIdentity(id)
}

func samePersistedWorkspace(left, right persistedWorkspace) bool {
	if left.Kind != right.Kind || left.Root != right.Root || len(left.Files) != len(right.Files) {
		return false
	}
	for index := range left.Files {
		if left.Files[index] != right.Files[index] {
			return false
		}
	}
	return true
}

func closedSignal() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
