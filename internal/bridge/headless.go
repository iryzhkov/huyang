package bridge

// Headless workspaces for the standalone MCP server. When the bridge is not
// launched from inside Neovim (no $AGENT99_NVIM), a client such as Claude
// Code first calls open_workspace(root): the bridge spawns
// `nvim --headless --listen <socket>` in that root with the user's normal
// configuration, so the same LSP servers attach as in an interactive
// session. Several roots can be open at once, and each tool call is routed
// to the instance that owns the path it names (see routing.go). An instance
// is killed when its workspace is closed or the server exits.
//
// Open workspaces never overlap: a root that contains, or is contained by,
// an open one is refused. Two instances over one file tree would each hold
// their own buffers for the same files, and an edit made in one would be
// lost the moment the other wrote.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent99/internal/provider"
	workspacecore "agent99/internal/workspace"
)

const (
	// Each workspace is a Neovim with a full set of language servers - a
	// gopls over a large repository is a gigabyte by itself - so how many
	// run at once is capped rather than left to the agent.
	defaultMaxWorkspaces = 10
)

type headlessWorkspace struct {
	Root      string
	Providers *provider.AnalysisProfile
	Provider  provider.Provider
	Workspace *workspacecore.Workspace
	// lastUsed is when a call was last routed here, which is what the idle
	// sweep in lifecycle.go measures. Guarded by headlessMu.
	lastUsed time.Time
	// inFlight counts the calls running here right now. A workspace with
	// one is in use whatever lastUsed says: check_project can run for ten
	// minutes, and the idle sweep must not close it from under that call.
	// Guarded by headlessMu.
	inFlight int
}

func (w *headlessWorkspace) session() session {
	return session{
		Root: w.Root, Providers: w.Providers, Provider: w.Provider,
		Workspace: w.Workspace, Headless: true,
	}
}

var (
	headlessMu sync.Mutex
	workspaces = map[string]*headlessWorkspace{}
)

