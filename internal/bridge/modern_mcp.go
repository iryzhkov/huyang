package bridge

import (
	workspacecore "agent99/internal/workspace"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const modernAPIVersion = "huyang.workspace/v1alpha1"

type mcpProfile string

const (
	profileLegacy mcpProfile = "legacy"
	profileFull   mcpProfile = "full"
	profileOrient mcpProfile = "orient"
	profileEdit   mcpProfile = "edit"
	profileDebug  mcpProfile = "debug"
)

type modernTool struct {
	Name        string
	Description string
	Profiles    []mcpProfile
	InputSchema map[string]any
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
}

var modernProfileOrder = []mcpProfile{profileFull, profileOrient, profileEdit, profileDebug}
var modernProfileNames = map[mcpProfile][]string{
	profileFull: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics",
		"code_actions", "edit_apply", "change_plan", "verify_run", "revision_diff", "evidence_get",
		"debug_session", "debug_breakpoints", "debug_control", "debug_inspect",
	},
	profileOrient: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics", "evidence_get",
	},
	profileEdit: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics", "evidence_get",
		"code_actions", "edit_apply", "change_plan", "verify_run", "revision_diff",
	},
	profileDebug: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics", "evidence_get",
		"debug_session", "debug_breakpoints", "debug_control", "debug_inspect",
	},
}

var modernTools = buildModernTools()

func boolPointer(value bool) *bool { return &value }

func schemaObject(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func enumSchema(values ...string) map[string]any {
	items := make([]any, len(values))
	for index, value := range values {
		items[index] = value
	}
	return map[string]any{"type": "string", "enum": items}
}

func targetSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"oneOf": []any{
			schemaObject(map[string]any{"handle": stringSchema("Opaque revision-bound handle.")}, "handle"),
			schemaObject(map[string]any{"file_range": schemaObject(map[string]any{
				"path":            stringSchema("Workspace-relative or absolute file path."),
				"revision_id":     stringSchema("Expected document revision."),
				"byte_start":      map[string]any{"type": "integer", "minimum": 0},
				"byte_end":        map[string]any{"type": "integer", "minimum": 0},
				"expected_sha256": stringSchema("SHA-256 of the exact selected bytes."),
				"before_sha256":   stringSchema("SHA-256 of the bounded preceding anchor."),
				"after_sha256":    stringSchema("SHA-256 of the bounded following anchor."),
				"anchor_bytes":    map[string]any{"type": "integer", "minimum": 0},
			}, "path", "revision_id", "byte_start", "byte_end", "expected_sha256", "before_sha256", "after_sha256", "anchor_bytes")}, "file_range"),
			schemaObject(map[string]any{"symbol_locator": schemaObject(map[string]any{
				"path":      stringSchema("File path."),
				"name_path": stringSchema("Human-readable declaration name path."),
			}, "path", "name_path")}, "symbol_locator"),
		},
	}
}

func workspaceIDProperty() map[string]any {
	return stringSchema("Explicit workspace ID returned by workspace_open.")
}

func statefulProperties() map[string]any {
	return map[string]any{
		"workspace_id":    workspaceIDProperty(),
		"idempotency_key": stringSchema("Caller-generated stable key for safe retries."),
	}
}

