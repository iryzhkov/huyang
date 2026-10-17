package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Spool readers are shared by opt-in tests. Ordinary service tests are disabled
// by TestMain, which also redirects their output away from the real spool.
func readFrictionEvents(t *testing.T, dir string) []frictionEvent {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, frictionSource, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one spool file: %v, %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var events []frictionEvent
	scan := bufio.NewScanner(bytes.NewReader(data))
	for scan.Scan() {
		var event frictionEvent
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func singleFrictionToolEvent(t *testing.T, dir, tool string) frictionEvent {
	t.Helper()
	var matched []frictionEvent
	for _, event := range readFrictionEvents(t, dir) {
		if event.Tool == tool {
			matched = append(matched, event)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("expected one %s event, got %d", tool, len(matched))
	}
	return matched[0]
}

func TestFrictionPartialResultState(t *testing.T) {
	failure := map[string]any{"path": "/private/missing.go", "code": "document_not_found", "error": "secret"}
	for _, test := range []struct {
		name, tool string
		data       map[string]any
		codes      map[string]int
		hasResults bool
	}{
		{"mixed batch", "read", map[string]any{
			"files":   []map[string]any{{"path": "/private/ok.go", "content": "secret"}, failure, failure},
			"entries": []map[string]any{failure, failure},
		}, map[string]int{"document_not_found": 2}, true},
		{"all failed", "read", map[string]any{"files": []any{failure}}, map[string]int{"document_not_found": 1}, false},
		{"malformed batch entries", "read", map[string]any{"files": []any{nil, "secret", map[string]any{"error": "secret"}, map[string]any{"path": "/private/metadata-only.go"}}}, nil, false},
		{"symbols", "symbol_find", map[string]any{"ranked_handles": []map[string]any{{"source": "secret"}}}, nil, true},
		{"decoded symbols", "symbol_find", map[string]any{"ranked_handles": []any{map[string]any{"source": "secret"}}}, nil, true},
		{"empty symbols", "symbol_find", map[string]any{"ranked_handles": []map[string]any{}, "coverage": map[string]any{"complete": false}}, nil, false},
		{"search hits", "search", map[string]any{"hits": []any{map[string]any{"text": "secret"}}}, nil, true},
		{"undelivered count", "search", map[string]any{"total": 2, "hits": []any{}}, nil, false},
		{"recovery only", "symbol_find", map[string]any{"candidates": []any{"secret"}, "next": []any{"secret"}}, nil, false},
		{"diff records", "revision_diff", map[string]any{"diffs": []any{map[string]any{"diff": "secret"}}}, nil, true},
		{"diff gaps only", "revision_diff", map[string]any{"gaps": []any{"secret"}}, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			codes, results := frictionResultState(test.tool, "partial", test.data)
			if !reflect.DeepEqual(codes, test.codes) || results == nil || *results != test.hasResults {
				t.Fatalf("state = %v %v, want %v %t", codes, results, test.codes, test.hasResults)
			}
			_, results = frictionResultState(test.tool, "ok", test.data)
			if results != nil {
				t.Fatal("non-partial replies should not carry has_results")
			}
		})
	}
}

func TestFrictionBatchReplySpoolsCountsAndResultsWithoutContent(t *testing.T) {
	spool, root := t.TempDir(), t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "1")
	const secret = "private-source-secret"
	if err := os.WriteFile(filepath.Join(root, "ok.go"), []byte("package sample // "+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileFull, newDirectWorkspaces(t.TempDir()))
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{"root": root})
	id := opened["workspace"].(map[string]any)["id"]
	for _, targets := range [][]map[string]any{
		{{"path": "ok.go"}, {"path": "missing-a.go"}, {"path": "missing-b.go"}},
		{{"path": "missing-a.go"}, {"path": "missing-b.go"}},
	} {
		reply := callModern(t, session, "read", map[string]any{"workspace_id": id, "targets": targets})
		if reply["outcome"] != "partial" {
			t.Fatalf("expected partial read: %v", reply["outcome"])
		}
	}
	events := readFrictionEvents(t, spool)
	var reads []frictionEvent
	for _, event := range events {
		if event.Tool == "read" {
			reads = append(reads, event)
		}
	}
	if len(reads) != 2 {
		t.Fatalf("reads = %d", len(reads))
	}
	for i, event := range reads {
		if event.OK || event.Outcome != "partial" || event.HasResults == nil || *event.HasResults != (i == 0) {
			t.Fatalf("partial classification %d: %+v", i, event)
		}
		if !reflect.DeepEqual(event.SubCodes, map[string]int{"document_not_found": 2}) {
			t.Fatalf("sub_codes = %v", event.SubCodes)
		}
	}
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, secret, "missing-a.go", "missing-b.go", "ok.go"} {
		if bytes.Contains(data, []byte(private)) {
			t.Fatalf("spool contains private content %q", private)
		}
	}
}

