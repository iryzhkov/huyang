package bridge

import (
	"context"
	"fmt"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newSDKServer(profile mcpProfile, direct *directWorkspaces) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name: "huyang", Title: "Huyang", Version: serverVersion,
		Description: "Transactional semantic workspace for coding agents.",
	}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}})
	for _, descriptor := range modernCatalog(profile) {
		registerModernTool(server, descriptor, direct)
	}
	return server
}

func registerModernTool(server *mcp.Server, descriptor modernTool, direct *directWorkspaces) {
	server.AddTool(&mcp.Tool{
		Name: descriptor.Name, Description: descriptor.Description,
		InputSchema: descriptor.InputSchema, OutputSchema: outputEnvelopeSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    descriptor.ReadOnly,
			DestructiveHint: boolPointer(descriptor.Destructive),
			IdempotentHint:  descriptor.Idempotent,
			OpenWorldHint:   boolPointer(false),
		},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		started := time.Now()
		client := modernClientName(request)
		if err := validateToolArgumentSize(request.Params.Arguments); err != nil {
			logModernValidationFriction(descriptor.Name, "", map[string]any{}, err, client, started)
			return nil, err
		}
		arguments, err := decodeArguments(request.Params.Arguments)
		if err != nil {
			logModernValidationFriction(descriptor.Name, "", map[string]any{}, err, client, started)
			return nil, err
		}
		if err := validateToolArguments(descriptor.InputSchema, arguments); err != nil {
			logModernValidationFriction(descriptor.Name, modernFrictionRoot(direct, descriptor.Name, arguments), arguments, err, client, started)
			return nil, err
		}
		if err := validateModernDebugArguments(descriptor.Name, arguments); err != nil {
			logModernValidationFriction(descriptor.Name, modernFrictionRoot(direct, descriptor.Name, arguments), arguments, err, client, started)
			return nil, err
		}
		envelope := direct.call(withClientIdentity(ctx, sessionIdentity(request)), descriptor.Name, arguments)
		isError := envelope["outcome"] == "failed" || envelope["outcome"] == "conflict"
		logFriction(descriptor.Name, modernFrictionRoot(direct, descriptor.Name, arguments), arguments,
			map[string]any{
				"isError": isError,
				"outcome": fmt.Sprint(envelope["outcome"]),
				"client":  client,
				"content": []map[string]any{{"type": "text", "text": fmt.Sprint(envelope["summary"])}},
			}, started)
		pretty, renderErr := renderJSON(compactTextEnvelope(envelope))
		if renderErr != nil {
			return nil, renderErr
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(pretty)}},
			StructuredContent: compactStructuredEnvelope(envelope),
			IsError:           isError,
		}, nil
	})
}

// sessionIdentity keys per-client delivery state by the MCP session. Stateless
// HTTP transports produce a fresh session per request and therefore receive
// every pending notice again; the Unix proxy keeps one session per adapter.
func sessionIdentity(request *mcp.CallToolRequest) string {
	if request == nil || request.Session == nil {
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
