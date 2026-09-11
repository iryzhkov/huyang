package bridge

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

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