func TestFrictionCapabilitiesOncePerConnection(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "1")
	// Reuse both the server and announced adapter session: each new connection
	// still needs its own record, independent of tool, profile, or process.
	server := newSDKServer(mcpapi.ProfileOrient, newDirectWorkspaces(t.TempDir()), "same-adapter")
	capabilities := &mcp.ClientCapabilities{
		RootsV2:      &mcp.RootCapabilities{},
		Sampling:     &mcp.SamplingCapabilities{},
		Elicitation:  &mcp.ElicitationCapabilities{},
		Experimental: map[string]any{"private-capability": map[string]any{"token": "private-token"}},
		Extensions:   map[string]any{"private/extension": map[string]any{"directory": "/private/workspace"}},
	}
	for i := 0; i < 2; i++ {
		serverTransport, clientTransport := mcp.NewInMemoryTransports()
		serverSession, err := server.Connect(context.Background(), serverTransport, nil)
		if err != nil {
			t.Fatal(err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "capability-test", Version: "1"}, &mcp.ClientOptions{Capabilities: capabilities})
		clientSession, err := client.Connect(context.Background(), clientTransport, nil)
		if err != nil {
			serverSession.Close()
			t.Fatal(err)
		}
		listedToolNames(t, clientSession)
		listedToolNames(t, clientSession)
		if i == 0 {
			// A rejected first call must not suppress or repeat capabilities.
			_, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "workspace_open", Arguments: map[string]any{"private_arg": "private-argument-value"},
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		clientSession.Close()
		serverSession.Close()
	}
	events := readFrictionEvents(t, spool)
	count := 0
	for _, event := range events {
		if event.Tool != "initialize" {
			if event.ClientCapabilities != nil {
				t.Fatal("tool event repeated capabilities")
			}
			continue
		}
		count++
		want := []string{"elicitation", "experimental", "extensions", "roots", "sampling"}
		if event.Session != "same-adapter" || event.Client != "capability-test/1" ||
			event.ClientCapabilities == nil || !reflect.DeepEqual(*event.ClientCapabilities, want) {
			t.Fatalf("initialize event = %+v", event)
		}
		if len(event.Args) != 0 || event.Root != "" || event.Rev != "" {
			t.Fatalf("initialize event has call or workspace details: %+v", event)
		}
	}
	if count != 2 {
		t.Fatalf("initialize events = %d, want two connections", count)
	}
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-capability", "private-token", "private/extension", "/private/workspace", "private-argument-value"} {
		if bytes.Contains(data, []byte(private)) {
			t.Fatalf("spool leaked %q", private)
		}
	}
}

func TestFrictionLegacyCapabilitiesOncePerConnection(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "1")
	server := newSDKServer(mcpapi.ProfileOrient, newDirectWorkspaces(t.TempDir()), "")
	// Exercise the SDK's supported fallback to the legacy initialize handshake.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "server/discover" {
				return nil, errors.New("legacy test server")
			}
			return next(ctx, method, request)
		}
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "legacy-test"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{RootsV2: &mcp.RootCapabilities{}},
	})
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.InitializeResult().ProtocolVersion != "2025-11-25" {
		t.Fatalf("did not exercise legacy initialize: %s", session.InitializeResult().ProtocolVersion)
	}
	listedToolNames(t, session)
	listedToolNames(t, session)
	event := singleFrictionToolEvent(t, spool, "initialize")
	if event.ClientCapabilities == nil || !reflect.DeepEqual(*event.ClientCapabilities, []string{"roots"}) {
		t.Fatalf("legacy capabilities = %+v", event.ClientCapabilities)
	}
}

func TestFrictionCapabilitiesAcrossStatelessHTTPConnections(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "1")
	server := newSDKServer(mcpapi.ProfileOrient, newDirectWorkspaces(t.TempDir()), "")
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	defer httpServer.Close()
	for i := 0; i < 2; i++ {
		client := mcp.NewClient(&mcp.Implementation{Name: "http-capability-test"}, &mcp.ClientOptions{
			Capabilities: &mcp.ClientCapabilities{RootsV2: &mcp.RootCapabilities{}},
		})
		session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
		if err != nil {
			t.Fatal(err)
		}
		listedToolNames(t, session)
		listedToolNames(t, session)
		session.Close()
	}
	events := readFrictionEvents(t, spool)
	if len(events) != 2 {
		t.Fatalf("HTTP connection events = %d, want two discoveries", len(events))
	}
	for _, event := range events {
		if event.Tool != "initialize" || event.ClientCapabilities == nil ||
			!reflect.DeepEqual(*event.ClientCapabilities, []string{"roots"}) {
			t.Fatalf("HTTP capability event = %+v", event)
		}
	}
}

