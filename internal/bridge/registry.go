package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const registryStateVersion = 1

type persistedRegistry struct {
	Version    int
	Workspaces []persistedWorkspace
	Replays    []persistedReplay
}

type persistedWorkspace struct {
	ID            workspacecore.ID
	Kind          workspacecore.Kind
	Root          string
	Files         []string
	ProviderEpoch uint64
	StateSeq      uint64
}

type persistedReplay struct {
	Key           string
	ArgumentsHash string
	Result        map[string]any
}

func (d *directWorkspaces) loadRegistry() error {
	content, err := os.ReadFile(d.registryPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read registry: %w", err)
	}
	var state persistedRegistry
	if err := json.Unmarshal(content, &state); err != nil {
		return fmt.Errorf("decode registry: %w", err)
	}
	if state.Version != registryStateVersion {
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
	for _, replay := range state.Replays {
		if providerBackedReplay(replay.Result) {
			continue
		}
		d.replays[replay.Key] = &directReplay{
			argumentsHash: replay.ArgumentsHash,
			result:        replay.Result,
			done:          closedSignal(),
			complete:      true,
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

func (d *directWorkspaces) persistRegistry() error {
	d.persistMu.Lock()
	defer d.persistMu.Unlock()

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

	d.replayMu.Lock()
	replays := make([]persistedReplay, 0, len(d.replays))
	for key, replay := range d.replays {
		if replay.complete {
			replays = append(replays, persistedReplay{
				Key: key, ArgumentsHash: replay.argumentsHash, Result: cloneEnvelope(replay.result),
			})
		}
	}
	d.replayMu.Unlock()
	sort.Slice(replays, func(i, j int) bool { return replays[i].Key < replays[j].Key })

	content, err := json.MarshalIndent(persistedRegistry{
		Version: registryStateVersion, Workspaces: records, Replays: replays,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode registry: %w", err)
	}
	content = append(content, '\n')
	if err := os.MkdirAll(d.stateDir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(d.stateDir, ".registry-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, d.registryPath); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(d.registryPath))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
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