func buildModernTools() []modernTool {
	orient := []mcpProfile{profileFull, profileOrient, profileEdit, profileDebug}
	edit := []mcpProfile{profileFull, profileEdit}
	debug := []mcpProfile{profileFull, profileDebug}
	readTarget := map[string]any{"workspace_id": workspaceIDProperty(), "target": targetSchema()}
	stateful := statefulProperties()
	operationKinds := []string{"replace_symbol", "delete_symbol", "insert_before", "insert_after", "replace_range", "create_file", "move_file", "delete_file", "rename_symbol", "move_symbols", "replace_matches", "apply_code_action"}
	return []modernTool{
		{Name: "workspace_open", Description: "Open a project or exact document allowlist and return its revision, capabilities, compact overview, and limits.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"kind":  enumSchema("project", "documents"),
			"root":  stringSchema("Project root; required for kind=project."),
			"files": map[string]any{"type": "array", "items": stringSchema("Allowlisted document."), "minItems": 1},
		}, "kind")},
		{Name: "workspace_inspect", Description: "Inspect revision, provider health, semantic coverage, pipeline availability, and limits without mutation.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "view": enumSchema("status", "overview", "map"),
		}, "workspace_id")},
		{Name: "search", Description: "Search literal or explicit-regex text in the current revision with exact ranges and honest coverage.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "query": stringSchema("Text or regular expression."), "mode": enumSchema("literal", "regex"),
		}, "workspace_id", "query")},
		{Name: "symbol_find", Description: "Find declarations and return ranked revision-bound handles when a semantic provider is available.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "query": stringSchema("Declaration name or path."), "include_source": map[string]any{"type": "boolean"},
		}, "workspace_id", "query")},
		{Name: "navigate", Description: "Navigate one semantic relationship from a shared revision-bound target.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "relation": enumSchema("definition", "type_definition", "implementation", "references", "incoming_calls", "outgoing_calls", "hover"), "target": targetSchema(),
		}, "workspace_id", "relation", "target")},
		{Name: "read", Description: "Read exact current source or an outline for a revision-bound target.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "target": targetSchema(), "view": enumSchema("source", "outline", "history", "changes"),
		}, "workspace_id", "target")},
		{Name: "diagnostics", Description: "Inspect normalized diagnostic evidence, confidence, coverage, and provenance.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "since": stringSchema("Optional diagnostic cursor."),
		}, "workspace_id")},
		{Name: "code_actions", Description: "List revision-bound quick fixes or refactors without applying them.", Profiles: edit, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(readTarget, "workspace_id", "target")},
		{Name: "edit_apply", Description: "Preview or apply exactly one guarded operation through the direct workspace core.", Profiles: edit, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
			"preview_only": map[string]any{"type": "boolean"},
			"operation": schemaObject(map[string]any{
				"kind": enumSchema(operationKinds...), "target": targetSchema(), "content": stringSchema("Exact replacement bytes as UTF-8 text."),
			}, "kind", "target"),
		}, "workspace_id", "idempotency_key", "operation")},
		{Name: "change_plan", Description: "Create, edit, preview, prepare, inspect, apply, or discard one coherent multi-operation plan.", Profiles: edit, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"], "action": enumSchema("create", "edit", "preview", "prepare", "inspect", "apply", "discard"),
		}, "workspace_id", "idempotency_key", "action")},
		{Name: "verify_run", Description: "Run selected formatting, parser, diagnostic, check, or test stages against an exact revision.", Profiles: edit, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
			"stages":   map[string]any{"type": "array", "items": enumSchema("format_gate", "parser", "diagnostics", "check", "tests"), "minItems": 1},
			"revision": stringSchema("Canonical or prepared revision."),
		}, "workspace_id", "idempotency_key", "stages", "revision")},
		{Name: "revision_diff", Description: "Explain changes between two revisions or a stale mutation refusal.", Profiles: edit, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "from_revision": stringSchema("Earlier revision."), "to_revision_or_current": stringSchema("Later revision or current."),
		}, "workspace_id", "from_revision", "to_revision_or_current")},
		{Name: "evidence_get", Description: "Page pending or final diff, diagnostic, command, or provenance evidence.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "evidence_id": stringSchema("Evidence identifier."), "cursor": stringSchema("Optional page cursor."),
		}, "workspace_id", "evidence_id")},
		{Name: "debug_session", Description: "Start, attach, restart, or stop a debugger session.", Profiles: debug, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"], "action": enumSchema("start", "attach", "restart", "stop"),
		}, "workspace_id", "idempotency_key", "action")},
		{Name: "debug_breakpoints", Description: "List, set, remove, or clear debugger breakpoints using normal source targets.", Profiles: debug, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"], "action": enumSchema("list", "set", "remove", "clear"), "target": targetSchema(),
		}, "workspace_id", "idempotency_key", "action")},
		{Name: "debug_control", Description: "Continue, pause, step, or run a debugger session to a revision-bound target.", Profiles: debug, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"], "action": enumSchema("continue", "pause", "step_over", "step_into", "step_out", "run_to"), "target": targetSchema(),
		}, "workspace_id", "idempotency_key", "action")},
		{Name: "debug_inspect", Description: "Inspect debugger threads, stacks, scopes, variables, or explicitly governed evaluation.", Profiles: debug, ReadOnly: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "action": enumSchema("threads", "stack", "scopes", "variables", "evaluate"), "policy": enumSchema("read_only", "allow_side_effects"),
		}, "workspace_id", "action")},
	}
}