func maxWorkspaces() int {
	if v := os.Getenv("AGENT99_MAX_WORKSPACES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxWorkspaces
}

// liveLocked drops the workspaces whose Neovim has exited and returns the
// rest. An instance can die underneath the server (a crash, an OOM kill),
// and a stale entry would route calls to a socket nobody answers on.
func liveLocked() map[string]*headlessWorkspace {
	for root, ws := range workspaces {
		select {
		case <-ws.Provider.Done():
			delete(workspaces, root)
			forgetRoot(root)
			// The per-client state this instance held - ledger, baselines,
			// what each client had been shown - died with it, so the roster
			// of clients using it means nothing now.
			forgetHolders(root)
			// Nobody asked for this one to go, so the work that was being
			// done in it probably is not finished. lifecycle.go starts it
			// again on the next call that needs it.
			noteReopenableLocked(root, "its Neovim went away")
		default:
		}
	}
	return workspaces
}

func rootsLocked() []string {
	live := liveLocked()
	out := make([]string, 0, len(live))
	for root := range live {
		out = append(out, root)
	}
	sort.Strings(out)
	return out
}

// openRoots lists the roots of the open workspaces, sorted.
func openRoots() []string {
	headlessMu.Lock()
	defer headlessMu.Unlock()
	return rootsLocked()
}

// workspaceAt returns the open workspace whose root is exactly this path.
func workspaceAt(root string) *headlessWorkspace {
	if root == "" {
		return nil
	}
	headlessMu.Lock()
	defer headlessMu.Unlock()
	return liveLocked()[root]
}

// workspaceFor returns the open workspace whose root contains path (or is
// path). Roots never overlap, so at most one can match.
func workspaceFor(path string) *headlessWorkspace {
	if path == "" {
		return nil
	}
	headlessMu.Lock()
	defer headlessMu.Unlock()
	for root, ws := range liveLocked() {
		if underRoot(root, path) {
			return ws
		}
	}
	return nil
}

// underRoot reports whether path is root or lies inside it.
func underRoot(root, path string) bool {
	if root == path {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(root, string(os.PathSeparator))+string(os.PathSeparator))
}

// usableRoot refuses the two directories that are not a project tree: the
// home directory, which holds every project on the machine plus its caches
// and dotfiles, and a filesystem root, which holds the machine. The refusal
// is not about taste. Everything that walks a workspace walks all of it -
// project_files behind find_symbol and workspace_map, and the language
// servers doing their own indexing - and the friction spool measured what
// that costs: single edits in a workspace rooted at the home directory took
// 244s, 255s and 476s, against a median of 1.4s for the same tool elsewhere.
//
// AGENT99_ALLOW_WIDE_ROOT=1 lifts the refusal, for a session that means it.
func usableRoot(abs string) error {
	if os.Getenv("AGENT99_ALLOW_WIDE_ROOT") != "" {
		return nil
	}
	if abs == filepath.Dir(abs) {
		return fmt.Errorf("%s is a filesystem root, not a project: opening it would "+
			"point the language servers at the whole machine. Open the project "+
			"directory instead, or set AGENT99_ALLOW_WIDE_ROOT=1 if that is really "+
			"what you want", abs)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	if abs == filepath.Clean(home) {
		return fmt.Errorf("%s is the home directory, not a project root: every tool "+
			"that walks the tree would walk every project, cache and dotfile under it "+
			"(measured here: single edits in a workspace rooted at the home directory "+
			"took 4 to 8 minutes, against a median of 1.4s elsewhere). Open the "+
			"repository or config directory the work is in - ~/Work/<project>, "+
			"~/.config/hypr - or set AGENT99_ALLOW_WIDE_ROOT=1 to open the home "+
			"directory anyway", abs)
	}
	return nil
}

// openWorkspace starts (or reuses) a headless Neovim rooted at root, and
// makes it the active workspace. Reopening the same root is a no-op; a root
// that shares a file tree with an open workspace is refused, and so is one
// past the workspace limit and one that is not a project tree at all
// (usableRoot).
func openWorkspace(root string) (*headlessWorkspace, error) {
	if root == "" {
		return nil, errors.New("open_workspace needs a root directory")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace root: %v", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory: %s", abs)
	}
	if err := usableRoot(abs); err != nil {
		return nil, err
	}

	headlessMu.Lock()
	defer headlessMu.Unlock()
	live := liveLocked()
	if ws := live[abs]; ws != nil {
		if ws.Provider.Health(context.Background()).State == provider.HealthHealthy {
			ws.Workspace.SyncProviderEpoch(ws.Provider.Descriptor().Epoch)
			noteRouted(stickyActive, abs)
			forgetReopenableLocked(abs)
			ws.lastUsed = time.Now()
			return ws, nil
		}
		delete(workspaces, abs)
		stopWorkspace(ws)
		forgetRoot(abs)
	}
	for _, other := range rootsLocked() {
		if underRoot(other, abs) || underRoot(abs, other) {
			return nil, fmt.Errorf("%s shares a file tree with the open workspace %s. "+
				"One Neovim per tree: two instances would hold their own buffers for the "+
				"same files, and an edit made in one is lost when the other writes. Close "+
				"%s first, or open a root beside it rather than inside or around it",
				abs, other, other)
		}
	}
	if other, pid := referenceProviders.FindForeign(abs); other != "" {
		return nil, fmt.Errorf("%s is already open in another agent99 bridge (process %d, "+
			"socket %s). One Neovim per tree: two instances would hold their own buffers for "+
			"the same files, and an edit made in one is lost when the other writes. This "+
			"bridge will not start a second one - work through the bridge that has it, or "+
			"wait for that process to finish with the root",
			abs, pid, other)
	}
	if max := maxWorkspaces(); len(workspaces) >= max {
		return nil, fmt.Errorf("%d workspaces are already open (%s) and the limit is %d; "+
			"close one of your own with close_workspace, or wait and retry - a workspace "+
			"is a Neovim with its own language servers, and the roots above may belong to "+
			"other agents sharing this server, whose slots free up when they finish. Raise "+
			"AGENT99_MAX_WORKSPACES in the server's environment if this machine has the "+
			"memory for more",
			len(workspaces), strings.Join(rootsLocked(), ", "), max)
	}

	backend, err := referenceProviders.Open(providerOpenConfig{
		Root:        abs,
		InitFile:    os.Getenv("AGENT99_HEADLESS_INIT"),
		RuntimePath: shippedRuntimePath(),
		Debug:       debugEnabled(),
	})
	if err != nil {
		return nil, err
	}
	registration := provider.Registration{
		Provider:     backend,
		Languages:    []string{"*"},
		Capabilities: backend.Descriptor().Capabilities,
		Role:         provider.RolePrimary,
	}
	profile, err := provider.NewAnalysisProfile("default", []provider.Registration{registration})
	if err != nil {
		_ = backend.Close(context.Background())
		return nil, err
	}
	core, err := workspacecore.New(workspacecore.KindProject, abs, backend.Descriptor().Epoch)
	if err != nil {
		_ = backend.Close(context.Background())
		return nil, err
	}
	ws := &headlessWorkspace{
		Root:      abs,
		Providers: profile,
		Provider:  backend,
		Workspace: core,
		lastUsed:  time.Now(),
	}
	workspaces[abs] = ws
	noteRouted(stickyActive, abs)
	forgetReopenableLocked(abs)
	return ws, nil
}

// closeWorkspaces stops the workspaces with these roots and returns the ones
// it actually stopped. The entries leave the map before the instances are
// told to quit, so a stop that takes its full timeout does not hold the lock
// against the rest of the server.
func closeWorkspaces(roots []string) []string {
	headlessMu.Lock()
	live := liveLocked()
	var targets []*headlessWorkspace
	for _, root := range roots {
		if ws := live[root]; ws != nil {
			targets = append(targets, ws)
		}
	}
	for _, ws := range targets {
		delete(workspaces, ws.Root)
		forgetRoot(ws.Root)
		forgetHolders(ws.Root)
		// An explicit close means it. The idle sweep marks its own roots
		// reopenable after calling this, because that close is the server's
		// decision rather than the agent's.
		forgetReopenableLocked(ws.Root)
	}
	headlessMu.Unlock()

	var wg sync.WaitGroup
	closed := make([]string, 0, len(targets))
	for _, ws := range targets {
		closed = append(closed, ws.Root)
		wg.Add(1)
		go func(ws *headlessWorkspace) {
			defer wg.Done()
			stopWorkspace(ws)
		}(ws)
	}
	wg.Wait()
	sort.Strings(closed)
	return closed
}

// closeAllWorkspaces stops every open workspace. Called on the way out, so
// the server never leaves a headless Neovim behind.
func closeAllWorkspaces() []string {
	return closeWorkspaces(openRoots())
}

// stopWorkspace ends one instance. The caller has already taken it out of
// the map.
func stopWorkspace(ws *headlessWorkspace) {
	_ = ws.Provider.Close(context.Background())
}

// Edits made by the symbol tools land in buffers; with no user at the
// keyboard they must reach disk on their own, otherwise a client reading
// files directly would see stale content. (BufModifiedSet does not fire for
// API edits to hidden buffers, so an autocmd cannot do this.)
var editTools = map[string]bool{
	"replace_symbol_body":  true,
	"replace_symbol_lines": true,
	"insert_after_symbol":  true,
	"insert_before_symbol": true,
	"insert_lines":         true,
	"apply_code_action":    true,
	"undo_edit":            true,
	"rename_symbol":        true,
	"replace_pattern":      true,
	// The file-lifecycle tools write the file themselves, but a server may
	// have rewritten other files' imports in response, and those land in
	// buffers like any other edit.
	"create_file":  true,
	"move_file":    true,
	"delete_file":  true,
	"move_symbols": true,
}

// headlessSaveAll writes every modified file buffer of the headless
// instance and reports the first failures.
func headlessSaveAll(ses session) error {
	if ses.Provider == nil {
		return nil
	}
	return ses.Provider.Save(context.Background())
}
