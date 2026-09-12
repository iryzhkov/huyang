package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func startTestHuyangService(t *testing.T, stateDir, socketPath, httpAddress string) (*huyangService, func()) {
	t.Helper()
	service, err := newHuyangService(serviceConfig{
		SocketPath: socketPath, StateDir: stateDir, HTTPAddress: httpAddress,
		ProviderQuota: 2, ExternalJobQuota: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Serve(ctx) }()
	stop := func() {
		cancel()
		service.Close()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	}
	return service, stop
}

func connectUnixOfficialClient(t *testing.T, socketPath string, profile mcpapi.Profile) (*mcp.ClientSession, func()) {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(connection).Encode(controlHello{Profile: string(profile), Session: clientSessionID()}); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-service-test", Version: "1"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
	})
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

func TestSharedServiceSurvivesAdapterReconnectAndRestart(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	socketPath := filepath.Join(base, "control.sock")
	document := filepath.Join(base, "note.txt")
	if err := os.WriteFile(document, []byte("alpha beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	firstService, stopFirst := startTestHuyangService(t, stateDir, socketPath, "")
	firstSession, closeFirst := connectUnixOfficialClient(t, socketPath, mcpapi.ProfileEdit)
	opened := callModern(t, firstSession, "workspace_open", map[string]any{
		"kind": "documents", "files": []string{document},
	})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	reopened := callModern(t, firstSession, "workspace_open", map[string]any{
		"kind": "documents", "files": []string{document},
	})
	if reopened["workspace"].(map[string]any)["id"] != workspaceID ||
		reopened["data"].(map[string]any)["registry"].(map[string]any)["reused"] != true {
		t.Fatalf("repeated open = %#v after %#v", reopened, opened)
	}
	searched := callModern(t, firstSession, "search", map[string]any{
		"workspace_id": workspaceID, "query": "beta", "mode": "literal",
		"include_ranges": true,
	})
	hit := searched["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	applyArguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "service-apply-1",
		"operation": map[string]any{
			"kind":    "replace_range",
			"target":  map[string]any{"file_range": hit["range"]},
			"content": "gamma",
		},
	}
	applied := callModern(t, firstSession, "edit_apply", applyArguments)
	// A persisted receipt is the normal case and is not announced; a text
	// document has no semantic diagnostics, so the edit is plainly ok.
	if applied["outcome"] != "ok" || applied["idempotency_persisted"] != nil {
		t.Fatalf("apply = %#v", applied)
	}
	closeFirst()

	reconnected, closeReconnected := connectUnixOfficialClient(t, socketPath, mcpapi.ProfileOrient)
	read := callModern(t, reconnected, "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       displayRangeTarget(document),
	})
	if content := read["data"].(map[string]any)["content"]; content != "alpha gamma\n" {
		t.Fatalf("reconnected read = %#v", read)
	}
	closeReconnected()
	if err := firstService.direct.registry.syncProviderEpoch(workspaceIDFromString(workspaceID), 3); err != nil {
		t.Fatal(err)
	}
	stopFirst()

	secondService, stopSecond := startTestHuyangService(t, stateDir, socketPath, "")
	defer stopSecond()
	secondSession, closeSecond := connectUnixOfficialClient(t, socketPath, mcpapi.ProfileEdit)
	defer closeSecond()
	replayed := callModern(t, secondSession, "edit_apply", applyArguments)
	if replayed["outcome"] != "ok" || replayed["idempotency"] != "replayed" {
		t.Fatalf("restart replay = %#v", replayed)
	}
	restartedRead := callModern(t, secondSession, "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       displayRangeTarget(document),
	})
	// A read names the workspace by ID and revision only; the epoch is
	// visible through workspace_open and workspace_inspect.
	identity := restartedRead["workspace"].(map[string]any)
	if identity["id"] != workspaceID || identity["revision"] != "wsrev_3" {
		t.Fatalf("restored identity = %#v", identity)
	}
	if content := restartedRead["data"].(map[string]any)["content"]; content != "alpha gamma\n" {
		t.Fatalf("restart read = %#v", restartedRead)
	}
	if secondService.direct.loadErr != nil {
		t.Fatal(secondService.direct.loadErr)
	}
}