func TestLogFrictionPartialSymbolsHaveResultsWithoutSerializingPayload(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "1")
	for _, handles := range [][]map[string]any{
		{{"source": "private-source", "unserializable": make(chan int)}},
		{},
	} {
		logFriction("", "symbol_find", "", map[string]any{"query": "private-query"}, map[string]any{
			"outcome": "partial", "code": "semantic_coverage_partial",
			"data": map[string]any{"ranked_handles": handles, "coverage": map[string]any{"complete": false}},
		}, time.Now())
	}
	events := readFrictionEvents(t, spool)
	if len(events) != 2 {
		t.Fatalf("symbol events = %d", len(events))
	}
	for i, event := range events {
		if event.HasResults == nil || *event.HasResults != (i == 0) || event.OK {
			t.Fatalf("symbol classification %d = %+v", i, event)
		}
	}
	data, err := json.Marshal(events)
	if err != nil || bytes.Contains(data, []byte("private-")) {
		t.Fatalf("symbol payload leaked: %s, %v", data, err)
	}
}

func TestFrictionEmptyCapabilitiesAreExplicit(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "1")
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileOrient, newDirectWorkspaces(t.TempDir()))
	defer cleanup()
	listedToolNames(t, session)
	event := singleFrictionToolEvent(t, spool, "initialize")
	if event.ClientCapabilities == nil || len(*event.ClientCapabilities) != 0 {
		t.Fatalf("empty capabilities = %+v", event.ClientCapabilities)
	}
	data, err := json.Marshal(event)
	if err != nil || !bytes.Contains(data, []byte(`"client_capabilities":[]`)) {
		t.Fatalf("empty advertisement should be []: %s, %v", data, err)
	}
}

func TestWorkspaceRevSkipsRelativeRoots(t *testing.T) {
	// Install a job-local fake git: even if the daemon cwd is a Git tree,
	// relative roots must not spawn git or populate its revision cache.
	bin, calls := t.TempDir(), filepath.Join(t.TempDir(), "git-calls")
	script := "#!/bin/sh\nprintf called >> \"$HUYANG_TEST_GIT_CALLS\"\nprintf fake-revision\\n\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUYANG_TEST_GIT_CALLS", calls)
	for _, root := range []string{"", ".", "..", "relative-project", "./relative-project"} {
		if got := workspaceRev(root); got != "" {
			t.Fatalf("workspaceRev(%q) = %q", root, got)
		}
		revMu.Lock()
		_, cached := revCache[root]
		revMu.Unlock()
		if cached {
			t.Fatalf("relative root %q was cached", root)
		}
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("git ran for a relative root: %v", err)
	}
	if got := workspaceRev(t.TempDir()); !strings.HasPrefix(got, "fake-revision") {
		t.Fatalf("absolute root did not run git: %q", got)
	}
}

func TestFrictionTestMainDisablesAmbientOptIn(t *testing.T) {
	// Re-exec this test binary with an enabled developer-like spool and all
	// ambient flags set. TestMain must override them before test helpers run.
	if os.Getenv("HUYANG_TEST_AMBIENT_FRICTION_CHILD") == "1" {
		if frictionEnabled() {
			t.Fatal("ambient opt-in survived TestMain")
		}
		if frictionDir() == os.Getenv("HUYANG_TEST_REAL_SPOOL") {
			t.Fatal("test suite kept the developer spool")
		}
		session, cleanup := connectOfficialClient(t, mcpapi.ProfileOrient, newDirectWorkspaces(t.TempDir()))
		defer cleanup()
		listedToolNames(t, session)
		logFriction("", "test", "", nil, textResult("ok", false), time.Now())
		return
	}
	spool := t.TempDir()
	if err := os.WriteFile(filepath.Join(spool, "enabled"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestFrictionTestMainDisablesAmbientOptIn$")
	command.Env = append(os.Environ(),
		"HUYANG_TEST_AMBIENT_FRICTION_CHILD=1", "HUYANG_TEST_REAL_SPOOL="+spool,
		"HUYANG_FRICTION_DIR="+spool, "HUYANG_FRICTION=1", "AGENT99_FRICTION=1", "TOOLFEEDBACK=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("ambient opt-in child: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 1 || entries[0].Name() != "enabled" {
		t.Fatalf("developer spool was touched: %v, %v", entries, err)
	}
}
