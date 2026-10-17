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

// newSDKServer serves one client connection. client is what that
// connection announced: the agent session every call it makes is spooled
// under, and the directory its relative roots are resolved against; style
// is how its replies are presented.
func newSDKServer(profile mcpapi.Profile, style mcpapi.ReplyStyle, direct *directWorkspaces, client clientConnection) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name: "huyang", Title: "Huyang", Version: mcpapi.ServerVersion,
		Description: "Transactional semantic workspace for coding agents.",
	}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}})
	server.AddReceivingMiddleware(frictionCapabilitiesMiddleware(client.session))
	for _, descriptor := range mcpapi.Catalog(profile) {
		registerModernTool(server, descriptor, style, direct, client)
	}
	return server
}

func registerModernTool(server *mcp.Server, descriptor mcpapi.ToolDescriptor, style mcpapi.ReplyStyle, direct *directWorkspaces, connection clientConnection) {
	session := connection.session
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
		var aliased []string
		var correlation frictionCorrelation
		// A refused argument is answered with an envelope like any other
		// refusal. Returning the Go error let the SDK turn it into bare text
		// with no code, no next step and no request ID.
		refuse := func(root string, arguments map[string]any, err error) (*mcp.CallToolResult, error) {
			logModernValidationFriction(session, descriptor.Name, root, arguments, err, client, started, correlation)
			requestID := fmt.Sprintf("req_%d", direct.requests.Add(1))
			envelope := mcpapi.FinalizeEnvelope(descriptor.Name, mcpapi.InvalidArguments(requestID, descriptor.Name, err))
			return renderToolResponse(descriptor.Name, arguments, mcpapi.PrependWarnings(envelope, aliased...), true, style)
		}
		if err := mcpapi.ValidateToolArgumentSize(request.Params.Arguments); err != nil {
			return refuse("", map[string]any{}, err)
		}
		// Capture the original wire arguments, before decoding, aliases,
		// normalization, workspace defaults or dispatch can change them.
		correlation = correlateMCPCall(descriptor.Name, request.Params.Arguments)
		arguments, err := mcpapi.DecodeArguments(request.Params.Arguments)
		if err != nil {
			return refuse("", map[string]any{}, err)
		}
		// Decode before validating: a client whose cached schema predates
		// the server sends new arguments as text.
		arguments = mcpapi.NormalizeArguments(arguments)
		aliased = mcpapi.ApplyArgumentAliases(descriptor.Name, arguments)
		if err := mcpapi.ValidateCall(descriptor.Name, descriptor.InputSchema, arguments); err != nil {
			return refuse(modernFrictionRoot(direct, descriptor.Name, arguments), arguments, err)
		}
		aliased = append(aliased, resolveClientPaths(descriptor.Name, arguments, connection.cwd)...)
		// Refused before the call runs: a mutation must not apply and then
		// answer that its requested presentation is unavailable.
		if err := mcpapi.ReplyStyleConflict(style, arguments); err != nil {
			return refuse(modernFrictionRoot(direct, descriptor.Name, arguments), arguments, err)
		}
		callContext := handlers.WithClientIdentity(ctx, sessionIdentity(request))
		if style == mcpapi.ReplyStylePlain {
			callContext = handlers.WithPlainReplies(callContext)
		}
		envelope := direct.call(callContext, descriptor.Name, arguments)
		isError := envelope["outcome"] == "failed" || envelope["outcome"] == "conflict"
		logFriction(session, descriptor.Name, modernFrictionRoot(direct, descriptor.Name, arguments), arguments,
			map[string]any{
				"isError": isError,
				"outcome": fmt.Sprint(envelope["outcome"]),
				"code":    envelope["code"],
				"client":  client,
				"data":    envelope["data"],
				"content": []map[string]any{{"type": "text", "text": fmt.Sprint(envelope["summary"])}},
			}, started, correlation)
		// An alias that was rewritten explains the rest of the reply, so its
		// warning goes ahead of the ones the call produced.
		return renderToolResponse(descriptor.Name, arguments, mcpapi.PrependWarnings(envelope, aliased...), isError, style)
	})
}

// renderToolResponse presents a finalized envelope at the transport
// boundary. The envelope was already persisted as the receipt where the call
// has one, so neither style changes what replay or evidence read back.
func renderToolResponse(tool string, arguments, envelope map[string]any, isError bool, style mcpapi.ReplyStyle) (*mcp.CallToolResult, error) {
	if style == mcpapi.ReplyStylePlain {
		return renderPlainResponse(tool, arguments, envelope, isError)
	}
	return renderJSONResponse(tool, arguments, envelope, isError)
}

// renderPlainResponse sends one text block and no structuredContent. It
// renders the full envelope: response_mode=compact never removes findings
// from a plain reply. An envelope the renderer cannot normalize is sent as
// the JSON text instead, still without the structured copy, rather than
// failing a call that may already have changed the workspace.
func renderPlainResponse(tool string, arguments, envelope map[string]any, isError bool) (*mcp.CallToolResult, error) {
	text, err := mcpapi.RenderPlain(tool, arguments, envelope)
	if err != nil {
		encoded, renderErr := mcpapi.RenderJSON(mcpapi.CompactTextEnvelope(mcpapi.TrimWireDiffs(tool, envelope)))
		if renderErr != nil {
			return nil, renderErr
		}
		text = string(encoded)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}, nil
}

// renderJSONResponse is the JSON style: the compact envelope as text and the
// structured copy, shaped by the experimental response_format and
// response_mode arguments.
func renderJSONResponse(tool string, arguments, envelope map[string]any, isError bool) (*mcp.CallToolResult, error) {
	envelope = mcpapi.CompactReceipt(tool, arguments, envelope)
	envelope = mcpapi.TrimWireDiffs(tool, envelope)
	pretty, renderErr := mcpapi.RenderJSON(mcpapi.CompactTextEnvelope(envelope))
	if renderErr != nil {
		return nil, renderErr
	}
	result := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(pretty)}},
		StructuredContent: mcpapi.CompactStructuredEnvelope(envelope),
		IsError:           isError,
	}
	format, _ := arguments["response_format"].(string)
	if format == "" && arguments["response_mode"] == "compact" {
		format = "text"
	}
	if format == "text" {
		result.StructuredContent = nil
	}
	if format == "structured" {
		result.Content = []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%v: %v", envelope["outcome"], envelope["summary"])}}
	}
	return result, nil
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

func logModernValidationFriction(session, name, root string, arguments map[string]any, err error, client string, started time.Time, correlation frictionCorrelation) {
	logFriction(session, name, root, arguments, map[string]any{
		"isError": true, "outcome": "failed", "code": "invalid_arguments", "client": client,
		"content": []map[string]any{{"type": "text", "text": err.Error()}},
	}, started, correlation)
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
