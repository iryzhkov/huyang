package service

// End-to-end tests of the client-directory route: the adapter's hello,
// argument validation, path resolution, dispatch and replay, through the
// real Unix socket and HTTP transports rather than resolveClientPaths alone.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// routingRepository makes a directory that counts as a Git working tree
// (it holds .git) with the given files.
func routingRepository(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// shortSocketPath is a control socket path that stays under the 108-byte
// Unix socket bound. t.TempDir embeds the test's name, which for these tests
// is long enough to exceed it under any TMPDIR longer than a few characters,
// so the socket gets a short directory of its own under os.TempDir.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "control.sock")
}

// connectUnixClientWith connects to the service socket with the hello an
// adapter started in some directory would send.
func connectUnixClientWith(t *testing.T, socketPath string, hello controlHello) (*mcp.ClientSession, func()) {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(connection).Encode(hello); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-routing-test", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	session, err := client.Connect(context.Background(), &mcp.IOTransport{Reader: connection, Writer: connection}, nil)
	if err != nil {
		connection.Close()
		t.Fatal(err)
	}
	return session, func() {
		_ = session.Close()
		_ = connection.Close()
	}
}

// useAdapterDirectory makes the adapter announce dir as its client's
// working directory.
func useAdapterDirectory(t *testing.T, dir string) {
	t.Helper()
	previous := adapterWorkingDirectory
	adapterWorkingDirectory = func() (string, error) { return dir, nil }
	t.Cleanup(func() { adapterWorkingDirectory = previous })
}

// routedByDefault reports whether a reply says the client's directory chose
// its workspace.
func routedByDefault(reply map[string]any, repository string) bool {
	for _, warning := range mcpapi.AnySlice(reply["warnings"]) {
		if strings.Contains(fmt.Sprint(warning), "the repository your client was started in, "+repository+",") {
			return true
		}
	}
	return false
}

func replyWorkspaceID(t *testing.T, reply map[string]any) string {
	t.Helper()
	workspace, _ := reply["workspace"].(map[string]any)
	id, _ := workspace["id"].(string)
	if id == "" {
		t.Fatalf("reply names no workspace: %#v", reply)
	}
	return id
}

func replyContent(reply map[string]any) string {
	data, _ := reply["data"].(map[string]any)
	content, _ := data["content"].(string)
	return content
}