func modernCatalog(profile mcpProfile) []modernTool {
	byName := make(map[string]modernTool, len(modernTools))
	for _, descriptor := range modernTools {
		byName[descriptor.Name] = descriptor
	}
	names := modernProfileNames[profile]
	catalog := make([]modernTool, 0, len(names))
	for _, name := range names {
		if descriptor, ok := byName[name]; ok {
			catalog = append(catalog, descriptor)
		}
	}
	return catalog
}

func requestedMCPProfile(arguments []string) (mcpProfile, error) {
	if len(arguments) == 0 {
		return profileLegacy, nil
	}
	if len(arguments) != 2 || arguments[0] != "--profile" {
		return "", errors.New("usage: agent99-bridge mcp [--profile full|orient|edit|debug|legacy]")
	}
	profile := mcpProfile(arguments[1])
	switch profile {
	case profileLegacy, profileFull, profileOrient, profileEdit, profileDebug:
		return profile, nil
	default:
		return "", fmt.Errorf("unknown MCP profile %q", arguments[1])
	}
}

func outputEnvelopeSchema() map[string]any {
	return schemaObject(map[string]any{
		"api_version": map[string]any{"const": modernAPIVersion},
		"request_id":  stringSchema("Server request identifier."),
		"outcome":     enumSchema("ok", "partial", "provisional", "conflict", "unavailable", "failed"),
		"code":        stringSchema("Stable application outcome code."),
		"summary":     stringSchema("Compact deterministic result summary."),
		"workspace":   map[string]any{"type": "object"},
		"data":        map[string]any{"type": "object", "additionalProperties": true},
		"evidence":    schemaObject(map[string]any{"ids": map[string]any{"type": "array", "items": stringSchema("Evidence ID.")}, "truncated": map[string]any{"type": "boolean"}}, "ids", "truncated"),
		"warnings":    map[string]any{"type": "array", "items": stringSchema("Warning.")},
		"next":        map[string]any{"type": "array", "maxItems": 2},
		"idempotency": enumSchema("created", "replayed"),
	}, "api_version", "request_id", "outcome", "summary", "data", "evidence", "warnings", "next")
}

func newSDKServer(profile mcpProfile, direct *directWorkspaces) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name: "huyang", Title: "Huyang", Version: serverVersion,
		Description: "Transactional semantic workspace for coding agents.",
	}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}})
	if profile == profileLegacy {
		advertised := map[string]bool{}
		for _, descriptor := range servedTools() {
			advertised[descriptor.Name] = true
			registerLegacyTool(server, descriptor)
		}
		server.AddReceivingMiddleware(legacyHiddenToolMiddleware(advertised))
		return server
	}
	for _, descriptor := range modernCatalog(profile) {
		registerModernTool(server, descriptor, direct)
	}
	return server
}

func registerLegacyTool(server *mcp.Server, descriptor tool) {
	server.AddTool(&mcp.Tool{
		Name: descriptor.Name, Description: descriptor.Description, InputSchema: descriptor.InputSchema,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    !writingTools[descriptor.Name],
			DestructiveHint: boolPointer(writingTools[descriptor.Name]),
			IdempotentHint:  !writingTools[descriptor.Name],
			OpenWorldHint:   boolPointer(false),
		},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments, err := decodeArguments(request.Params.Arguments)
		if err != nil {
			return nil, err
		}
		meta := map[string]any{"_meta": map[string]any(request.Params.Meta)}
		result := callMCPToolContext(ctx, descriptor.Name, arguments, callClient(metaClient(meta)))
		return legacySDKResult(result), nil
	})
}