func TestChangePlanCreateReplayRecoversLostResponseAfterRestart(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(stateDir)
	opened := direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := string(opened["workspace"].(workspacecore.Identity).ID)
	arguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "lost-create-response",
		"action": "create", "operations": []any{map[string]any{
			"op_id": "create", "kind": "create_file", "path": "created.txt", "content": "created\n",
		}},
	}
	created := direct.call(context.Background(), "change_plan", arguments)
	planID := created["transaction"].(map[string]any)["id"]
	restarted := newDirectWorkspaces(stateDir)
	replayed := restarted.call(context.Background(), "change_plan", arguments)
	if replayed["idempotency"] != "replayed" || replayed["transaction"].(map[string]any)["id"] != planID {
		t.Fatalf("lost create response was not recoverable: %#v", replayed)
	}
}

func TestEditReceiptIsDurableBeforePostMutationDiagnostics(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// A source file: only source files go through the post-mutation
	// diagnostics this test holds open.
	path := filepath.Join(root, "note.go")
	if err := os.WriteFile(path, []byte("alpha beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The canonical resync is the first provider call after the receipt
	// checkpoint; blocking it holds the edit between the durable receipt and
	// its post-mutation diagnostics while a second service instance starts.
	backend := &resyncBlockingProvider{reached: make(chan struct{}), release: make(chan struct{})}
	useFixedProvider(t, backend)
	direct := newDirectWorkspaces(stateDir)
	defer direct.closeProviders()
	opened := direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := string(opened["workspace"].(workspacecore.Identity).ID)
	searched := direct.call(context.Background(), "search", map[string]any{
		"workspace_id": workspaceID, "query": "beta", "mode": "literal",
		"include_ranges": true,
	})
	hit := searched["data"].(map[string]any)["hits"].([]map[string]any)[0]
	encodedRange, err := json.Marshal(hit["range"])
	if err != nil {
		t.Fatal(err)
	}
	var fileRange map[string]any
	if err := json.Unmarshal(encodedRange, &fileRange); err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "restart-during-diagnostics",
		"operation": map[string]any{
			"kind": "replace_range", "target": map[string]any{"file_range": fileRange}, "content": "gamma",
		},
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	done := make(chan map[string]any, 1)
	go func() { done <- direct.call(requestContext, "edit_apply", arguments) }()
	<-backend.reached

	restarted := newDirectWorkspaces(stateDir)
	replayed := restarted.call(context.Background(), "edit_apply", arguments)
	data := replayed["data"].(map[string]any)
	if replayed["idempotency"] != "replayed" || replayed["outcome"] != "provisional" ||
		data["canonical_changed"] != false || data["original_canonical_changed"] != true ||
		data["revision"] != "wsrev_2" {
		t.Fatalf("checkpointed edit receipt = %#v", replayed)
	}
	cancelRequest()
	close(backend.release)
	<-done
}

// resyncBlockingProvider is a provider whose workspace_resync call parks
// until released, signalling reached when the call arrives.
type resyncBlockingProvider struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *resyncBlockingProvider) Descriptor() provider.Descriptor {
	return provider.Descriptor{ID: "resync-blocking", Backend: "test", Epoch: 1}
}
func (p *resyncBlockingProvider) Health(context.Context) provider.Health {
	return provider.Health{State: provider.HealthHealthy, Epoch: 1}
}
func (p *resyncBlockingProvider) Call(ctx context.Context, request provider.Request) (provider.Result, error) {
	if request.Operation == "workspace_resync" {
		p.once.Do(func() { close(p.reached) })
		select {
		case <-p.release:
		case <-ctx.Done():
			return provider.Result{}, ctx.Err()
		}
	}
	return provider.Result{Value: map[string]any{}}, nil
}
func (p *resyncBlockingProvider) Close(context.Context) error { return nil }
func (p *resyncBlockingProvider) Done() <-chan struct{}       { return make(chan struct{}) }

func displayRangeTarget(path string) map[string]any {
	return map[string]any{"file_range": map[string]any{
		"path": path, "revision_id": "display-only", "byte_start": 0, "byte_end": 0,
		"expected_sha256": "", "before_sha256": "", "after_sha256": "", "anchor_bytes": 0,
	}}
}

func workspaceIDFromString(value string) workspacecore.ID {
	return workspacecore.ID(value)
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func TestHuyangMCPAdapterDefaultsToFullAndProxiesService(t *testing.T) {
	base := t.TempDir()
	socketPath := filepath.Join(base, "control.sock")
	_, stop := startTestHuyangService(t, filepath.Join(base, "state"), socketPath, "")
	defer stop()

	toAdapterReader, toAdapterWriter := io.Pipe()
	fromAdapterReader, fromAdapterWriter := io.Pipe()
	adapterDone := make(chan error, 1)
	go func() {
		adapterDone <- runHuyang([]string{"mcp", "--socket", socketPath}, toAdapterReader, fromAdapterWriter, io.Discard)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-adapter-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.IOTransport{
		Reader: fromAdapterReader, Writer: toAdapterWriter,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 19 {
		t.Fatalf("default adapter catalog = %d tools, want 19", len(listed.Tools))
	}
	_ = session.Close()
	_ = toAdapterWriter.Close()
	_ = fromAdapterReader.Close()
	if err := <-adapterDone; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

func TestHuyangMCPAdapterReconnectsAcrossServiceRestart(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	socketPath := filepath.Join(base, "control.sock")
	_, stopFirst := startTestHuyangService(t, stateDir, socketPath, "")

	toAdapterReader, toAdapterWriter := io.Pipe()
	fromAdapterReader, fromAdapterWriter := io.Pipe()
	adapterDone := make(chan error, 1)
	go func() {
		adapterDone <- runHuyang([]string{"mcp", "--socket", socketPath}, toAdapterReader, fromAdapterWriter, io.Discard)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-restart-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.IOTransport{
		Reader: fromAdapterReader, Writer: toAdapterWriter,
	}, nil)
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
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	stopFirst()
	_, stopSecond = startTestHuyangService(t, stateDir, socketPath, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("same adapter session after service restart: %v", err)
	}
	if len(listed.Tools) != 19 {
		t.Fatalf("reconnected adapter catalog = %d tools, want 19", len(listed.Tools))
	}

	stopSecond()
	_, stopSecond = startTestHuyangService(t, stateDir, socketPath, "")
	listed, err = session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("same adapter session after second service restart: %v", err)
	}
	if len(listed.Tools) != 19 {
		t.Fatalf("twice-reconnected adapter catalog = %d tools, want 19", len(listed.Tools))
	}
}

func TestLoopbackHTTPUsesFixedProfilesAndBearerCredential(t *testing.T) {
	base := t.TempDir()
	service, stop := startTestHuyangService(t, filepath.Join(base, "state"), filepath.Join(base, "control.sock"), "127.0.0.1:0")
	defer stop()
	endpoint := "http://" + service.httpListener.Addr().String()

	response, err := http.Post(endpoint+"/mcp", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", response.StatusCode)
	}

	for route, expected := range map[string]int{"/mcp": 19, "/mcp/orient": 8, "/mcp/edit": 13, "/mcp/debug": 12} {
		httpClient := &http.Client{Transport: bearerTransport{token: service.httpToken, base: http.DefaultTransport}}
		client := mcp.NewClient(&mcp.Implementation{Name: "huyang-http-test", Version: "1"}, nil)
		session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: endpoint + route, HTTPClient: httpClient, DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatalf("%s connect: %v", route, err)
		}
		listed, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("%s list: %v", route, err)
		}
		if len(listed.Tools) != expected {
			t.Errorf("%s tools = %d, want %d", route, len(listed.Tools), expected)
		}
		_ = session.Close()
	}
}

func TestServiceRejectsNonLoopbackHTTPAndProtectsSocketPath(t *testing.T) {
	base := t.TempDir()
	config := serviceConfig{
		SocketPath: filepath.Join(base, "control.sock"), StateDir: filepath.Join(base, "state"),
		HTTPAddress: "0.0.0.0:0", ProviderQuota: 1, ExternalJobQuota: 1,
	}
	if _, err := newHuyangService(config); err == nil {
		t.Fatal("non-loopback HTTP address was accepted")
	}
	protected := filepath.Join(base, "ordinary-file")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SocketPath = protected
	config.HTTPAddress = ""
	if _, err := newHuyangService(config); err == nil {
		t.Fatal("ordinary file at socket path was replaced")
	}
	content, err := os.ReadFile(protected)
	if err != nil || string(content) != "keep" {
		t.Fatalf("protected socket path content = %q, err = %v", content, err)
	}
}

// clientHeaderTransport adds the bearer token and, when set, the client
// identity header to every HTTP request.
type clientHeaderTransport struct {
	token, client string
	base          http.RoundTripper
}

func (t clientHeaderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	if t.client != "" {
		clone.Header.Set(clientIdentityHeader, t.client)
	}
	return t.base.RoundTrip(clone)
}

// Over the stateless HTTP transport a request that names its client with
// X-Huyang-Client receives each diagnostic notice once. A client is met at
// the current notice head, so its first request carries no backlog, and a
// request without the header is a new session that starts at the head again.
func TestHTTPClientHeaderMakesDiagnosticUpdatesADelta(t *testing.T) {
	base := t.TempDir()
	service, stop := startTestHuyangService(t, filepath.Join(base, "state"), filepath.Join(base, "control.sock"), "127.0.0.1:0")
	defer stop()
	endpoint := "http://" + service.httpListener.Addr().String() + "/mcp"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := service.direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
	identity := opened["workspace"].(workspacecore.Identity)
	workspace := service.direct.get(identity.ID)

	inspect := func(client string) map[string]any {
		t.Helper()
		httpClient := &http.Client{Transport: clientHeaderTransport{token: service.httpToken, client: client, base: http.DefaultTransport}}
		mcpClient := mcp.NewClient(&mcp.Implementation{Name: "huyang-http-test", Version: "1"}, nil)
		session, err := mcpClient.Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		return callModern(t, session, "workspace_inspect", map[string]any{"workspace_id": string(identity.ID), "view": "status"})
	}
	for _, client := range []string{"agent-a", "agent-b"} {
		if quiet := inspect(client); quiet["diagnostic_updates"] != nil {
			t.Fatalf("%s was met with a backlog: %#v", client, quiet["diagnostic_updates"])
		}
	}
	recordTestFinding(t, workspace, filepath.Join(root, "main.go"), 0)
	if first := inspect("agent-a"); first["diagnostic_updates"] == nil {
		t.Fatalf("first request with a client header saw no notices: %#v", first)
	}
	if second := inspect("agent-a"); second["diagnostic_updates"] != nil {
		t.Fatalf("second request with the same client header saw the notices again: %#v", second["diagnostic_updates"])
	}
	if other := inspect("agent-b"); other["diagnostic_updates"] == nil {
		t.Fatalf("a different client did not receive the pending notices: %#v", other)
	}
	// A request without the header is a session nobody has seen, so it is
	// met at the head like any other and told what changes from there.
	if anonymous := inspect(""); anonymous["diagnostic_updates"] != nil {
		t.Fatalf("an unnamed session was met with a backlog: %#v", anonymous["diagnostic_updates"])
	}
}