// The adapter names itself when the client exports no session id, and both
// that name and the client's directory are announced again on the
// connection that replaces a lost one.
func TestAdapterAnnouncesTheSameDirectoryAndSessionOnReconnect(t *testing.T) {
	t.Setenv("HUYANG_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	client := t.TempDir()
	useAdapterDirectory(t, client)
	socketPath := shortSocketPath(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	toAdapterReader, toAdapterWriter := io.Pipe()
	adapterDone := make(chan error, 1)
	go func() {
		adapterDone <- runHuyang([]string{"mcp", "--socket", socketPath}, toAdapterReader, io.Discard, io.Discard)
	}()
	accept := func() (controlHello, *net.UnixConn) {
		t.Helper()
		if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		connection, err := listener.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(connection).ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var hello controlHello
		if err := json.Unmarshal(line, &hello); err != nil {
			t.Fatal(err)
		}
		return hello, connection
	}
	first, connection := accept()
	// The service goes away; the adapter dials again.
	_ = connection.Close()
	second, reconnected := accept()
	defer reconnected.Close()
	if !strings.HasPrefix(first.Session, "adapter-") || second.Session != first.Session {
		t.Fatalf("sessions = %q then %q, want one adapter- name kept across the reconnect", first.Session, second.Session)
	}
	if first.Cwd != client || second.Cwd != client {
		t.Fatalf("directories = %q then %q, want %q both times", first.Cwd, second.Cwd, client)
	}
	_ = toAdapterWriter.Close()
	if err := <-adapterDone; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

// A client started at the top of a repository reads it without naming it;
// an explicit root or workspace_id names another and wins; a mutation is
// never given the default; and a refusal at validation happens before any
// resolution.
func TestClientDirectoryDefaultsOnlyReadsAndExplicitNamesWin(t *testing.T) {
	useStubProvider(t, newStubProvider())
	base := t.TempDir()
	repository := routingRepository(t, filepath.Join(base, "repo"), map[string]string{"notes.txt": "needle here\n"})
	other := routingRepository(t, filepath.Join(base, "other"), map[string]string{"notes.txt": "needle there\n"})
	socketPath := shortSocketPath(t)
	_, stop := startTestHuyangService(t, filepath.Join(base, "state"), socketPath, "")
	defer stop()
	session, closeSession := connectUnixClientWith(t, socketPath, controlHello{Profile: string(mcpapi.ProfileEdit), Session: "routing", Cwd: repository})
	defer closeSession()

	searched := callModern(t, session, "search", map[string]any{"query": "needle"})
	if searched["outcome"] != "ok" || !routedByDefault(searched, repository) {
		t.Fatalf("unnamed search = %#v", searched)
	}
	defaultID := replyWorkspaceID(t, searched)
	read := callModern(t, session, "read", map[string]any{"target": map[string]any{"path": "notes.txt"}})
	if replyContent(read) != "needle here\n" || replyWorkspaceID(t, read) != defaultID || !routedByDefault(read, repository) {
		t.Fatalf("unnamed read = %#v", read)
	}

	byRoot := callModern(t, session, "read", map[string]any{"root": other, "target": map[string]any{"path": "notes.txt"}})
	if replyContent(byRoot) != "needle there\n" || routedByDefault(byRoot, repository) {
		t.Fatalf("explicit root lost to the client directory: %#v", byRoot)
	}
	otherID := replyWorkspaceID(t, byRoot)
	byID := callModern(t, session, "read", map[string]any{"workspace_id": otherID, "target": map[string]any{"path": "notes.txt"}})
	if replyContent(byID) != "needle there\n" || replyWorkspaceID(t, byID) != otherID || routedByDefault(byID, repository) {
		t.Fatalf("explicit workspace_id lost to the client directory: %#v", byID)
	}

	created := callModern(t, session, "edit_apply", map[string]any{
		"operation": map[string]any{"kind": "create_file", "path": "made.txt", "content": "x\n"},
	})
	next := mcpapi.AnySlice(created["next"])
	if created["code"] != "invalid_target" || len(next) == 0 || next[0].(map[string]any)["action"] != "resend_with_root" || routedByDefault(created, repository) {
		t.Fatalf("an unnamed edit was not refused with its repair: %#v", created)
	}
	if _, err := os.Lstat(filepath.Join(repository, "made.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an edit that named no workspace wrote into the client's repository: %v", err)
	}

	invalid := callModern(t, session, "search", map[string]any{"query": "needle", "bogus": true})
	if invalid["code"] != "invalid_arguments" || routedByDefault(invalid, repository) {
		t.Fatalf("a refused call was resolved first: %#v", invalid)
	}
}

// The replay key is built after the root is adopted into a workspace_id, so
// a retry that spells the same workspace another way replays the first call
// rather than applying it twice, and a different call under the key is
// still a conflict.
func TestRelativeRootRetriesReplayWhicheverWayTheWorkspaceIsNamed(t *testing.T) {
	useStubProvider(t, newStubProvider())
	base := t.TempDir()
	repository := routingRepository(t, filepath.Join(base, "repo"), map[string]string{"notes.txt": "needle here\n"})
	socketPath := shortSocketPath(t)
	_, stop := startTestHuyangService(t, filepath.Join(base, "state"), socketPath, "")
	defer stop()
	session, closeSession := connectUnixClientWith(t, socketPath, controlHello{Profile: string(mcpapi.ProfileEdit), Session: "replay", Cwd: repository})
	defer closeSession()

	edit := func(name map[string]any, replacement string) map[string]any {
		arguments := map[string]any{
			"idempotency_key": "routing-replay",
			"operation":       map[string]any{"kind": "replace_literal", "path": "notes.txt", "old": "needle here", "new": replacement},
		}
		for key, value := range name {
			arguments[key] = value
		}
		return callModern(t, session, "edit_apply", arguments)
	}
	first := edit(map[string]any{"root": "."}, "thread here")
	if first["outcome"] == "failed" || first["outcome"] == "conflict" || first["idempotency"] != "created" {
		t.Fatalf("edit with a relative root = %#v", first)
	}
	if !strings.Contains(fmt.Sprint(first["warnings"]), `root "." was resolved against your working directory to `+repository) {
		t.Fatalf("relative root resolution was not reported: %#v", first["warnings"])
	}
	workspaceID := replyWorkspaceID(t, first)
	for label, name := range map[string]map[string]any{
		"absolute root": {"root": repository},
		"workspace_id":  {"workspace_id": workspaceID},
		"relative root": {"root": "."},
	} {
		if retried := edit(name, "thread here"); retried["idempotency"] != "replayed" {
			t.Fatalf("retry naming the workspace by %s = %#v", label, retried)
		}
	}
	reused := edit(map[string]any{"root": "."}, "different")
	if reused["code"] != "idempotency_key_reused" {
		t.Fatalf("a different edit under the same key = %#v", reused)
	}
	if content, _ := os.ReadFile(filepath.Join(repository, "notes.txt")); string(content) != "thread here\n" {
		t.Fatalf("notes.txt = %q, want the edit applied exactly once", content)
	}
}

// A linked worktree is a repository of its own: its .git is a file, and a
// client started in it reads the worktree, not the checkout it was added
// from. A client in a subdirectory of it gets no default.
func TestClientDirectoryInALinkedWorktreeDefaultsToTheWorktree(t *testing.T) {
	useStubProvider(t, newStubProvider())
	base := t.TempDir()
	primary := routingRepository(t, filepath.Join(base, "main"), map[string]string{"a.txt": "main copy\n"})
	worktree := filepath.Join(base, "worktree")
	if err := os.MkdirAll(filepath.Join(worktree, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(primary, ".git", "worktrees", "worktree")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("worktree copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := shortSocketPath(t)
	_, stop := startTestHuyangService(t, filepath.Join(base, "state"), socketPath, "")
	defer stop()

	session, closeSession := connectUnixClientWith(t, socketPath, controlHello{Profile: string(mcpapi.ProfileEdit), Session: "worktree", Cwd: worktree})
	read := callModern(t, session, "read", map[string]any{"target": map[string]any{"path": "a.txt"}})
	closeSession()
	if replyContent(read) != "worktree copy\n" || !routedByDefault(read, worktree) {
		t.Fatalf("read from a linked worktree = %#v", read)
	}

	nested, closeNested := connectUnixClientWith(t, socketPath, controlHello{Profile: string(mcpapi.ProfileEdit), Session: "worktree-sub", Cwd: filepath.Join(worktree, "sub")})
	defer closeNested()
	ambiguous := callModern(t, nested, "read", map[string]any{"target": map[string]any{"path": "a.txt"}})
	if ambiguous["code"] != "invalid_target" || routedByDefault(ambiguous, worktree) {
		t.Fatalf("a client in a subdirectory was given a default: %#v", ambiguous)
	}
}

// The directory travels with every connection the adapter makes, so the
// default survives the service being restarted under a running client.
func TestClientDirectorySurvivesAServiceRestartThroughTheAdapter(t *testing.T) {
	useStubProvider(t, newStubProvider())
	base := t.TempDir()
	repository := routingRepository(t, filepath.Join(base, "repo"), map[string]string{"notes.txt": "needle here\n"})
	useAdapterDirectory(t, repository)
	stateDir, socketPath := filepath.Join(base, "state"), shortSocketPath(t)
	_, stopFirst := startTestHuyangService(t, stateDir, socketPath, "")

	toAdapterReader, toAdapterWriter := io.Pipe()
	fromAdapterReader, fromAdapterWriter := io.Pipe()
	adapterDone := make(chan error, 1)
	go func() {
		adapterDone <- runHuyang([]string{"mcp", "--socket", socketPath}, toAdapterReader, fromAdapterWriter, io.Discard)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-routing-restart", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.IOTransport{Reader: fromAdapterReader, Writer: toAdapterWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stopSecond func()
	defer func() {
		_ = session.Close()
		_ = toAdapterWriter.Close()
		_ = fromAdapterReader.Close()
		if err := <-adapterDone; err != nil && !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("adapter: %v", err)
		}
		if stopSecond != nil {
			stopSecond()
		}
	}()
	before := callModern(t, session, "search", map[string]any{"query": "needle"})
	if before["outcome"] != "ok" || !routedByDefault(before, repository) {
		t.Fatalf("unnamed search through the adapter = %#v", before)
	}

	stopFirst()
	_, stopSecond = startTestHuyangService(t, stateDir, socketPath, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"query": "needle"}})
	if err != nil {
		t.Fatalf("search after the service restarted: %v", err)
	}
	if after := structuredMap(t, result); after["outcome"] != "ok" || !routedByDefault(after, repository) {
		t.Fatalf("unnamed search after the adapter reconnected = %#v", after)
	}
}

// An HTTP client announces no directory, so it never gets a default,
// whatever directory the service process itself runs in.
func TestHTTPClientsGetNoDefaultWorkspace(t *testing.T) {
	useStubProvider(t, newStubProvider())
	base := t.TempDir()
	service, stop := startTestHuyangService(t, filepath.Join(base, "state"), shortSocketPath(t), "127.0.0.1:0")
	defer stop()
	httpClient := &http.Client{Transport: bearerTransport{token: service.httpToken, base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-routing-http", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: "http://" + service.httpListener.Addr().String() + "/mcp/edit", HTTPClient: httpClient, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	searched := callModern(t, session, "search", map[string]any{"query": "needle"})
	if searched["outcome"] != "failed" || strings.Contains(fmt.Sprint(searched["warnings"]), "your client was started in") {
		t.Fatalf("an HTTP call that named no workspace = %#v", searched)
	}
}
