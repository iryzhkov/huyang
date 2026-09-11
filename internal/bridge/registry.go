package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

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

func (d *directWorkspaces) loadRegistry() error {
	content, err := os.ReadFile(d.registryPath)
	if errors.Is(err, os.ErrNotExist) {
		return d.loadReceipts()
	}
	if err != nil {
		return fmt.Errorf("read registry: %w", err)
	}
	var state persistedRegistry
	if err := json.Unmarshal(content, &state); err != nil {
		return fmt.Errorf("decode registry: %w", err)
	}
	if state.Version != registryStateVersion && state.Version != legacyRegistryStateVersion {
		return fmt.Errorf("unsupported registry version %d", state.Version)
	}
	for _, record := range state.Workspaces {
		opened, err := workspacecore.Open(workspacecore.OpenOptions{
			Kind: record.Kind, Root: record.Root, Files: record.Files,
			ProviderEpoch: record.ProviderEpoch, StateSeq: record.StateSeq,
			StateDir: d.stateDir, Identity: record.ID,
		})
		if err != nil {
			return fmt.Errorf("restore workspace %s: %w", record.ID, err)
		}
		d.items[record.ID] = opened
		d.records[record.ID] = record
	}
	if err := d.loadReceipts(); err != nil {
		return err
	}
	if state.Version == legacyRegistryStateVersion {
		if err := d.migrateLegacyReceipts(state.Replays); err != nil {
			return fmt.Errorf("migrate legacy receipts: %w", err)
		}
		if err := d.persistRegistry(); err != nil {
			return fmt.Errorf("rewrite legacy registry: %w", err)
		}
	}
	return nil
}

func providerBackedReplay(result map[string]any) bool {
	transaction, _ := result["transaction"].(map[string]any)
	state, _ := transaction["state"].(string)
	switch workspacecore.PlanState(state) {
	case workspacecore.PlanPreparing, workspacecore.PlanReady, workspacecore.PlanProvisional, workspacecore.PlanRollingBack, workspacecore.PlanConflicted:
		return true
	default:
		return false
	}
}

// persistRegistry writes the workspace definitions. Receipts are persisted
// separately per workspace by persistReceipts.
func (d *directWorkspaces) persistRegistry() error {
	d.mu.Lock()
	records := make([]persistedWorkspace, 0, len(d.records))
	for id, record := range d.records {
		record.Files = append([]string(nil), record.Files...)
		if opened := d.items[record.ID]; opened != nil {
			identity := opened.Identity()
			record.ProviderEpoch = identity.Epoch
			record.StateSeq = identity.StateSeq
		}
		d.records[id] = record
		records = append(records, record)
	}
	d.mu.Unlock()
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })

	content, err := json.MarshalIndent(persistedRegistry{Version: registryStateVersion, Workspaces: records}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode registry: %w", err)
	}
	d.persistMu.Lock()
	defer d.persistMu.Unlock()
	if err := os.MkdirAll(d.stateDir, 0o700); err != nil {
		return err
	}
	return writeDurableFile(d.registryPath, append(content, '\n'))
}

func (d *directWorkspaces) persistWorkspaceIdentity(id workspacecore.ID) error {
	d.mu.Lock()
	record, ok := d.records[id]
	opened := d.items[id]
	if !ok || opened == nil {
		d.mu.Unlock()
		return nil
	}
	identity := opened.Identity()
	if record.ProviderEpoch == identity.Epoch && record.StateSeq == identity.StateSeq {
		d.mu.Unlock()
		return nil
	}
	record.ProviderEpoch = identity.Epoch
	record.StateSeq = identity.StateSeq
	d.records[id] = record
	d.mu.Unlock()
	return d.persistRegistry()
}

func (d *directWorkspaces) syncProviderEpoch(id workspacecore.ID, epoch uint64) error {
	opened := d.get(id)
	if opened == nil {
		return fmt.Errorf("workspace %s not found", id)
	}
	opened.SyncProviderEpoch(epoch)
	return d.persistWorkspaceIdentity(id)
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
