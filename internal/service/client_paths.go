package service

// Client paths: the service is long-lived and shared, so its own working
// directory means nothing to any caller. The adapter a client starts does
// know where the client is, and says so in its hello. This file uses that
// directory for the two things agents assume it is used for: a relative
// root names a directory below it, and a call that names no workspace at
// all is about the repository the client was started in.
//
// Both are applied only when the answer is unambiguous: the client's
// directory and the resolved root must lie in a Git working tree, a default
// workspace is chosen only when the
// client was started at that tree's top level, and a call that names any
// absolute path keeps the document-workspace behaviour it already had.
// Every rewrite is reported in a warning that names the resolved root.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// clientConnection is what an adapter announced about its client.
type clientConnection struct {
	// session is the agent session the connection belongs to.
	session string
	// cwd is the client's working directory, absolute, or empty when the
	// connection did not announce one (an older adapter, or HTTP).
	cwd string
}

// defaultWorkspaceTools are the tools a call may leave the workspace
// unnamed for: the ones that only read. A mutation is not among them. Its
// guards check content, not intent - create_file needs no prior revision
// and replace_literal succeeds wherever the old text also occurs - so an
// edit meant for another repository could land here, and the warning that
// names the root would arrive after it had.
var defaultWorkspaceTools = map[string]bool{
	"workspace_open": true, "workspace_inspect": true, "search": true, "symbol_find": true,
	"navigate": true, "read": true, "diagnostics": true, "evidence_get": true, "code_actions": true,
	"revision_diff": true, "language_server_status": true,
}

// relativeRootTools may have a relative root resolved. A root the caller
// wrote names its workspace explicitly, so mutations are included here:
// "." is the caller's own statement of intent, resolved against the
// directory the caller's client was started in.
var relativeRootTools = map[string]bool{
	"edit_apply": true, "change_plan": true,
}

// resolveClientPaths rewrites a relative root against the client's working
// directory and supplies a default root when the call names no workspace,
// returning a warning for each rewrite. It never fails a call: anything it
// cannot resolve unambiguously is left for the handler to refuse as before.
func resolveClientPaths(tool string, arguments map[string]any, cwd string) []string {
	if cwd == "" || !filepath.IsAbs(cwd) || (!defaultWorkspaceTools[tool] && !relativeRootTools[tool]) {
		return nil
	}
	// A client started outside any repository did not say where its agent
	// works: Codex starts every MCP server from the home directory whatever
	// directory the thread is in, so ".t3/repo" against that directory names
	// a different checkout from the one the agent is looking at.
	top := workspacecore.GitTopLevel(cwd)
	if top == "" {
		return nil
	}
	if workspaceID, _ := arguments["workspace_id"].(string); strings.TrimSpace(workspaceID) != "" {
		return nil
	}
	root, _ := arguments["root"].(string)
	root = strings.TrimSpace(root)
	if root != "" {
		if filepath.IsAbs(root) {
			return nil
		}
		// filepath.Join would fold "link/.." back to the client directory
		// lexically while the filesystem follows link elsewhere; resolving
		// symbolic links first makes ".." mean what the agent's shell means.
		resolved, err := filepath.EvalSymlinks(cwd + string(filepath.Separator) + root)
		if err != nil || workspacecore.GitTopLevel(resolved) == "" {
			return nil
		}
		arguments["root"] = resolved
		return []string{fmt.Sprintf("root %q was resolved against your working directory to %s; send absolute roots", root, resolved)}
	}
	if !defaultWorkspaceTools[tool] {
		return nil
	}
	if tool == "workspace_open" && len(mcpapi.AnySlice(arguments["files"])) > 0 {
		return nil
	}
	if namesAbsolutePath(arguments) {
		return nil
	}
	// From a subdirectory, a relative path means one thing to the agent
	// (below its directory) and another to a workspace at the top level.
	if top != filepath.Clean(cwd) {
		return nil
	}
	arguments["root"] = top
	return []string{fmt.Sprintf("no workspace was named, so the repository your client was started in, %s, was used; pass root to name another", top)}
}

// namesAbsolutePath reports whether a call names any file by absolute path.
// Such a call already has a meaning without a workspace - read and
// edit_apply open an exact document workspace over it - and a default root
// must not change that meaning.
func namesAbsolutePath(arguments map[string]any) bool {
	absolute := func(value any) bool {
		text, _ := value.(string)
		return filepath.IsAbs(text)
	}
	named := func(object map[string]any) bool {
		for _, field := range []string{"path", "from", "to"} {
			if absolute(object[field]) {
				return true
			}
		}
		if locator, ok := object["symbol_locator"].(map[string]any); ok && absolute(locator["path"]) {
			return true
		}
		if target, ok := object["target"].(map[string]any); ok {
			if locator, ok := target["symbol_locator"].(map[string]any); ok && absolute(locator["path"]) {
				return true
			}
		}
		return false
	}
	if target, ok := arguments["target"].(map[string]any); ok && named(target) {
		return true
	}
	if operation, ok := arguments["operation"].(map[string]any); ok && named(operation) {
		return true
	}
	for _, list := range []string{"targets", "operations"} {
		for _, raw := range mcpapi.AnySlice(arguments[list]) {
			if object, ok := raw.(map[string]any); ok && named(object) {
				return true
			}
		}
	}
	for _, raw := range mcpapi.AnySlice(arguments["files"]) {
		if absolute(raw) {
			return true
		}
	}
	return false
}
