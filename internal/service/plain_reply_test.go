package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Plain replies across the real SDK transport: one text block, no
// structuredContent, and every guard a follow-up needs readable in the text.

var (
	plainRevisionPattern  = regexp.MustCompile(`revision_id=(docrev_[0-9a-f]{64})`)
	plainResultSetPattern = regexp.MustCompile(`result_set=(set_[0-9a-f]+)`)
	plainWorkspacePattern = regexp.MustCompile(`id=(ws_[0-9a-f]+)`)
)

// callPlain makes one call and returns its text, failing the test when the
// reply is not exactly one text block without a structured copy.
func callPlain(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.StructuredContent != nil {
		t.Fatalf("%s plain reply carried structuredContent: %#v", name, result.StructuredContent)
	}
	if len(result.Content) != 1 {
		t.Fatalf("%s plain reply has %d content blocks", name, len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s plain reply is %T, want text", name, result.Content[0])
	}
	return text.Text, result.IsError
}

func firstMatch(t *testing.T, pattern *regexp.Regexp, text string) string {
	t.Helper()
	match := pattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("no %s in plain reply:\n%s", pattern, text)
	}
	return match[1]
}

// A read's revision_id, taken from the text alone, guards the overwrite that
// follows; a search shows source lines and the set handle that refines it;
// a stale guard is refused with the current one; a repeated key replays the
// original receipt without a new mutation.
func TestPlainRepliesDriveAGuardedWorkflow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("alpha beta\nalpha gamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(t.TempDir())
	defer direct.closeProviders()
	session, cleanup := connectStyledClient(t, mcpapi.ProfileFull, mcpapi.ReplyStylePlain, direct, "")
	defer cleanup()

	opened, _ := callPlain(t, session, "workspace_open", map[string]any{"root": root})
	if !strings.HasPrefix(opened, "Workspace "+root+" (id=ws_") || !strings.Contains(opened, "revision=wsrev_") {
		t.Fatalf("workspace_open = %q", opened)
	}
	read, isError := callPlain(t, session, "read", map[string]any{"root": root, "target": map[string]any{"path": "note.txt"}})
	if isError || !regexp.MustCompile(`^note\.txt \(revision_id=docrev_[0-9a-f]{64}; lines=1-2/2\)\nalpha beta\nalpha gamma\n$`).MatchString(read) {
		t.Fatalf("read = %q", read)
	}
	revision := firstMatch(t, plainRevisionPattern, read)

	searched, _ := callPlain(t, session, "search", map[string]any{"root": root, "query": "alpha"})
	if searched != "note.txt:1:alpha beta\nnote.txt:2:alpha gamma\n2 matches in 1 file; complete\n" {
		t.Fatalf("search = %q", searched)
	}
	limited, _ := callPlain(t, session, "search", map[string]any{"root": root, "query": "alpha", "limit": 1})
	if !strings.HasPrefix(limited, "note.txt:1:alpha beta\n1 of 2 matches in 1 file; complete; result_set=set_") {
		t.Fatalf("limited search = %q", limited)
	}
	set := firstMatch(t, plainResultSetPattern, limited)
	refined, _ := callPlain(t, session, "search", map[string]any{
		"root": root, "query": "alpha", "result_set_handle": set, "refine": map[string]any{"matched_text": map[string]any{"literal": "alpha"}},
	})
	if !strings.Contains(refined, "note.txt:2:alpha gamma\n") || !strings.Contains(refined, "refined from "+set) || !strings.Contains(refined, "; result_set=set_") {
		t.Fatalf("refinement of %s = %q", set, refined)
	}

	replace := map[string]any{"root": root, "idempotency_key": "plain-replace-1", "operation": map[string]any{
		"kind": "create_file", "path": "note.txt", "content": "replaced\n", "replace": true, "revision_id": revision,
	}}
	replaced, isError := callPlain(t, session, "edit_apply", replace)
	if isError || !regexp.MustCompile(`^Replaced note\.txt \(revision=wsrev_\d+; revision_id=docrev_[0-9a-f]{64}\)\n`).MatchString(replaced) {
		t.Fatalf("guarded replace = %q", replaced)
	}
	current := firstMatch(t, plainRevisionPattern, replaced)
	if current == revision {
		t.Fatal("the replace reported the old revision")
	}

	unguarded, isError := callPlain(t, session, "edit_apply", map[string]any{"root": root, "operation": map[string]any{
		"kind": "create_file", "path": "note.txt", "content": "clobbered\n",
	}})
	if !isError || !strings.Contains(unguarded, current) {
		t.Fatalf("an overwrite without a guard was not refused with the current revision_id: %q", unguarded)
	}
	stale := map[string]any{"root": root, "idempotency_key": "plain-replace-stale", "operation": map[string]any{
		"kind": "create_file", "path": "note.txt", "content": "stale\n", "replace": true, "revision_id": revision,
	}}
	if refused, isError := callPlain(t, session, "edit_apply", stale); !isError || strings.HasPrefix(refused, "Replaced") {
		t.Fatalf("a stale guard was accepted: %q", refused)
	}

	replayed, isError := callPlain(t, session, "edit_apply", replace)
	if isError || !strings.HasPrefix(replayed, "Replayed original receipt; no new mutation") ||
		!strings.Contains(replayed, "revision_id="+current+" (as of the original edit)") {
		t.Fatalf("replay = %q", replayed)
	}
	reread, _ := callPlain(t, session, "read", map[string]any{"root": root, "target": map[string]any{"path": "note.txt"}})
	if reread != "note.txt (revision_id="+current+"; lines=1-1/1)\nreplaced\n" {
		t.Fatalf("read after the guarded replace = %q", reread)
	}
}

