package bridge

import (
	"context"
	"encoding/json"
	"errors"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

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

func connectUnixOfficialClient(t *testing.T, socketPath string, profile mcpProfile) (*mcp.ClientSession, func()) {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(connection).Encode(controlHello{Profile: string(profile)}); err != nil {
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
	firstSession, closeFirst := connectUnixOfficialClient(t, socketPath, profileEdit)
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
	if applied["outcome"] != "provisional" || applied["idempotency_persisted"] != true {
		t.Fatalf("apply = %#v", applied)
	}
	closeFirst()

	reconnected, closeReconnected := connectUnixOfficialClient(t, socketPath, profileOrient)
	read := callModern(t, reconnected, "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       displayRangeTarget(document),
	})
	if content := read["data"].(map[string]any)["content"]; content != "alpha gamma\n" {
		t.Fatalf("reconnected read = %#v", read)
	}
	closeReconnected()
	if err := firstService.direct.syncProviderEpoch(workspaceIDFromString(workspaceID), 3); err != nil {
		t.Fatal(err)
	}
	stopFirst()

	secondService, stopSecond := startTestHuyangService(t, stateDir, socketPath, "")
	defer stopSecond()
	secondSession, closeSecond := connectUnixOfficialClient(t, socketPath, profileEdit)
	defer closeSecond()
	replayed := callModern(t, secondSession, "edit_apply", applyArguments)
	if replayed["outcome"] != "provisional" || replayed["idempotency"] != "replayed" {
		t.Fatalf("restart replay = %#v", replayed)
	}
	restartedRead := callModern(t, secondSession, "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       displayRangeTarget(document),
	})
	identity := restartedRead["workspace"].(map[string]any)
	if identity["epoch"] != float64(3) || identity["state_seq"] != float64(3) {
		t.Fatalf("restored identity = %#v", identity)
	}
	if content := restartedRead["data"].(map[string]any)["content"]; content != "alpha gamma\n" {
		t.Fatalf("restart read = %#v", restartedRead)
	}
	if secondService.direct.loadErr != nil {
		t.Fatal(secondService.direct.loadErr)
	}
}

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
	if len(listed.Tools) != 17 {
		t.Fatalf("default adapter catalog = %d tools, want 17", len(listed.Tools))
	}
	_ = session.Close()
	_ = toAdapterWriter.Close()
	_ = fromAdapterReader.Close()
	if err := <-adapterDone; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
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

	for route, expected := range map[string]int{"/mcp": 17, "/mcp/orient": 8, "/mcp/edit": 13, "/mcp/debug": 12} {
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