func legacyHiddenToolMiddleware(advertised map[string]bool) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			call, ok := request.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || advertised[call.Params.Name] || !lspToolNames[call.Params.Name] {
				return next(ctx, method, request)
			}
			arguments, err := decodeArguments(call.Params.Arguments)
			if err != nil {
				return nil, err
			}
			meta := map[string]any{"_meta": map[string]any(call.Params.Meta)}
			result := callMCPToolContext(ctx, call.Params.Name, arguments, callClient(metaClient(meta)))
			return legacySDKResult(result), nil
		}
	}
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
		arguments, err := decodeArguments(request.Params.Arguments)
		if err != nil {
			return nil, err
		}
		if err := validateToolArguments(descriptor.InputSchema, arguments); err != nil {
			return nil, err
		}
		envelope := direct.call(ctx, descriptor.Name, arguments)
		pretty, renderErr := renderJSON(envelope)
		if renderErr != nil {
			return nil, renderErr
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(pretty)}},
			StructuredContent: envelope,
			IsError:           envelope["outcome"] == "failed" || envelope["outcome"] == "conflict",
		}, nil
	})
}

func validateToolArguments(schema map[string]any, arguments map[string]any) error {
	return validateSchemaValue(schema, arguments, "arguments")
}

func validateSchemaValue(schema map[string]any, value any, path string) error {
	if alternatives, ok := schema["oneOf"].([]any); ok {
		for _, candidate := range alternatives {
			candidateSchema, _ := candidate.(map[string]any)
			if candidateSchema != nil && validateSchemaValue(candidateSchema, value, path) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s does not match any allowed shape", path)
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, candidate := range enum {
			if value == candidate {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s is not one of the allowed values", path)
		}
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		if closed, _ := schema["additionalProperties"].(bool); !closed {
			for key := range object {
				if _, ok := properties[key]; !ok {
					return fmt.Errorf("%s contains unknown property %q", path, key)
				}
			}
		}
		if required, ok := schema["required"].([]string); ok {
			for _, key := range required {
				if _, present := object[key]; !present {
					return fmt.Errorf("%s is missing required property %q", path, key)
				}
			}
		}
		for key, child := range object {
			childSchema, _ := properties[key].(map[string]any)
			if childSchema != nil {
				if err := validateSchemaValue(childSchema, child, path+"."+key); err != nil {
					return err
				}
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || number != float64(int64(number)) {
			return fmt.Errorf("%s must be an integer", path)
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for index, item := range array {
			if itemSchema != nil {
				if err := validateSchemaValue(itemSchema, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func decodeArguments(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var arguments map[string]any
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	if arguments == nil {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	return arguments, nil
}

func legacySDKResult(result map[string]any) *mcp.CallToolResult {
	sdkResult := &mcp.CallToolResult{}
	if value, ok := result["isError"].(bool); ok {
		sdkResult.IsError = value
	}
	for _, item := range result["content"].([]map[string]any) {
		if item["type"] == "text" {
			sdkResult.Content = append(sdkResult.Content, &mcp.TextContent{Text: fmt.Sprint(item["text"])})
		}
	}
	return sdkResult
}

type directWorkspaces struct {
	mu       sync.RWMutex
	items    map[workspacecore.ID]*workspacecore.Workspace
	stateDir string
	requests atomic.Uint64

	replayMu sync.Mutex
	replays  map[string]directReplay
}

type directReplay struct {
	argumentsHash string
	result        map[string]any
}

func newDirectWorkspaces(stateDir string) *directWorkspaces {
	return &directWorkspaces{
		items: make(map[workspacecore.ID]*workspacecore.Workspace), stateDir: stateDir,
		replays: make(map[string]directReplay),
	}
}

func (d *directWorkspaces) call(ctx context.Context, name string, arguments map[string]any) map[string]any {
	requestID := fmt.Sprintf("req_%d", d.requests.Add(1))
	if !isStatefulModernTool(name) {
		return d.execute(ctx, requestID, name, arguments)
	}
	idempotencyKey, _ := arguments["idempotency_key"].(string)
	if idempotencyKey == "" {
		return modernEnvelope(requestID, nil, "failed", "missing_idempotency_key", "Stateful calls require idempotency_key", map[string]any{})
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return modernEnvelope(requestID, nil, "failed", "invalid_arguments", err.Error(), map[string]any{})
	}
	hash := sha256.Sum256(encoded)
	argumentsHash := fmt.Sprintf("%x", hash[:])
	workspaceID, _ := arguments["workspace_id"].(string)
	replayKey := workspaceID + "\x00" + name + "\x00" + idempotencyKey

	d.replayMu.Lock()
	defer d.replayMu.Unlock()
	if previous, ok := d.replays[replayKey]; ok {
		if previous.argumentsHash != argumentsHash {
			return modernEnvelope(requestID, nil, "conflict", "idempotency_key_reused",
				"Idempotency key was already used with different arguments", map[string]any{})
		}
		replayed := cloneEnvelope(previous.result)
		replayed["request_id"] = requestID
		replayed["idempotency"] = "replayed"
		return replayed
	}
	result := d.execute(ctx, requestID, name, arguments)
	result["idempotency"] = "created"
	d.replays[replayKey] = directReplay{argumentsHash: argumentsHash, result: cloneEnvelope(result)}
	return result
}

func isStatefulModernTool(name string) bool {
	switch name {
	case "edit_apply", "change_plan", "verify_run", "debug_session", "debug_breakpoints", "debug_control":
		return true
	default:
		return false
	}
}

func cloneEnvelope(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func (d *directWorkspaces) execute(ctx context.Context, requestID, name string, arguments map[string]any) map[string]any {
	if err := ctx.Err(); err != nil {
		return modernEnvelope(requestID, nil, "failed", "request_cancelled", err.Error(), map[string]any{})
	}
	if name == "workspace_open" {
		return d.open(requestID, arguments)
	}
	workspaceID, _ := arguments["workspace_id"].(string)
	workspace := d.get(workspacecore.ID(workspaceID))
	if workspace == nil {
		return modernEnvelope(requestID, nil, "failed", "workspace_not_found", "Unknown or missing workspace_id", map[string]any{"workspace_id": workspaceID})
	}
	switch name {
	case "workspace_inspect":
		inspection := workspace.Inspect()
		if arguments["view"] == "map" {
			orientation, err := workspace.Orient()
			if err != nil {
				return modernFailure(requestID, workspace, "workspace_map_failed", err)
			}
			return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d workspace entries", len(orientation.Entries)), map[string]any{"inspection": inspection, "overview": orientation})
		}
		return modernEnvelope(requestID, workspace, "ok", "", "Workspace inspection is current", inspection)
	case "search":
		query, _ := arguments["query"].(string)
		mode := workspacecore.SearchMode("literal")
		if requested, _ := arguments["mode"].(string); requested != "" {
			mode = workspacecore.SearchMode(requested)
		}
		result, err := workspace.Search(workspacecore.SearchRequest{Query: query, Mode: mode})
		if err != nil {
			return modernFailure(requestID, workspace, "search_failed", err)
		}
		return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d matches", len(result.Hits)), result)
	case "read":
		return d.read(requestID, workspace, arguments)
	case "edit_apply":
		return d.edit(requestID, workspace, arguments)
	default:
		return modernEnvelope(requestID, workspace, "unavailable", "capability_not_implemented",
			fmt.Sprintf("%s is registered but its semantic provider is not available in S07 direct mode", name),
			map[string]any{"tool": name, "coverage": map[string]any{"complete": false, "unavailable": []string{"semantic_provider_or_later_stage"}}})
	}
}

func (d *directWorkspaces) open(requestID string, arguments map[string]any) map[string]any {
	kind, _ := arguments["kind"].(string)
	options := workspacecore.OpenOptions{Kind: workspacecore.Kind(kind), StateDir: d.stateDir}
	switch kind {
	case "project":
		options.Root, _ = arguments["root"].(string)
	case "documents":
		for _, value := range anySlice(arguments["files"]) {
			if name, ok := value.(string); ok {
				options.Files = append(options.Files, name)
			}
		}
	default:
		return modernEnvelope(requestID, nil, "failed", "invalid_workspace_kind", "kind must be project or documents", map[string]any{})
	}
	workspace, err := workspacecore.Open(options)
	if err != nil {
		return modernEnvelope(requestID, nil, "failed", "workspace_open_failed", err.Error(), map[string]any{})
	}
	d.mu.Lock()
	d.items[workspace.Identity().ID] = workspace
	d.mu.Unlock()
	orientation, err := workspace.Orient()
	if err != nil {
		return modernFailure(requestID, workspace, "workspace_overview_failed", err)
	}
	return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("Opened %s workspace with %d entries", kind, len(orientation.Entries)), map[string]any{
		"revision":       fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
		"capabilities":   workspace.Inspect(),
		"overview":       orientation,
		"recent_commits": []any{},
	})
}

func (d *directWorkspaces) get(id workspacecore.ID) *workspacecore.Workspace {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.items[id]
}

func (d *directWorkspaces) read(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	target, ok := arguments["target"].(map[string]any)
	if !ok {
		return modernEnvelope(requestID, workspace, "failed", "invalid_target", "target must be an object", map[string]any{})
	}
	fileRange, ok := target["file_range"].(map[string]any)
	if !ok {
		return modernEnvelope(requestID, workspace, "unavailable", "target_kind_unavailable", "S07 direct reads require target.file_range", map[string]any{})
	}
	path, _ := fileRange["path"].(string)
	if arguments["view"] == "outline" {
		outline, err := workspace.Outline(path)
		if err != nil {
			return modernFailure(requestID, workspace, "read_failed", err)
		}
		return modernEnvelope(requestID, workspace, "ok", "", "Outline read", outline)
	}
	read, err := workspace.Read(path)
	if err != nil {
		return modernFailure(requestID, workspace, "read_failed", err)
	}
	data := map[string]any{"path": read.Path, "content": string(read.Content), "snapshot": read.Snapshot, "coverage": read.Coverage}
	return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("Read %s", read.Path), data)
}

func (d *directWorkspaces) edit(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	operation, ok := arguments["operation"].(map[string]any)
	if !ok || operation["kind"] != "replace_range" {
		return modernEnvelope(requestID, workspace, "unavailable", "operation_unavailable", "S07 direct edit supports only replace_range", map[string]any{})
	}
	target, _ := operation["target"].(map[string]any)
	fileRange, _ := target["file_range"].(map[string]any)
	handle, err := decodeRangeHandle(fileRange)
	if err != nil {
		return modernFailure(requestID, workspace, "invalid_target", err)
	}
	content, _ := operation["content"].(string)
	var change workspacecore.TextChange
	if preview, _ := arguments["preview_only"].(bool); preview {
		change, err = workspace.PreviewReplace(workspace.Identity().ID, handle, []byte(content))
	} else {
		var after workspacecore.DocumentSnapshot
		change, after, err = workspace.ApplyReplace(workspace.Identity().ID, handle, []byte(content))
		if err == nil {
			change.Workspace = workspace.Identity()
			_ = after
		}
	}
	if err != nil {
		var conflict *workspacecore.Conflict
		if errors.As(err, &conflict) {
			return modernEnvelope(requestID, workspace, "conflict", string(conflict.Code), conflict.Error(), map[string]any{"target": handle})
		}
		return modernFailure(requestID, workspace, "edit_failed", err)
	}
	summary := "Guarded range preview is ready; canonical bytes unchanged"
	if preview, _ := arguments["preview_only"].(bool); !preview {
		summary = "Guarded range edit applied"
	}
	return modernEnvelope(requestID, workspace, "ok", "", summary, map[string]any{
		"change": change, "tool_delta": []any{}, "diagnostic_delta": map[string]any{"new": []any{}, "resolved": []any{}},
		"verification": map[string]any{"confidence": "provisional", "coverage": map[string]any{"edited_documents": "complete", "semantic_provider": "unavailable"}},
	})
}

func decodeRangeHandle(value map[string]any) (workspacecore.RangeHandle, error) {
	if value == nil {
		return workspacecore.RangeHandle{}, errors.New("target.file_range is required")
	}
	integer := func(key string) int {
		number, _ := value[key].(float64)
		return int(number)
	}
	handle := workspacecore.RangeHandle{
		Path: fmt.Sprint(value["path"]), Revision: workspacecore.RevisionID(fmt.Sprint(value["revision_id"])),
		ByteStart: integer("byte_start"), ByteEnd: integer("byte_end"),
		ExpectedSHA256: fmt.Sprint(value["expected_sha256"]), BeforeSHA256: fmt.Sprint(value["before_sha256"]),
		AfterSHA256: fmt.Sprint(value["after_sha256"]), AnchorBytes: integer("anchor_bytes"),
	}
	if handle.Path == "" || handle.Revision == "" {
		return workspacecore.RangeHandle{}, errors.New("file range path and revision_id are required")
	}
	return handle, nil
}

func modernFailure(requestID string, workspace *workspacecore.Workspace, code string, err error) map[string]any {
	return modernEnvelope(requestID, workspace, "failed", code, err.Error(), map[string]any{})
}

func modernEnvelope(requestID string, workspace *workspacecore.Workspace, outcome, code, summary string, data any) map[string]any {
	result := map[string]any{
		"api_version": modernAPIVersion, "request_id": requestID, "outcome": outcome,
		"summary": summary, "data": data,
		"evidence": map[string]any{"ids": []string{}, "truncated": false},
		"warnings": []string{}, "next": []any{},
	}
	if code != "" {
		result["code"] = code
	}
	if workspace != nil {
		result["workspace"] = workspace.Identity()
	}
	return result
}

func anySlice(value any) []any {
	if values, ok := value.([]any); ok {
		return values
	}
	return nil
}

func catalogJSON(profile mcpProfile) ([]byte, error) {
	catalog := modernCatalog(profile)
	tools := make([]map[string]any, 0, len(catalog))
	for _, descriptor := range catalog {
		tools = append(tools, map[string]any{
			"name": descriptor.Name, "description": descriptor.Description,
			"inputSchema": descriptor.InputSchema, "outputSchema": outputEnvelopeSchema(),
		})
	}
	return json.Marshal(tools)
}

func modernCatalogNames(profile mcpProfile) []string {
	catalog := modernCatalog(profile)
	names := make([]string, len(catalog))
	for index, descriptor := range catalog {
		names[index] = descriptor.Name
	}
	return names
}

func validateModernRegistry() error {
	seen := map[string]bool{}
	for _, descriptor := range modernTools {
		if seen[descriptor.Name] {
			return fmt.Errorf("duplicate modern tool %q", descriptor.Name)
		}
		seen[descriptor.Name] = true
		if descriptor.InputSchema["additionalProperties"] != false {
			return fmt.Errorf("tool %s schema is not closed", descriptor.Name)
		}
	}
	expected := map[mcpProfile]int{profileFull: 17, profileOrient: 8, profileEdit: 13, profileDebug: 12}
	for _, profile := range modernProfileOrder {
		if len(modernCatalog(profile)) != expected[profile] {
			return fmt.Errorf("profile %s has %d tools, want %d", profile, len(modernCatalog(profile)), expected[profile])
		}
	}
	return nil
}

func directStateDir() (string, error) {
	base := os.Getenv("HUYANG_DIRECT_STATE_DIR")
	if base != "" {
		absolute, err := filepath.Abs(base)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return "", err
		}
		return absolute, nil
	}
	return os.MkdirTemp("", "huyang-direct-state-")
}

func profileSummary() string {
	names := make([]string, 0, len(modernProfileOrder))
	for _, profile := range modernProfileOrder {
		names = append(names, fmt.Sprintf("%s=%d", profile, len(modernCatalog(profile))))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