// A finding published after a client's previous reply arrives on its next
// plain reply, located and not claimed for the call that carried it.
func TestPlainRepliesCarryLateFindings(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(t.TempDir())
	defer direct.closeProviders()
	session, cleanup := connectStyledClient(t, mcpapi.ProfileFull, mcpapi.ReplyStylePlain, direct, "")
	defer cleanup()
	opened, _ := callPlain(t, session, "workspace_open", map[string]any{"root": root})
	workspaceID := firstMatch(t, plainWorkspacePattern, opened)
	inspect := map[string]any{"workspace_id": workspaceID, "view": "status"}
	if quiet, _ := callPlain(t, session, "workspace_inspect", inspect); strings.Contains(quiet, "late finding") {
		t.Fatalf("first reply carried a backlog: %q", quiet)
	}
	workspace := direct.get(workspaceIDFromString(workspaceID))
	recordTestFinding(t, workspace, filepath.Join(workspace.Identity().Root, "main.go"), 0)
	late, _ := callPlain(t, session, "workspace_inspect", inspect)
	// The recorded finding has no pre-edit baseline, so the line says it may
	// be new or pre-existing before saying it came from the notice stream and
	// is not claimed for this call. The whole line is asserted, exactly once.
	want := "main.go:1: error: undefined: x0 (no baseline: new or pre-existing) (late finding; not attributed to this call)"
	if count := strings.Count("\n"+late, "\n"+want+"\n"); count != 1 {
		t.Fatalf("late finding line %q appears %d times in the plain reply:\n%s", want, count, late)
	}
	if again, _ := callPlain(t, session, "workspace_inspect", inspect); strings.Contains(again, "undefined: x0") {
		t.Fatalf("the same notice was delivered twice:\n%s", again)
	}
}

// A plain session refuses a per-call request for structured content before
// the call runs, and response_mode=compact never removes the receipt.
func TestPlainSessionsRefuseStructuredFormatsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	direct := newDirectWorkspaces(t.TempDir())
	defer direct.closeProviders()
	session, cleanup := connectStyledClient(t, mcpapi.ProfileExperimental, mcpapi.ReplyStylePlain, direct, "")
	defer cleanup()
	create := func(name, format string) (string, bool) {
		arguments := map[string]any{"root": root, "operation": map[string]any{"kind": "create_file", "path": name, "content": "x\n"}}
		if format != "" {
			arguments["response_format"] = format
		}
		return callPlain(t, session, "edit_apply", arguments)
	}
	refused, isError := create("structured.txt", "structured")
	if !isError || !strings.HasPrefix(refused, "invalid_arguments: response_format=structured needs a session started with --reply-style json") {
		t.Fatalf("structured format in a plain session = %q", refused)
	}
	if _, err := os.Stat(filepath.Join(root, "structured.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused call created its file: %v", err)
	}
	if text, isError := create("text.txt", "text"); isError || !strings.HasPrefix(text, "Created text.txt (revision=wsrev_") {
		t.Fatalf("response_format=text in a plain session = %q", text)
	}
	compact, isError := callPlain(t, session, "edit_apply", map[string]any{"root": root, "response_mode": "compact",
		"operation": map[string]any{"kind": "create_file", "path": "compact.txt", "content": "x\n"}})
	if isError || !strings.HasPrefix(compact, "Created compact.txt (revision=wsrev_") || strings.Contains(compact, "details_omitted") {
		t.Fatalf("compact mode in a plain session = %q", compact)
	}
}

