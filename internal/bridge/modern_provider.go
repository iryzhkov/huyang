package bridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func huyangHeadlessInit() string {
	if value := os.Getenv("HUYANG_HEADLESS_INIT"); value != "" {
		return value
	}
	return os.Getenv("AGENT99_HEADLESS_INIT")
}

// canonicalProvider returns the owned Neovim provider for a modern workspace.
// Modern workspaces are durable while provider processes are replaceable, so
// the provider is started lazily and recreated after a daemon or provider
// restart without changing the workspace ID.
func (d *directWorkspaces) canonicalProvider(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	identity := workspace.Identity()
	if identity.Kind != workspacecore.KindProject {
		return nil, fmt.Errorf("semantic provider requires a project workspace")
	}
	d.providerMu.Lock()
	defer d.providerMu.Unlock()
	if existing := d.providers[identity.ID]; existing != nil {
		health := existing.Health(ctx)
		if health.State == provider.HealthHealthy || health.State == provider.HealthStarting {
			workspace.SyncProviderEpoch(existing.Descriptor().Epoch)
			return existing, nil
		}
		_ = existing.Close(context.Background())
		delete(d.providers, identity.ID)
	}
	backend, err := referenceProviders.Open(providerOpenConfig{
		Root: identity.Root, InitFile: huyangHeadlessInit(),
		RuntimePath: shippedRuntimePath(), Debug: false,
	})
	if err != nil {
		return nil, err
	}
	d.providers[identity.ID] = backend
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	return backend, nil
}

func (d *directWorkspaces) restartCanonicalProvider(ctx context.Context, workspace *workspacecore.Workspace) (provider.Provider, error) {
	identity := workspace.Identity()
	d.providerMu.Lock()
	if existing := d.providers[identity.ID]; existing != nil {
		_ = existing.Close(context.Background())
		delete(d.providers, identity.ID)
	}
	d.providerMu.Unlock()
	return d.canonicalProvider(ctx, workspace)
}

func callCanonicalProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, backend provider.Provider, operation string, arguments map[string]any) (any, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	callContext := ctx
	if callContext == nil {
		callContext = context.Background()
	}
	if _, ok := callContext.Deadline(); !ok {
		var cancel context.CancelFunc
		callContext, cancel = context.WithTimeout(callContext, defaultToolCallTimeout)
		defer cancel()
	}
	deadline, _ := callContext.Deadline()
	descriptor := backend.Descriptor()
	result, err := backend.Call(callContext, provider.Request{
		Context: provider.RequestContext{
			RequestID: requestID, WorkspaceID: string(workspace.Identity().ID),
			Epoch: descriptor.Epoch, Deadline: deadline, Cancellation: descriptor.Cancellation,
		},
		Operation: operation, Arguments: arguments,
	})
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

func canonicalProviderStatus(backend provider.Provider) map[string]any {
	descriptor := backend.Descriptor()
	health := backend.Health(context.Background())
	return map[string]any{
		"state": health.State, "detail": health.Detail, "failure_code": health.FailureCode,
		"backend": descriptor.Backend, "provider_id": descriptor.ID,
		"epoch": descriptor.Epoch, "process_id": descriptor.ProcessID,
		"endpoint": descriptor.Endpoint, "languages": descriptor.Languages,
		"capabilities": descriptor.Capabilities, "observed_at": health.ObservedAt,
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
	lineStart := bytes.LastIndex(read.Content[:handle.ByteStart], []byte{'\n'}) + 1
	selected := strings.TrimSpace(string(read.Content[handle.ByteStart:handle.ByteEnd]))
	return map[string]any{
		"file": filepath.Join(workspace.Identity().Root, filepath.FromSlash(handle.Path)),
		"line": bytes.Count(read.Content[:handle.ByteStart], []byte{'\n'}) + 1,
		"col":  handle.ByteStart - lineStart + 1, "symbol": selected,
	}, nil
}
