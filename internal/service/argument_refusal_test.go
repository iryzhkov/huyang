package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A schema violation is answered with the envelope every other refusal has:
// a code, a request ID, the argument that was wrong and the shape to send.
// It used to be returned as a Go error, which the SDK turned into bare text
// the agent could not act on.
func TestSchemaViolationsAreAnsweredWithAnEnvelope(t *testing.T) {
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileOrient, newDirectWorkspaces(t.TempDir()))
	defer cleanup()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "search", Arguments: map[string]any{
		"workspace_id": "ws_test", "query": "needle", "mode": "fuzzy",
	}})
	if err != nil {
		t.Fatalf("a schema violation was returned as a transport error: %v", err)
	}
	if !result.IsError {
		t.Fatal("a refused call is not marked as an error")
	}
	envelope := structuredMap(t, result)
	if envelope["outcome"] != "failed" || envelope["code"] != "invalid_arguments" || envelope["retryable"] != false {
		t.Fatalf("refusal envelope = %#v", envelope)
	}
	if id, _ := envelope["request_id"].(string); !strings.HasPrefix(id, "req_") {
		t.Fatalf("refusal has no request ID: %#v", envelope)
	}
	if argument := envelope["data"].(map[string]any)["argument"]; argument != "mode" {
		t.Fatalf("refused argument = %#v, want mode", argument)
	}
	next := envelope["next"].([]any)
	step, _ := next[0].(map[string]any)
	if step["tool"] != "search" || !strings.Contains(step["expected"].(string), "literal") {
		t.Fatalf("next does not restate the expected shape: %#v", next)
	}
	var text map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &text); err != nil || text["code"] != "invalid_arguments" || text["retryable"] != false {
		t.Fatalf("text content is not the envelope: %v %v", err, result.Content[0])
	}
}

// A missing required property is named as the argument to supply.
func TestAMissingRequiredPropertyIsNamedAsTheArgument(t *testing.T) {
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileOrient, newDirectWorkspaces(t.TempDir()))
	defer cleanup()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "symbol_find", Arguments: map[string]any{
		"workspace_id": "ws_test",
	}})
	if err != nil {
		t.Fatalf("a missing property was returned as a transport error: %v", err)
	}
	envelope := structuredMap(t, result)
	if envelope["code"] != "invalid_arguments" || envelope["data"].(map[string]any)["argument"] != "query" {
		t.Fatalf("missing required property = %#v", envelope)
	}
	if !strings.Contains(envelope["summary"].(string), `missing required property "query"`) {
		t.Fatalf("summary = %v", envelope["summary"])
	}
}