// The Unix socket takes each adapter's style from its hello: none (an
// adapter older than the option) is plain, json keeps the structured copy,
// and an unknown style is not served.
func TestUnixHelloSelectsTheReplyStyle(t *testing.T) {
	base, err := os.MkdirTemp("", "h8-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	socketPath := filepath.Join(base, "sock")
	_, stop := startTestHuyangService(t, filepath.Join(base, "state"), socketPath, "")
	defer stop()
	root := t.TempDir()
	for style, wantStructured := range map[string]bool{"": false, "plain": false, "json": true} {
		session, cleanup := connectUnixStyledClient(t, socketPath, mcpapi.ProfileFull, style)
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "workspace_open", Arguments: map[string]any{"root": root}})
		cleanup()
		if err != nil {
			t.Fatalf("style %q: %v", style, err)
		}
		if (result.StructuredContent != nil) != wantStructured {
			t.Fatalf("style %q: structuredContent present = %v", style, result.StructuredContent != nil)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if wantStructured != strings.HasPrefix(text, "{") {
			t.Fatalf("style %q text = %q", style, text)
		}
	}
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(controlHello{Profile: "full", ReplyStyle: "bogus"}); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := bufio.NewReader(connection).ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("a hello with an unknown style was served: %v", err)
	}
}

// huyang mcp defaults to plain, forwards --reply-style json in its hello,
// and refuses an unknown style before it connects.
func TestMCPAdapterReplyStyleFlag(t *testing.T) {
	if err := runHuyang([]string{"mcp", "--reply-style", "plain-v1"}, strings.NewReader(""), io.Discard, io.Discard); err == nil ||
		!strings.Contains(err.Error(), `unknown reply style "plain-v1"`) {
		t.Fatalf("unknown style: %v", err)
	}
	base := t.TempDir()
	socketPath := filepath.Join(base, "control.sock")
	_, stop := startTestHuyangService(t, filepath.Join(base, "state"), socketPath, "")
	defer stop()
	root := t.TempDir()
	for _, test := range []struct {
		flags          []string
		wantStructured bool
	}{{nil, false}, {[]string{"--reply-style", "json"}, true}} {
		toAdapterReader, toAdapterWriter := io.Pipe()
		fromAdapterReader, fromAdapterWriter := io.Pipe()
		adapterDone := make(chan error, 1)
		arguments := append([]string{"mcp", "--socket", socketPath}, test.flags...)
		go func() { adapterDone <- runHuyang(arguments, toAdapterReader, fromAdapterWriter, io.Discard) }()
		client := mcp.NewClient(&mcp.Implementation{Name: "huyang-style-test", Version: "1"}, nil)
		session, err := client.Connect(context.Background(), &mcp.IOTransport{Reader: fromAdapterReader, Writer: toAdapterWriter}, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "workspace_open", Arguments: map[string]any{"root": root}})
		if err != nil {
			t.Fatal(err)
		}
		if (result.StructuredContent != nil) != test.wantStructured {
			t.Fatalf("flags %v: structuredContent present = %v", test.flags, result.StructuredContent != nil)
		}
		_ = session.Close()
		_ = toAdapterWriter.Close()
		_ = fromAdapterReader.Close()
		if err := <-adapterDone; err != nil && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	}
}

// The HTTP routes answer in plain text unless serve was started with
// --reply-style json.
func TestHTTPRoutesDefaultToPlainReplies(t *testing.T) {
	base := t.TempDir()
	service, err := newHuyangService(serviceConfig{
		SocketPath: filepath.Join(base, "control.sock"), StateDir: filepath.Join(base, "state"),
		HTTPAddress: "127.0.0.1:0", ProviderQuota: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Serve(ctx) }()
	defer func() {
		cancel()
		service.Close()
		<-done
	}()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: bearerTransport{token: service.httpToken, base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-http-plain", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: "http://" + service.httpListener.Addr().String() + "/mcp/lean", HTTPClient: httpClient, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	text, isError := callPlain(t, session, "read", map[string]any{"root": root, "target": map[string]any{"path": "a.txt"}})
	if isError || !regexp.MustCompile(`^a\.txt \(revision_id=docrev_[0-9a-f]{64}; lines=1-1/1\)\na\n$`).MatchString(text) {
		t.Fatalf("plain HTTP read = %q", text)
	}
}
