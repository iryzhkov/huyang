package handlers

import (
	"path/filepath"
	"strings"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// canonicalNamePath turns the name-path shapes a caller may write into the
// slash-separated path the semantic kernel indexes and the workspace's
// symbol handles carry. Accepted shapes: "Name", "Parent/Name",
// "Parent.Name", "Parent::Name", "Parent#Name" and the Go receiver forms
// "(*Parent).Name" and "(Parent).Name"; every one of them becomes
// "Parent/Name".
func canonicalNamePath(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "(") {
		if closing := strings.Index(name, ")"); closing > 0 {
			receiver := strings.TrimPrefix(strings.TrimSpace(name[1:closing]), "*")
			rest := strings.TrimLeft(name[closing+1:], ".")
			name = receiver + "/" + rest
		}
	}
	name = strings.ReplaceAll(name, "::", "/")
	name = strings.ReplaceAll(name, "#", "/")
	// A dotted shape is only read as a path when the caller did not already
	// write a slash path; a slash path may legitimately contain dots.
	if !strings.Contains(name, "/") {
		name = strings.ReplaceAll(name, ".", "/")
	}
	return strings.Trim(name, "/")
}

// workspacePath turns an absolute path inside the workspace root into the
// slash-separated relative path the workspace uses in locators and
// handles; any other path is returned unchanged.
func workspacePath(workspace *workspacecore.Workspace, path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	relative, err := filepath.Rel(workspace.Identity().Root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return path
	}
	return filepath.ToSlash(relative)
}
