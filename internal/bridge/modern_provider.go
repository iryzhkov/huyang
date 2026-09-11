package bridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func huyangHeadlessInit() string {
	if value := os.Getenv("HUYANG_HEADLESS_INIT"); value != "" {
		return value
	}
	return os.Getenv("AGENT99_HEADLESS_INIT")
}

// providerSlot owns the canonical provider of one workspace. Spawning and
// health-checking a provider can take seconds, so each slot has its own lock:
// d.providerMu only guards the slot map and is never held across a provider
// operation, which keeps one workspace's slow spawn from stalling every other
// workspace's provider lookups.
type providerSlot struct {
	mu      sync.Mutex
	backend provider.Provider
}

func (d *directWorkspaces) providerSlotFor(id workspacecore.ID) *providerSlot {
	d.providerMu.Lock()
	defer d.providerMu.Unlock()
	slot := d.providers[id]
	if slot == nil {
		slot = &providerSlot{}
		d.providers[id] = slot
	}
	return slot
}

// canonicalProvider returns the owned Neovim provider for a modern workspace.
// Modern workspaces are durable while provider processes are replaceable, so
// the provider is started lazily and recreated after a daemon or provider
// restart without changing the workspace ID.
func (d *directWorkspaces) canonicalProvider(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	return d.workspaceProvider(ctx, workspace, false)
}

func (d *directWorkspaces) workspaceProvider(ctx context.Context, workspace *workspacecore.Workspace, debug bool) (provider.Provider, error) {
	identity := workspace.Identity()
	if identity.Kind != workspacecore.KindProject {
		return nil, fmt.Errorf("semantic provider requires a project workspace")
	}
	slot := d.providerSlotFor(identity.ID)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if existing := slot.backend; existing != nil {
		health := existing.Health(ctx)
		if health.State == provider.HealthHealthy || health.State == provider.HealthStarting {
			workspace.SyncProviderEpoch(existing.Descriptor().Epoch)
			return existing, nil
		}
		_ = existing.Close(context.Background())
		slot.backend = nil
	}
	backend, err := openReferenceProvider(identity.Root, debug)
	if err != nil {
		return nil, err
	}
	slot.backend = backend
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	return backend, nil
}

func (d *directWorkspaces) restartCanonicalProvider(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	slot := d.providerSlotFor(workspace.Identity().ID)
	slot.mu.Lock()
	if existing := slot.backend; existing != nil {
		_ = existing.Close(context.Background())
		slot.backend = nil
	}
	slot.mu.Unlock()
	return d.canonicalProvider(ctx, workspace)
}

// closeProviders shuts every provider and rolls back every sandbox stager.
// The maps are detached under d.providerMu and the slow work happens outside
// it so a stager still inside a long operation cannot stall the map.
func (d *directWorkspaces) closeProviders() {
	d.providerMu.Lock()
	slots := d.providers
	stagers := d.sandboxStagers
	d.providers = make(map[workspacecore.ID]*providerSlot)
	d.sandboxStagers = make(map[stagerKey]*sandboxPlanStager)
	d.providerMu.Unlock()
	for _, slot := range slots {
		slot.mu.Lock()
		if slot.backend != nil {
			_ = slot.backend.Close(context.Background())
			slot.backend = nil
		}
		slot.mu.Unlock()
	}
	for key, stager := range stagers {
		_ = stager.Rollback(context.Background(), key.planID)
	}
}

func modernProviderTarget(workspace *workspacecore.Workspace, target map[string]any) (map[string]any, error) {
	if locator, ok := target["symbol_locator"].(map[string]any); ok {
		path, _ := locator["path"].(string)
		name, _ := locator["name_path"].(string)
		if path == "" || name == "" {
			return nil, fmt.Errorf("symbol_locator requires path and name_path")
		}
		read, err := workspace.Read(path)
		if err != nil {
			return nil, err
		}
		leaf := name
		if slash := strings.LastIndexAny(leaf, "/."); slash >= 0 {
			leaf = leaf[slash+1:]
		}
		offset := bytes.Index(read.Content, []byte(leaf))
		if offset < 0 {
			return nil, fmt.Errorf("symbol %q is not present in %s; refresh the locator", name, path)
		}
		lineStart := bytes.LastIndex(read.Content[:offset], []byte{'\n'}) + 1
		return map[string]any{
			"file": filepath.Join(workspace.Identity().Root, filepath.FromSlash(path)),
			"line": bytes.Count(read.Content[:offset], []byte{'\n'}) + 1,
			"col":  offset - lineStart + 1, "symbol": leaf,
		}, nil
	}
	var handle workspacecore.RangeHandle
	symbolName := ""
	if opaque, ok := target["handle"].(string); ok && opaque != "" {
		resolution, err := workspace.ResolveHandle(workspacecore.HandleID(opaque))
		if err != nil {
			return nil, err
		}
		if resolution.Status == workspacecore.ResolutionConflicted {
			return nil, fmt.Errorf("%s", resolution.Code)
		}
		handle, err = resolution.RangeHandle()
		if err != nil {
			return nil, err
		}
		if resolution.Current != nil {
			symbolName = resolution.Current.NamePath
		} else {
			symbolName = resolution.Original.NamePath
		}
	} else if encoded, ok := target["file_range"].(map[string]any); ok {
		decoded, err := decodeRangeHandle(encoded)
		if err != nil {
			return nil, err
		}
		handle = decoded
	} else {
		return nil, fmt.Errorf("target must contain handle, file_range, or symbol_locator")
	}
	read, err := workspace.Read(handle.Path)
	if err != nil {
		return nil, err
	}
	if handle.ByteStart < 0 || handle.ByteStart > len(read.Content) || handle.ByteEnd < handle.ByteStart || handle.ByteEnd > len(read.Content) {
		return nil, fmt.Errorf("semantic target is outside the current document")
	}
	targetStart := handle.ByteStart
	selected := strings.TrimSpace(string(read.Content[handle.ByteStart:handle.ByteEnd]))
	if symbolName != "" {
		leaf := symbolName
		if slash := strings.LastIndexAny(leaf, "/."); slash >= 0 {
			leaf = leaf[slash+1:]
		}
		if relative := bytes.Index(read.Content[handle.ByteStart:handle.ByteEnd], []byte(leaf)); relative >= 0 {
			targetStart += relative
			selected = leaf
		}
	}
	lineStart := bytes.LastIndex(read.Content[:targetStart], []byte{'\n'}) + 1
	return map[string]any{
		"file": filepath.Join(workspace.Identity().Root, filepath.FromSlash(handle.Path)),
		"line": bytes.Count(read.Content[:targetStart], []byte{'\n'}) + 1,
		"col":  targetStart - lineStart + 1, "symbol": selected,
	}, nil
}
