package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iryzhkov/huyang/internal/handlers"
	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newSDKServer(profile mcpapi.Profile, direct *directWorkspaces) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name: "huyang", Title: "Huyang", Version: mcpapi.ServerVersion,
		Description: "Transactional semantic workspace for coding agents.",
	}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}})
	for _, descriptor := range mcpapi.Catalog(profile) {
		registerModernTool(server, descriptor, direct)
	}
	return server
}

func registerModernTool(server *mcp.Server, descriptor mcpapi.ToolDescriptor, direct *directWorkspaces) {
	server.AddTool(&mcp.Tool{
		Name: descriptor.Name, Description: descriptor.Description,
		// The output envelope schema is the same for every tool and is not
		// attached: it was a quarter of the catalog every session loads.
		InputSchema: descriptor.InputSchema,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    descriptor.ReadOnly,
			DestructiveHint: boolPointer(descriptor.Destructive),
			IdempotentHint:  descriptor.Idempotent,
			OpenWorldHint:   boolPointer(false),
		},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		started := time.Now()
		client := modernClientName(request)
		if err := mcpapi.ValidateToolArgumentSize(request.Params.Arguments); err != nil {
			logModernValidationFriction(descriptor.Name, "", map[string]any{}, err, client, started)
			return nil, err
		}
		arguments, err := mcpapi.DecodeArguments(request.Params.Arguments)
		if err != nil {
			logModernValidationFriction(descriptor.Name, "", map[string]any{}, err, client, started)
			return nil, err
		}
		if err := mcpapi.ValidateToolArguments(descriptor.InputSchema, arguments); err != nil {
			logModernValidationFriction(descriptor.Name, modernFrictionRoot(direct, descriptor.Name, arguments), arguments, err, client, started)
			return nil, err
		}
		if err := mcpapi.ValidateDebugArguments(descriptor.Name, arguments); err != nil {
			logModernValidationFriction(descriptor.Name, modernFrictionRoot(direct, descriptor.Name, arguments), arguments, err, client, started)
			return nil, err
		}
		envelope := direct.call(handlers.WithClientIdentity(ctx, sessionIdentity(request)), descriptor.Name, arguments)
		isError := envelope["outcome"] == "failed" || envelope["outcome"] == "conflict"
		logFriction(descriptor.Name, modernFrictionRoot(direct, descriptor.Name, arguments), arguments,
			map[string]any{
				"isError": isError,
				"outcome": fmt.Sprint(envelope["outcome"]),
				"client":  client,
				"content": []map[string]any{{"type": "text", "text": fmt.Sprint(envelope["summary"])}},
			}, started)
		pretty, renderErr := mcpapi.RenderJSON(mcpapi.CompactTextEnvelope(envelope))
		if renderErr != nil {
			return nil, renderErr
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(pretty)}},
			StructuredContent: mcpapi.CompactStructuredEnvelope(envelope),
			IsError:           isError,
		}, nil
	})
}

// clientIdentityHeader names the HTTP client for diagnostic_updates. The
// Streamable HTTP transport is stateless, so without it every request is a
// new session and receives every pending notice again.
const clientIdentityHeader = "X-Huyang-Client"

// sessionIdentity keys per-client delivery state. An HTTP request that
// carries X-Huyang-Client is keyed by that value so diagnostic_updates is a
// delta across its requests; otherwise the MCP session is the key, which
// the Unix proxy keeps for the life of the adapter and a stateless HTTP
// transport renews on every request.
func sessionIdentity(request *mcp.CallToolRequest) string {
	if request == nil {
		return ""
	}
	if request.Extra != nil && request.Extra.Header != nil {
		if client := strings.TrimSpace(request.Extra.Header.Get(clientIdentityHeader)); client != "" {
			return "client:" + client
		}
	}
	if request.Session == nil {
		return ""
	}
	if id := request.Session.ID(); id != "" {
		return id
	}
	return fmt.Sprintf("session_%p", request.Session)
}

func modernClientName(request *mcp.CallToolRequest) string {
	info := request.ClientInfo()
	if info == nil {
		return ""
	}
	if info.Version != "" {
		return info.Name + "/" + info.Version
	}
	return info.Name
}

func logModernValidationFriction(name, root string, arguments map[string]any, err error, client string, started time.Time) {
	logFriction(name, root, arguments, map[string]any{
		"isError": true, "outcome": "failed", "client": client,
		"content": []map[string]any{{"type": "text", "text": err.Error()}},
	}, started)
}

func modernFrictionRoot(direct *directWorkspaces, tool string, arguments map[string]any) string {
	if workspaceID, _ := arguments["workspace_id"].(string); workspaceID != "" {
		if workspace := direct.get(workspacecore.ID(workspaceID)); workspace != nil {
			return workspace.Identity().Root
		}
	}
	if tool == "workspace_open" {
		root, _ := arguments["root"].(string)
		return root
	}
	return ""
}

func boolPointer(value bool) *bool { return &value }
