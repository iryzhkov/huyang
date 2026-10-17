package service

import (
	"context"
	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Compact diagnostics are notice deltas. full:true is the explicit recovery
// path for current bodies after acknowledgement, across transport reconnects.
func TestDiagnosticRecovery(t *testing.T) {
	for _, transport := range []string{"unix", "http"} {
		t.Run(transport, func(t *testing.T) {
			base := t.TempDir()
			service, stop := startTestHuyangService(t, filepath.Join(base, "state"), filepath.Join(base, "sock"), "127.0.0.1:0")
			defer stop()
			root := t.TempDir()
			path := filepath.Join(root, "main.go")
			if err := os.WriteFile(path, []byte("package main\n"), 0600); err != nil {
				t.Fatal(err)
			}
			opened := service.direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
			identity := opened["workspace"].(workspacecore.Identity)
			workspace := service.direct.get(identity.ID)
			connect := func() (*mcp.ClientSession, func()) {
				if transport == "unix" {
					return connectUnixOfficialClient(t, filepath.Join(base, "sock"), mcpapi.ProfileEdit)
				}
				client := mcp.NewClient(&mcp.Implementation{Name: "diagnostic-recovery", Version: "1"}, nil)
				session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
					Endpoint:             "http://" + service.httpListener.Addr().String() + "/mcp",
					HTTPClient:           &http.Client{Transport: clientHeaderTransport{token: service.httpToken, client: "recovery", base: http.DefaultTransport}},
					DisableStandaloneSSE: true,
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				return session, func() { _ = session.Close() }
			}
			session, closeSession := connect()
			call := func(full bool, since string) map[string]any {
				args := map[string]any{"workspace_id": string(identity.ID), "full": full}
				if since != "" {
					args["since"] = since
				}
				reply := callModern(t, session, "diagnostics", args)
				return reply["data"].(map[string]any)["diagnostics"].(map[string]any)
			}
			// Register before producing late evidence, so diagnostic_updates has a
			// real observer and reconnect is exercised after an acknowledged notice.
			call(false, "")
			recordTestFinding(t, workspace, path, 0)
			initial := call(false, "")
			cursor := initial["cursor"].(string)
			acknowledged := call(false, cursor)
			if len(acknowledged["new"].([]any)) != 0 || acknowledged["preexisting_count"] != float64(1) {
				t.Fatalf("ack did not prune notices: %+v", acknowledged)
			}
			closeSession()
			session, closeSession = connect()
			defer closeSession()
			full := call(true, "")
			current, ok := full["current"].([]any)
			if !ok || len(current) != 1 {
				t.Fatalf("current bodies lost after reconnect: %+v", full)
			}
			item := current[0].(map[string]any)
			if item["finding"].(map[string]any)["message"] != "undefined: x0" || len(probeServiceSlice(full["new"])) != 0 {
				t.Fatalf("recovery changed transition or body: %+v", full)
			}
		})
	}
}

func probeServiceSlice(value any) []any { items, _ := value.([]any); return items }
