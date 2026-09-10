package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

func readTargetSchema() map[string]any {
	schema := targetSchema()
	alternatives := append([]any{}, schema["oneOf"].([]any)...)
	alternatives = append(alternatives, schemaObject(map[string]any{
		"path": stringSchema("Workspace-relative or absolute text file path."),
	}, "path"))
	schema["oneOf"] = alternatives
	return schema
}

func mutationRangeTargetSchema() map[string]any {
	schema := targetSchema()
	schema["oneOf"] = append([]any{}, schema["oneOf"].([]any)[:2]...)
	return schema
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

func planOperationSchema(operationKinds []string) map[string]any {
	return schemaObject(map[string]any{
		"op_id":                   stringSchema("Stable operation identifier unique within the plan."),
		"kind":                    enumSchema(operationKinds...),
		"target":                  targetSchema(),
		"content":                 stringSchema("Exact UTF-8 content for the declared operation."),
		"path":                    stringSchema("Workspace path for create_file or delete_file."),
		"from":                    stringSchema("Source path for move_file."),
		"to":                      stringSchema("Destination path for move_file."),
		"revision_id":             stringSchema("Expected source or path document revision."),
		"destination_revision_id": stringSchema("Expected destination document revision for move_file."),
		"depends_on":              map[string]any{"type": "array", "items": stringSchema("Predecessor op_id.")},
		"indentation":             enumSchema("exact", "syntax_anchor", "formatter"),
	}, "op_id", "kind")
}

func changePlanSchema(stateful map[string]any, operationKinds []string) map[string]any {
	operation := planOperationSchema(operationKinds)
	operations := map[string]any{"type": "array", "items": operation}
	return schemaObject(map[string]any{
		"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
		"action":            enumSchema("create", "edit", "preview", "inspect", "prepare", "apply", "discard"),
		"operations":        operations,
		"plan_id":           stringSchema("Required after creation unless prepare supplies operations inline."),
		"plan_revision":     map[string]any{"type": "integer", "minimum": 1, "description": "Required with plan_id."},
		"prepared_revision": stringSchema("Required when action=apply."),
		"edit": schemaObject(map[string]any{
			"mode": enumSchema("add", "update", "remove", "reorder", "replace_all"), "operations": operations,
			"op_ids": map[string]any{"type": "array", "items": stringSchema("Operation identifier.")},
		}, "mode"),
	}, "workspace_id", "idempotency_key", "action")
}

func buildModernTools() []modernTool {
	orient := []mcpProfile{profileFull, profileOrient, profileEdit, profileDebug}
	edit := []mcpProfile{profileFull, profileEdit}
	debug := []mcpProfile{profileFull, profileDebug}
	readTarget := map[string]any{"workspace_id": workspaceIDProperty(), "target": targetSchema()}
	stateful := statefulProperties()
	operationKinds := []string{"replace_symbol", "delete_symbol", "insert_before", "insert_after", "replace_range", "create_file", "move_file", "delete_file", "rename_symbol", "move_symbols", "replace_matches", "apply_code_action"}
	refinement := schemaObject(map[string]any{
		"path": stringSchema("Additional path substring."),
		"matched_text": schemaObject(map[string]any{
			"literal": stringSchema("Additional literal match constraint."),
			"regex":   stringSchema("Additional regular-expression constraint."),
		}),
	})
	historySource := schemaObject(map[string]any{
		"query":  stringSchema("Literal query over bounded local history."),
		"ref":    stringSchema("Local revision or range; defaults to HEAD."),
		"fields": map[string]any{"type": "array", "items": enumSchema("message", "path", "diff")},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
	}, "query")
	searchProperties := map[string]any{
		"workspace_id": workspaceIDProperty(), "query": stringSchema("Text or regular expression."), "mode": enumSchema("literal", "regex"),
		"result_set_handle": stringSchema("Frozen current-source result set."), "refine": refinement,
		"git_history": historySource, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
	}
	searchSchema := schemaObject(searchProperties, "workspace_id")
	searchSchema["oneOf"] = []any{
		schemaObject(searchProperties, "workspace_id", "query"),
		schemaObject(searchProperties, "workspace_id", "result_set_handle", "refine"),
		schemaObject(searchProperties, "workspace_id", "git_history"),
	}
	return []modernTool{
		{Name: "workspace_open", Description: "Open a project or exact document allowlist and return its revision, capabilities, compact overview, bounded local commits, and limits.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"kind":  enumSchema("project", "documents"),
			"root":  stringSchema("Project root; required for kind=project."),
			"files": map[string]any{"type": "array", "items": stringSchema("Allowlisted document."), "minItems": 1},
		}, "kind")},
		{Name: "workspace_inspect", Description: "Inspect revision, provider health, semantic coverage, pipeline availability, and limits without mutation.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "view": enumSchema("status", "overview", "map"),
		}, "workspace_id")},
		{Name: "search", Description: "Search current source, monotonically refine a frozen set, or search bounded local Git history without mutation.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: searchSchema},
		{Name: "symbol_find", Description: "Find declarations and return ranked revision-bound handles when a semantic provider is available.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "query": stringSchema("Declaration name or path."), "include_source": map[string]any{"type": "boolean"},
		}, "workspace_id", "query")},
		{Name: "navigate", Description: "Navigate one semantic relationship from a shared revision-bound target.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "relation": enumSchema("definition", "type_definition", "implementation", "references", "incoming_calls", "outgoing_calls", "hover"), "target": targetSchema(),
		}, "workspace_id", "relation", "target")},
		{Name: "read", Description: "Read exact or line-bounded source by path or revision-bound handle, plus outlines, provenance, and commit changes.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "target": readTargetSchema(), "view": enumSchema("source", "outline", "history", "changes"),
			"start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		}, "workspace_id", "target")},
		{Name: "diagnostics", Description: "Inspect normalized diagnostic evidence, confidence, coverage, and provenance.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "since": stringSchema("Optional diagnostic cursor."),
		}, "workspace_id")},
		{Name: "code_actions", Description: "List revision-bound quick fixes or refactors without applying them.", Profiles: edit, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(readTarget, "workspace_id", "target")},
		{Name: "edit_apply", Description: "Preview or apply exactly one guarded range replacement through the native workspace core.", Profiles: edit, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
			"preview_only": map[string]any{"type": "boolean"},
			"operation": schemaObject(map[string]any{
				"kind": enumSchema("replace_range"), "target": mutationRangeTargetSchema(), "content": stringSchema("Exact replacement bytes as UTF-8 text."),
			}, "kind", "target"),
		}, "workspace_id", "idempotency_key", "operation")},
		{Name: "change_plan", Description: "Create, edit, preview, prepare, inspect, apply, or discard one coherent multi-operation plan.", Profiles: edit, Destructive: true, InputSchema: changePlanSchema(stateful, operationKinds)},
		{Name: "verify_run", Description: "Run selected formatting, parser, diagnostic, check, or test stages against an exact revision.", Profiles: edit, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
			"stages":                  map[string]any{"type": "array", "items": enumSchema("format_gate", "parser", "diagnostics", "check", "tests"), "minItems": 1},
			"revision_or_transaction": stringSchema("Canonical revision, prepared revision, or transaction ID."),
			"test_scope":              enumSchema("affected", "full"),
		}, "workspace_id", "idempotency_key", "stages", "revision_or_transaction")},
		{Name: "revision_diff", Description: "Explain changes between two revisions or a stale mutation refusal.", Profiles: edit, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "from_revision": stringSchema("Earlier revision."), "to_revision_or_current": stringSchema("Later revision or current."),
		}, "workspace_id", "from_revision", "to_revision_or_current")},
		{Name: "evidence_get", Description: "Page pending or final diff, diagnostic, command, or provenance evidence.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "evidence_id": stringSchema("Evidence identifier."), "cursor": stringSchema("Optional page cursor."),
		}, "workspace_id", "evidence_id")},
		modernDebugSessionTool(debug),
		modernDebugBreakpointsTool(debug),
		modernDebugControlTool(debug),
		modernDebugInspectTool(debug),
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
		"transaction": schemaObject(map[string]any{
			"id":    stringSchema("Opaque plan or transaction identifier."),
			"state": enumSchema("OPEN", "PREVIEWED", "PREPARING", "CONFLICTED", "FAILED", "PROVISIONAL", "READY", "COMMITTING", "COMMITTED", "RECOVERY_REQUIRED", "ROLLING_BACK", "ROLLED_BACK", "EXPIRED", "DISCARDED"),
		}, "id", "state"),
		"data":     map[string]any{"type": "object", "additionalProperties": true},
		"evidence": schemaObject(map[string]any{"ids": map[string]any{"type": "array", "items": stringSchema("Evidence ID.")}, "truncated": map[string]any{"type": "boolean"}}, "ids", "truncated"),
		"warnings": map[string]any{"type": "array", "items": stringSchema("Warning.")},
		"next":     map[string]any{"type": "array", "maxItems": 2},
		"diagnostic_updates": map[string]any{"type": "array", "items": schemaObject(map[string]any{
			"cursor": stringSchema("Diagnostic inbox cursor."), "kind": enumSchema("new", "resolved"),
			"id": stringSchema("Stable diagnostic ID."), "severity": map[string]any{"type": "integer"},
			"document": stringSchema("Affected document."), "attribution": map[string]any{"type": "object"},
		}, "cursor", "kind", "id")},
		"idempotency":           enumSchema("created", "replayed"),
		"idempotency_persisted": map[string]any{"type": "boolean"},
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
		started := time.Now()
		arguments, err := decodeArguments(request.Params.Arguments)
		if err != nil {
			return nil, err
		}
		if err := validateToolArguments(descriptor.InputSchema, arguments); err != nil {
			return nil, err
		}
		if err := validateModernDebugArguments(descriptor.Name, arguments); err != nil {
			return nil, err
		}
		envelope := direct.call(ctx, descriptor.Name, arguments)
		isError := envelope["outcome"] == "failed" || envelope["outcome"] == "conflict"
		logFriction(descriptor.Name, modernFrictionRoot(direct, descriptor.Name, arguments), arguments,
			map[string]any{
				"isError": isError,
				"content": []map[string]any{{"type": "text", "text": fmt.Sprint(envelope["summary"])}},
			}, started)
		pretty, renderErr := renderJSON(compactTextEnvelope(envelope))
		if renderErr != nil {
			return nil, renderErr
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(pretty)}},
			StructuredContent: envelope,
			IsError:           isError,
		}, nil
	})
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

func compactTextEnvelope(envelope map[string]any) map[string]any {
	compact := map[string]any{
		"api_version": envelope["api_version"], "request_id": envelope["request_id"],
		"outcome": envelope["outcome"], "summary": envelope["summary"],
		"evidence": envelope["evidence"], "warnings": envelope["warnings"], "next": envelope["next"],
	}
	for _, key := range []string{"code", "workspace", "transaction", "idempotency", "idempotency_persisted"} {
		if value, ok := envelope[key]; ok {
			compact[key] = value
		}
	}
	return compact
}

func validateToolArguments(schema map[string]any, arguments map[string]any) error {
	return validateSchemaValue(schema, arguments, "arguments")
}

func validateSchemaValue(schema map[string]any, value any, path string) error {
	if alternatives, ok := schema["oneOf"].([]any); ok {
		base := make(map[string]any, len(schema)-1)
		for key, item := range schema {
			if key != "oneOf" {
				base[key] = item
			}
		}
		if properties, ok := base["properties"].(map[string]any); ok && len(properties) == 0 {
			delete(base, "properties")
			delete(base, "additionalProperties")
		}
		if err := validateSchemaValue(base, value, path); err != nil {
			return err
		}
		matches := 0
		for _, candidate := range alternatives {
			candidateSchema, _ := candidate.(map[string]any)
			if candidateSchema != nil && validateSchemaValue(candidateSchema, value, path) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s must match exactly one allowed shape", path)
		}
		return nil
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
		if closed, present := schema["additionalProperties"].(bool); present && !closed {
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
	records  map[workspacecore.ID]persistedWorkspace
	stateDir string
	requests atomic.Uint64

	replayMu sync.Mutex
	replays  map[string]*directReplay

	persistMu    sync.Mutex
	registryPath string
	loadErr      error
	scheduler    *workspaceScheduler

	providerMu     sync.Mutex
	stagers        map[workspacecore.ID]workspacecore.PlanStager
	providers      map[workspacecore.ID]provider.Provider
	sandboxStagers map[string]*sandboxPlanStager

	verificationMu    sync.Mutex
	verificationCache map[string]cachedVerification
}

type directReplay struct {
	argumentsHash string
	result        map[string]any
	done          chan struct{}
	complete      bool
}

type cachedVerification struct {
	Result  workspacecore.VerificationResult
	Outcome string
}

func newDirectWorkspaces(stateDir string) *directWorkspaces {
	return newDirectWorkspacesWithQuotas(stateDir, 4, 2)
}

func newDirectWorkspacesWithQuotas(stateDir string, providerQuota, externalJobQuota int) *directWorkspaces {
	direct := &directWorkspaces{
		items:             make(map[workspacecore.ID]*workspacecore.Workspace),
		records:           make(map[workspacecore.ID]persistedWorkspace),
		stateDir:          stateDir,
		replays:           make(map[string]*directReplay),
		registryPath:      filepath.Join(stateDir, "registry.json"),
		scheduler:         newWorkspaceScheduler(providerQuota, externalJobQuota),
		stagers:           make(map[workspacecore.ID]workspacecore.PlanStager),
		providers:         make(map[workspacecore.ID]provider.Provider),
		sandboxStagers:    make(map[string]*sandboxPlanStager),
		verificationCache: make(map[string]cachedVerification),
	}
	direct.loadErr = direct.loadRegistry()
	if direct.loadErr == nil {
		direct.loadErr = workspacecore.ReapSandboxes(filepath.Join(stateDir, "sandboxes"), nil)
	}
	return direct
}

func (d *directWorkspaces) call(ctx context.Context, name string, arguments map[string]any) map[string]any {
	requestID := fmt.Sprintf("req_%d", d.requests.Add(1))
	if d.loadErr != nil {
		return modernEnvelope(requestID, nil, "failed", "service_state_unavailable", d.loadErr.Error(), map[string]any{})
	}
	if !isStatefulModernTool(name) {
		return d.executeScheduled(ctx, requestID, name, arguments)
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
	if previous, ok := d.replays[replayKey]; ok {
		d.replayMu.Unlock()
		select {
		case <-previous.done:
			if previous.argumentsHash != argumentsHash {
				return modernEnvelope(requestID, nil, "conflict", "idempotency_key_reused",
					"Idempotency key was already used with different arguments", map[string]any{})
			}
			replayed := cloneEnvelope(previous.result)
			replayed["request_id"] = requestID
			replayed["idempotency"] = "replayed"
			return replayed
		case <-ctx.Done():
			return modernEnvelope(requestID, nil, "failed", "request_cancelled", ctx.Err().Error(), map[string]any{})
		}
	}
	pending := &directReplay{argumentsHash: argumentsHash, done: make(chan struct{})}
	d.replays[replayKey] = pending
	d.replayMu.Unlock()

	result := d.executeScheduled(ctx, requestID, name, arguments)
	result["idempotency"] = "created"
	result["idempotency_persisted"] = true
	d.replayMu.Lock()
	pending.result = cloneEnvelope(result)
	pending.complete = true
	d.replayMu.Unlock()
	if err := d.persistRegistry(); err != nil {
		result["warnings"] = append(result["warnings"].([]string), "idempotency receipt was not persisted: "+err.Error())
		result["idempotency_persisted"] = false
		d.replayMu.Lock()
		pending.result = cloneEnvelope(result)
		d.replayMu.Unlock()
	}
	d.replayMu.Lock()
	close(pending.done)
	d.replayMu.Unlock()
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

func (d *directWorkspaces) executeScheduled(ctx context.Context, requestID, name string, arguments map[string]any) map[string]any {
	if name == "workspace_open" {
		return d.execute(ctx, requestID, name, arguments)
	}
	workspaceID, _ := arguments["workspace_id"].(string)
	class := modernSchedulerClass(name)
	if name == "change_plan" {
		if action, _ := arguments["action"].(string); action != "prepare" {
			class = scheduleCanonicalWrite
		}
	}
	release, err := d.scheduler.acquire(ctx, workspaceID, class)
	if err != nil {
		return modernEnvelope(requestID, nil, "failed", "scheduler_wait_cancelled", err.Error(), map[string]any{
			"workspace_id": workspaceID,
			"class":        class,
		})
	}
	result := d.execute(ctx, requestID, name, arguments)
	release()
	if name != "diagnostics" {
		if workspace := d.get(workspacecore.ID(workspaceID)); workspace != nil {
			if notices := workspace.DiagnosticNotices(20); len(notices) > 0 {
				result["diagnostic_updates"] = notices
			}
		}
	}
	if !isStatefulModernTool(name) {
		if err := d.persistWorkspaceIdentity(workspacecore.ID(workspaceID)); err != nil {
			result["warnings"] = append(result["warnings"].([]string), "workspace state was not persisted: "+err.Error())
		}
	}
	return result
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
	if modernSchedulerClass(name) == scheduleProviderRead {
		transactionID, _ := arguments["transaction_id"].(string)
		if err := workspace.CheckProviderAccess(transactionID); err != nil {
			return modernEnvelope(requestID, workspace, "conflict", "workspace_busy", err.Error(), map[string]any{
				"transaction_id": transactionID,
			})
		}
	}
	switch name {
	case "workspace_inspect":
		inspection := workspace.Inspect()
		policy, policyErr := workspacecore.LoadPipelinePolicy(workspace.Identity().Root, "")
		if policyErr != nil {
			return modernFailure(requestID, workspace, "workspace_policy_invalid", policyErr)
		}
		if arguments["view"] == "map" {
			orientation, err := workspace.Orient()
			if err != nil {
				return modernFailure(requestID, workspace, "workspace_map_failed", err)
			}
			return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d workspace entries", len(orientation.Entries)), map[string]any{
				"inspection": inspection, "overview": orientation, "scheduler": d.scheduler.description(), "pipeline_policy": policy,
			})
		}
		return modernEnvelope(requestID, workspace, "ok", "", "Workspace inspection is current", map[string]any{
			"inspection": inspection, "scheduler": d.scheduler.description(), "pipeline_policy": policy,
		})
	case "search":
		return d.search(requestID, workspace, arguments)
	case "symbol_find":
		return d.symbolFind(requestID, workspace, arguments)
	case "read":
		return d.read(requestID, workspace, arguments)
	case "diagnostics":
		since, _ := arguments["since"].(string)
		report, err := workspace.Diagnostics(since)
		if err != nil {
			return modernFailure(requestID, workspace, "diagnostic_cursor_invalid", err)
		}
		outcome := "ok"
		if report.Confidence == workspacecore.ConfidenceProvisional {
			outcome = "provisional"
		} else if report.Confidence == workspacecore.ConfidenceUnavailable {
			outcome = "unavailable"
		}
		result := modernEnvelope(requestID, workspace, outcome, "", "Diagnostic evidence retrieved from the durable workspace inbox", map[string]any{"diagnostics": report})
		result["evidence"] = map[string]any{"ids": nonNilStrings(report.EvidenceIDs), "truncated": false}
		return result
	case "evidence_get":
		evidence, err := workspace.Evidence(fmt.Sprint(arguments["evidence_id"]))
		if err != nil {
			return modernFailure(requestID, workspace, "evidence_not_found", err)
		}
		result := modernEnvelope(requestID, workspace, "ok", "", "Detailed diagnostic evidence retrieved", map[string]any{"evidence": evidence})
		result["evidence"] = map[string]any{"ids": []string{evidence.ID}, "truncated": false}
		return result
	case "edit_apply":
		return d.edit(requestID, workspace, arguments)
	case "change_plan":
		return d.changePlan(ctx, requestID, workspace, arguments)
	case "verify_run":
		return d.verify(ctx, requestID, workspace, arguments)
	case "revision_diff":
		return d.revisionDiff(requestID, workspace, arguments)
	case "debug_session", "debug_breakpoints", "debug_control", "debug_inspect":
		return d.debug(ctx, requestID, name, workspace, arguments)
	default:
		result := modernEnvelope(requestID, workspace, "unavailable", "semantic_provider_unavailable",
			fmt.Sprintf("%s requires a semantic provider that is not available for this workspace", name),
			map[string]any{"tool": name, "coverage": map[string]any{"complete": false, "unavailable": []string{"semantic_provider"}}})
		result["next"] = []any{
			map[string]any{"tool": "search", "action": "literal_fallback"},
			map[string]any{"tool": "read", "action": "read_known_path"},
		}
		return result
	}
}

func (d *directWorkspaces) open(requestID string, arguments map[string]any) map[string]any {
	kind, _ := arguments["kind"].(string)
	options := workspacecore.OpenOptions{Kind: workspacecore.Kind(kind), StateDir: d.stateDir, ProviderEpoch: 1}
	switch kind {
	case "project":
		options.Root, _ = arguments["root"].(string)
	case "documents":
		for _, value := range anySlice(arguments["files"]) {
			if name, ok := value.(string); ok {
				absolute, err := filepath.Abs(name)
				if err != nil {
					return modernEnvelope(requestID, nil, "failed", "workspace_open_failed", err.Error(), map[string]any{})
				}
				options.Files = append(options.Files, filepath.Clean(absolute))
			}
		}
		sort.Strings(options.Files)
	default:
		return modernEnvelope(requestID, nil, "failed", "invalid_workspace_kind", "kind must be project or documents", map[string]any{})
	}
	opened, err := workspacecore.Open(options)
	if err != nil {
		return modernEnvelope(requestID, nil, "failed", "workspace_open_failed", err.Error(), map[string]any{})
	}
	identity := opened.Identity()
	record := persistedWorkspace{
		ID: identity.ID, Kind: identity.Kind, Root: identity.Root, Files: append([]string(nil), options.Files...),
		ProviderEpoch: identity.Epoch, StateSeq: identity.StateSeq,
	}

	created := true
	d.mu.Lock()
	for id, existing := range d.records {
		if samePersistedWorkspace(existing, record) {
			opened = d.items[id]
			created = false
			break
		}
	}
	if created {
		d.items[identity.ID] = opened
		d.records[identity.ID] = record
	}
	d.mu.Unlock()
	if created {
		if err := d.persistRegistry(); err != nil {
			d.mu.Lock()
			delete(d.items, identity.ID)
			delete(d.records, identity.ID)
			d.mu.Unlock()
			return modernEnvelope(requestID, nil, "failed", "service_state_persist_failed", err.Error(), map[string]any{})
		}
	}
	orientation, err := opened.Orient()
	if err != nil {
		return modernFailure(requestID, opened, "workspace_overview_failed", err)
	}
	recent, recentErr := opened.RecentCommits(3)
	if recentErr != nil {
		recent = workspacecore.CommitList{Coverage: workspacecore.GitCoverage{Complete: false, Unavailable: []string{"git_history"}}}
	}
	action := "Opened"
	if !created {
		action = "Reopened"
	}
	return modernEnvelope(requestID, opened, "ok", "", fmt.Sprintf("%s %s workspace with %d entries", action, kind, len(orientation.Entries)), map[string]any{
		"revision":       fmt.Sprintf("wsrev_%d", opened.Identity().StateSeq),
		"capabilities":   opened.Inspect(),
		"overview":       orientation,
		"recent_commits": recent,
		"registry":       map[string]any{"persistent": true, "reused": !created},
	})
}

func (d *directWorkspaces) get(id workspacecore.ID) *workspacecore.Workspace {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.items[id]
}

func (d *directWorkspaces) search(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	if source, ok := arguments["git_history"].(map[string]any); ok {
		fields := make([]string, 0)
		for _, value := range anySlice(source["fields"]) {
			if field, ok := value.(string); ok {
				fields = append(fields, field)
			}
		}
		query, _ := source["query"].(string)
		ref, _ := source["ref"].(string)
		result, err := workspace.SearchHistory(workspacecore.HistorySearchRequest{
			Query: query, Fields: fields, Ref: ref, Limit: argInt(source, "limit", 20),
		})
		if err != nil {
			return modernFailure(requestID, workspace, "git_history_search_failed", err)
		}
		return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d historical matches", len(result.Hits)), result)
	}
	if parent, _ := arguments["result_set_handle"].(string); parent != "" {
		refine, _ := arguments["refine"].(map[string]any)
		matched, _ := refine["matched_text"].(map[string]any)
		path, _ := refine["path"].(string)
		literal, _ := matched["literal"].(string)
		regex, _ := matched["regex"].(string)
		result, err := workspace.RefineResultSet(workspacecore.ResultSetID(parent), workspacecore.ResultRefinement{
			Path: path, MatchLiteral: literal, MatchRegex: regex,
		})
		if err != nil {
			var conflict *workspacecore.Conflict
			if errors.As(err, &conflict) {
				return modernEnvelope(requestID, workspace, "conflict", string(conflict.Code), conflict.Error(), map[string]any{"result_set_handle": parent})
			}
			return modernFailure(requestID, workspace, "search_refinement_failed", err)
		}
		limit := argInt(arguments, "limit", 50)
		hits, truncated := compactSearchHits(result.Matches, limit)
		envelope := modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d matches retained; %d eliminated", result.Retained, result.Eliminated), map[string]any{
			"hits": hits, "returned": len(hits), "total": result.Retained, "result_set": result, "coverage": result.Coverage,
		})
		if truncated {
			envelope["warnings"] = []string{fmt.Sprintf("response limited to %d of %d retained matches", len(hits), result.Retained)}
			envelope["next"] = []any{map[string]any{"tool": "search", "action": "refine", "result_set_handle": result.Handle}}
		}
		return envelope
	}
	query, _ := arguments["query"].(string)
	mode := workspacecore.SearchMode("literal")
	if requested, _ := arguments["mode"].(string); requested != "" {
		mode = workspacecore.SearchMode(requested)
	}
	result, err := workspace.Search(workspacecore.SearchRequest{Query: query, Mode: mode})
	if err != nil {
		return modernFailure(requestID, workspace, "search_failed", err)
	}
	limit := argInt(arguments, "limit", 50)
	hits, truncated := compactSearchHits(result.Hits, limit)
	summary := fmt.Sprintf("%d matches", len(result.Hits))
	if !result.Coverage.Complete {
		summary = fmt.Sprintf("%d matches; search coverage incomplete", len(result.Hits))
	}
	data := map[string]any{
		"workspace": result.Workspace, "hits": hits, "returned": len(hits), "total": len(result.Hits),
		"coverage": result.Coverage, "query": result.Query, "mode": result.Mode, "result_set": result.ResultSet,
	}
	envelope := modernEnvelope(requestID, workspace, "ok", "", summary, data)
	if truncated {
		envelope["warnings"] = []string{fmt.Sprintf("response limited to %d of %d matches", len(hits), len(result.Hits))}
		envelope["next"] = []any{map[string]any{"tool": "search", "action": "refine", "result_set_handle": result.ResultSet.Handle}}
	} else if !result.Coverage.Complete {
		envelope["next"] = []any{map[string]any{"tool": "read", "action": "read_known_path"}}
	}
	return envelope
}

func compactSearchHits(hits []workspacecore.SearchHit, limit int) ([]map[string]any, bool) {
	if limit <= 0 {
		limit = 100
	}
	returned := hits
	if len(returned) > limit {
		returned = returned[:limit]
	}
	compact := make([]map[string]any, 0, len(returned))
	for _, hit := range returned {
		item := map[string]any{
			"path": hit.Path, "byte_start": hit.ByteStart, "byte_end": hit.ByteEnd,
			"line": hit.Line, "column": hit.Column, "match": hit.Match, "range": hit.Range,
		}
		if hit.MatchHandle != nil {
			item["handle"] = hit.MatchHandle.Handle
			item["match_handle"] = map[string]any{"handle": hit.MatchHandle.Handle}
		}
		compact = append(compact, item)
	}
	return compact, len(hits) > len(returned)
}

func (d *directWorkspaces) symbolFind(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	query, _ := arguments["query"].(string)
	records, coverage, err := workspace.FindSymbols(query)
	if err != nil {
		return modernFailure(requestID, workspace, "symbol_find_failed", err)
	}
	items := make([]map[string]any, 0, len(records))
	includeSource, _ := arguments["include_source"].(bool)
	for _, record := range records {
		item := map[string]any{"handle": record}
		if includeSource {
			read, readErr := workspace.Read(record.Locator.Path)
			if readErr == nil && record.Locator.ByteStart >= 0 && record.Locator.ByteEnd <= len(read.Content) {
				item["source"] = string(read.Content[record.Locator.ByteStart:record.Locator.ByteEnd])
			}
		}
		items = append(items, item)
	}
	outcome := "ok"
	code := ""
	if !coverage.Complete {
		outcome = "partial"
		code = "semantic_coverage_partial"
	}
	summary := fmt.Sprintf("%d symbols found", len(records))
	if len(records) == 0 && !coverage.Complete {
		summary = "Symbol search could not establish results because semantic coverage is incomplete"
	}
	result := modernEnvelope(requestID, workspace, outcome, code, summary, map[string]any{
		"ranked_handles": items, "coverage": coverage,
	})
	if !coverage.Complete {
		result["next"] = []any{map[string]any{"tool": "search", "action": "literal_fallback", "query": query}}
	}
	return result
}

func (d *directWorkspaces) read(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	target, ok := arguments["target"].(map[string]any)
	if !ok {
		return modernEnvelope(requestID, workspace, "failed", "invalid_target", "target must be an object", map[string]any{})
	}
	view, _ := arguments["view"].(string)
	opaque, _ := target["handle"].(string)
	if view == "changes" {
		if opaque == "" {
			return modernEnvelope(requestID, workspace, "failed", "commit_handle_required", "changes view requires an opaque commit handle", map[string]any{})
		}
		changes, err := workspace.CommitChanges(workspacecore.CommitHandle(opaque), argInt(arguments, "limit", 20))
		if err != nil {
			return modernFailure(requestID, workspace, "commit_changes_failed", err)
		}
		return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d changed paths", len(changes.Changes)), changes)
	}

	var path string
	startLine, endLine := argInt(arguments, "start_line", 0), argInt(arguments, "end_line", 0)
	if opaque != "" {
		resolution, err := workspace.ResolveHandle(workspacecore.HandleID(opaque))
		if err != nil {
			return modernFailure(requestID, workspace, "handle_resolve_failed", err)
		}
		if resolution.Status == workspacecore.ResolutionConflicted {
			return modernEnvelope(requestID, workspace, "conflict", string(resolution.Code), "Handle no longer resolves uniquely", resolution)
		}
		resolved, err := resolution.RangeHandle()
		if err != nil {
			return modernFailure(requestID, workspace, "handle_resolve_failed", err)
		}
		path = resolved.Path
		read, err := workspace.Read(path)
		if err != nil {
			return modernFailure(requestID, workspace, "read_failed", err)
		}
		if resolved.ByteStart < 0 || resolved.ByteEnd > len(read.Content) || resolved.ByteEnd < resolved.ByteStart {
			return modernEnvelope(requestID, workspace, "conflict", "target_deleted", "Resolved handle range is no longer readable", resolution)
		}
		if view == "history" {
			startLine = bytes.Count(read.Content[:resolved.ByteStart], []byte("\n")) + 1
			endLine = bytes.Count(read.Content[:resolved.ByteEnd], []byte("\n")) + 1
		} else if startLine == 0 && endLine == 0 {
			return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("Read %s", resolved.Path), map[string]any{
				"path": resolved.Path, "content": string(read.Content[resolved.ByteStart:resolved.ByteEnd]), "snapshot": read.Snapshot,
				"coverage": read.Coverage, "resolution": resolution,
			})
		}
	} else {
		if directPath, present := target["path"].(string); present {
			path = directPath
		} else if symbol, present := target["symbol_locator"].(map[string]any); present {
			path, _ = symbol["path"].(string)
		} else if fileRange, present := target["file_range"].(map[string]any); present {
			path, _ = fileRange["path"].(string)
		} else {
			return modernEnvelope(requestID, workspace, "unavailable", "target_kind_unavailable", "read requires target.path, target.handle, target.symbol_locator, or target.file_range", map[string]any{})
		}
	}
	if view == "history" {
		history, err := workspace.FileHistory(workspacecore.HistoryRequest{Path: path, StartLine: startLine, EndLine: endLine, Limit: argInt(arguments, "limit", 20)})
		if err != nil {
			return modernFailure(requestID, workspace, "git_history_read_failed", err)
		}
		return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d provenance spans", len(history.Spans)), history)
	}
	if view == "outline" {
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
	content, actualStart, actualEnd, rangeErr := boundedLines(read.Content, startLine, endLine)
	if rangeErr != nil {
		return modernFailure(requestID, workspace, "invalid_line_range", rangeErr)
	}
	data := map[string]any{"path": read.Path, "content": string(content), "snapshot": read.Snapshot, "coverage": read.Coverage}
	if startLine != 0 || endLine != 0 {
		data["start_line"], data["end_line"] = actualStart, actualEnd
	}
	return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("Read %s", read.Path), data)
}

func boundedLines(content []byte, startLine, endLine int) ([]byte, int, int, error) {
	if startLine == 0 && endLine == 0 {
		return content, 0, 0, nil
	}
	if startLine == 0 {
		startLine = 1
	}
	if endLine == 0 {
		endLine = startLine
	}
	if endLine < startLine {
		return nil, 0, 0, errors.New("end_line must be greater than or equal to start_line")
	}
	lines := bytes.SplitAfter(content, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if startLine > len(lines) {
		return nil, 0, 0, fmt.Errorf("start_line %d exceeds document line count %d", startLine, len(lines))
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	return bytes.Join(lines[startLine-1:endLine], nil), startLine, endLine, nil
}

func (d *directWorkspaces) edit(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	operation, ok := arguments["operation"].(map[string]any)
	if !ok || operation["kind"] != "replace_range" {
		return modernEnvelope(requestID, workspace, "unavailable", "operation_unavailable", "S07 direct edit supports only replace_range", map[string]any{})
	}
	target, _ := operation["target"].(map[string]any)
	var handle workspacecore.RangeHandle
	var resolution *workspacecore.HandleResolution
	var err error
	if opaque, _ := target["handle"].(string); opaque != "" {
		resolved, resolveErr := workspace.ResolveHandle(workspacecore.HandleID(opaque))
		if resolveErr != nil {
			return modernFailure(requestID, workspace, "handle_resolve_failed", resolveErr)
		}
		resolution = &resolved
		if resolved.Status == workspacecore.ResolutionConflicted {
			return modernEnvelope(requestID, workspace, "conflict", string(resolved.Code), "Handle no longer resolves uniquely", resolved)
		}
		handle, err = resolved.RangeHandle()
	} else {
		fileRange, _ := target["file_range"].(map[string]any)
		handle, err = decodeRangeHandle(fileRange)
	}
	if err != nil {
		return modernFailure(requestID, workspace, "invalid_target", err)
	}
	content, _ := operation["content"].(string)
	var change workspacecore.TextChange
	var after workspacecore.DocumentSnapshot
	preview, _ := arguments["preview_only"].(bool)
	if preview {
		change, err = workspace.PreviewReplace(workspace.Identity().ID, handle, []byte(content))
	} else {
		change, after, err = workspace.ApplyReplace(workspace.Identity().ID, handle, []byte(content))
		if err == nil {
			change.Workspace = workspace.Identity()
		}
	}
	if err != nil {
		var conflict *workspacecore.Conflict
		if errors.As(err, &conflict) {
			return modernEnvelope(requestID, workspace, "conflict", string(conflict.Code), conflict.Error(), map[string]any{"target": handle})
		}
		return modernFailure(requestID, workspace, "edit_failed", err)
	}
	summary, outcome := "Guarded range preview is ready; canonical bytes unchanged", "ok"
	data := map[string]any{
		"change": compactTextChange(change), "resolution": resolution, "tool_delta": []any{},
		"diagnostic_delta": map[string]any{"new": []any{}, "resolved": []any{}},
		"from_revision":    fmt.Sprintf("wsrev_%d", change.Before.Workspace.StateSeq), "canonical_changed": !preview,
	}
	evidenceIDs := make([]string, 0)
	if !preview {
		report, evidenceErr := workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
			Kind: workspacecore.EvidencePush, ProviderID: "native_text", Producer: "native_text_core",
			Document: handle.Path, DocumentRevision: string(after.Revision), TimedOut: true, Selected: true,
			Dimension: "edited_documents",
		})
		outcome = "provisional"
		if evidenceErr != nil {
			summary = "Guarded range edit applied; diagnostic evidence could not be persisted"
			data["verification"] = map[string]any{"confidence": "unavailable", "error": evidenceErr.Error()}
		} else {
			summary = "Guarded range edit applied; semantic diagnostic coverage is provisional"
			data["verification"] = report
			evidenceIDs = report.EvidenceIDs
		}
		data["document_revision"] = after.Revision
	}
	data["revision"] = fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	result := modernEnvelope(requestID, workspace, outcome, "", summary, data)
	result["evidence"] = map[string]any{"ids": nonNilStrings(evidenceIDs), "truncated": false}
	return result
}

func (d *directWorkspaces) revisionDiff(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	from := fmt.Sprint(arguments["from_revision"])
	to := fmt.Sprint(arguments["to_revision_or_current"])
	current := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	if to == "current" {
		to = current
	}
	fromSeq, fromErr := workspaceRevisionSequence(from)
	toSeq, toErr := workspaceRevisionSequence(to)
	if fromErr != nil || toErr != nil || fromSeq > toSeq {
		return modernEnvelope(requestID, workspace, "failed", "invalid_revision_range", "revision_diff requires an ordered wsrev_N range", map[string]any{"from_revision": from, "to_revision": to})
	}
	if toSeq > workspace.Identity().StateSeq {
		return modernEnvelope(requestID, workspace, "conflict", "revision_changed", "Requested target revision is newer than the workspace", map[string]any{"current_revision": current})
	}
	type recordedDiff struct {
		from, to uint64
		diff     any
	}
	d.replayMu.Lock()
	var recorded []recordedDiff
	for key, replay := range d.replays {
		if !replay.complete || !strings.HasPrefix(key, string(workspace.Identity().ID)+"\x00edit_apply\x00") {
			continue
		}
		data, _ := replay.result["data"].(map[string]any)
		if changed, _ := data["canonical_changed"].(bool); !changed {
			continue
		}
		editFrom, editTo := fmt.Sprint(data["from_revision"]), fmt.Sprint(data["revision"])
		left, leftErr := workspaceRevisionSequence(editFrom)
		right, rightErr := workspaceRevisionSequence(editTo)
		if leftErr == nil && rightErr == nil && left >= fromSeq && right <= toSeq {
			change, _ := data["change"].(map[string]any)
			if diff, ok := change["diff"]; ok {
				recorded = append(recorded, recordedDiff{from: left, to: right, diff: diff})
			}
		}
	}
	d.replayMu.Unlock()
	sort.Slice(recorded, func(i, j int) bool { return recorded[i].from < recorded[j].from })
	diffs := make([]any, 0, len(recorded))
	cursor := fromSeq
	for _, item := range recorded {
		if item.from != cursor {
			continue
		}
		diffs = append(diffs, item.diff)
		cursor = item.to
	}
	if cursor != toSeq {
		result := modernEnvelope(requestID, workspace, "partial", "diff_evidence_incomplete", "Native edit evidence does not cover the full requested revision range", map[string]any{
			"from_revision": from, "to_revision": to, "current_revision": current, "covered_through": fmt.Sprintf("wsrev_%d", cursor), "diffs": diffs,
		})
		result["warnings"] = []string{"The uncovered revision may contain an external write, provider edit, or expired receipt."}
		result["next"] = []any{map[string]any{"tool": "read", "action": "inspect_current_files"}}
		return result
	}
	return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d native edit diffs between %s and %s", len(diffs), from, to), map[string]any{
		"from_revision": from, "to_revision": to, "current_revision": current, "diffs": diffs,
	})
}

func workspaceRevisionSequence(revision string) (uint64, error) {
	var sequence uint64
	if _, err := fmt.Sscanf(revision, "wsrev_%d", &sequence); err != nil || sequence == 0 {
		return 0, errors.New("invalid workspace revision")
	}
	return sequence, nil
}

func compactTextChange(change workspacecore.TextChange) map[string]any {
	return map[string]any{
		"workspace":    change.Workspace,
		"before":       change.Before,
		"after_sha256": change.AfterHash,
		"range":        change.Range,
		"replacement":  string(change.Replacement),
		"diff": map[string]any{
			"path": change.Diff.Path, "before_sha256": change.Diff.BeforeSHA256,
			"after_sha256": change.Diff.AfterSHA256, "patch": change.Diff.Patch,
		},
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (d *directWorkspaces) changePlan(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	action, _ := arguments["action"].(string)
	decode := func(value any, target any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, target)
	}
	planResult := func(summary string, plan workspacecore.PlanRecord) map[string]any {
		result := modernEnvelope(requestID, workspace, "ok", "", summary, map[string]any{"plan": plan})
		result["transaction"] = map[string]any{"id": plan.PlanID, "state": plan.State}
		if plan.Preparation != nil {
			evidenceIDs := make([]string, 0)
			for _, stage := range plan.Preparation.Verification {
				evidenceIDs = append(evidenceIDs, stage.EvidenceIDs...)
			}
			result["evidence"] = map[string]any{"ids": evidenceIDs, "truncated": false}
		}
		if plan.State == workspacecore.PlanProvisional {
			result["outcome"] = "provisional"
		}
		if plan.Preview != nil && plan.Preview.Outcome == "conflict" {
			result["outcome"] = "conflict"
			result["code"] = "plan_validation_conflicts"
			result["summary"] = fmt.Sprintf("Plan preview found %d conflicts; canonical workspace unchanged", len(plan.Preview.Conflicts))
		}
		return result
	}
	var plan workspacecore.PlanRecord
	var err error
	switch action {
	case "create":
		var operations []workspacecore.PlanOperation
		if err = decode(arguments["operations"], &operations); err == nil {
			plan, err = workspace.CreatePlan(operations)
		}
		if err == nil {
			return planResult("Plan intent created; canonical workspace unchanged", plan)
		}
	case "edit":
		var edit workspacecore.PlanEdit
		if err = decode(arguments["edit"], &edit); err == nil {
			plan, err = workspace.EditPlan(
				fmt.Sprint(arguments["plan_id"]), uintArgument(arguments["plan_revision"]), edit,
			)
		}
		if err == nil {
			return planResult("Plan intent updated; canonical workspace unchanged", plan)
		}
	case "preview":
		plan, err = workspace.PreviewPlan(
			fmt.Sprint(arguments["plan_id"]), uintArgument(arguments["plan_revision"]),
		)
		if err == nil {
			return planResult("Deterministic plan preview recorded; canonical workspace unchanged", plan)
		}
	case "inspect":
		plan, err = workspace.InspectPlan(
			fmt.Sprint(arguments["plan_id"]), uintArgument(arguments["plan_revision"]),
		)
		if err == nil {
			return planResult("Plan inspection loaded from durable intent", plan)
		}
	case "prepare":
		planID := fmt.Sprint(arguments["plan_id"])
		revision := uintArgument(arguments["plan_revision"])
		if raw, inline := arguments["operations"]; inline {
			var operations []workspacecore.PlanOperation
			if err = decode(raw, &operations); err == nil {
				plan, err = workspace.CreatePlan(operations)
				if err == nil {
					planID, revision = plan.PlanID, plan.PlanRevision
				}
			}
		}
		var stager workspacecore.PlanStager
		if err == nil {
			stager, err = d.planStager(workspace, planID, revision, true)
		}
		if err == nil {
			plan, err = workspace.PreparePlan(ctx, planID, revision, stager)
		}
		if err == nil {
			return planResult("Plan prepared in an isolated sandbox; canonical workspace unchanged", plan)
		}
	case "discard":
		planID := fmt.Sprint(arguments["plan_id"])
		revision := uintArgument(arguments["plan_revision"])
		current, inspectErr := workspace.InspectPlan(planID, revision)
		if inspectErr != nil {
			err = inspectErr
			break
		}
		if current.State == workspacecore.PlanReady || current.State == workspacecore.PlanProvisional || current.State == workspacecore.PlanFailed {
			stager, stagerErr := d.planStager(workspace, planID, revision, false)
			if stagerErr != nil {
				err = stagerErr
			} else {
				plan, err = workspace.RollbackPlan(ctx, planID, revision, stager)
			}
		} else {
			plan, err = workspace.DiscardPlan(planID, revision)
		}
		if err == nil {
			return planResult("Plan discarded and isolated sandbox removed; canonical workspace unchanged", plan)
		}
	case "apply":
		planID := fmt.Sprint(arguments["plan_id"])
		revision := uintArgument(arguments["plan_revision"])
		preparedRevision := fmt.Sprint(arguments["prepared_revision"])
		var stager workspacecore.PlanStager
		stager, err = d.planStager(workspace, planID, revision, false)
		if err == nil {
			plan, err = workspace.CommitPlan(ctx, planID, revision, preparedRevision, stager)
		}
		if err == nil {
			return planResult("Prepared plan applied through the durable commit journal; canonical provider resynced", plan)
		}
	default:
		err = fmt.Errorf("unknown change_plan action %q", action)
	}
	if err != nil {
		code := "plan_action_failed"
		outcome := "failed"
		switch {
		case strings.Contains(err.Error(), "plan_revision_changed"):
			code, outcome = "plan_revision_changed", "conflict"
		case strings.Contains(err.Error(), "workspace_busy"):
			code, outcome = "workspace_busy", "conflict"
		case strings.Contains(err.Error(), "plan_validation_conflicts"):
			code, outcome = "plan_validation_conflicts", "conflict"
		case strings.Contains(err.Error(), "commit_precondition_changed"), strings.Contains(err.Error(), "prepared_revision_changed"):
			code, outcome = "commit_precondition_changed", "conflict"
		case strings.Contains(err.Error(), "recovery"), plan.State == workspacecore.PlanRecoveryRequired:
			code = "commit_recovery_required"
		case strings.Contains(err.Error(), "provider"):
			code = "provider_prepare_failed"
		}
		data := map[string]any{"action": action, "canonical_changed": false}
		if plan.PlanID != "" {
			data["plan"] = plan
			if plan.Preparation != nil {
				data["canonical_changed"] = plan.Preparation.CanonicalChanged
			}
		}
		return modernEnvelope(requestID, workspace, outcome, code, err.Error(), data)
	}
	panic("unreachable")
}

func (d *directWorkspaces) verify(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	revision := fmt.Sprint(arguments["revision_or_transaction"])
	var stages []string
	for _, value := range anySlice(arguments["stages"]) {
		stage, ok := value.(string)
		if !ok {
			return modernFailure(requestID, workspace, "invalid_verification_stage", errors.New("verification stage must be a string"))
		}
		stages = append(stages, stage)
	}
	testScope := fmt.Sprint(arguments["test_scope"])
	request := workspacecore.VerificationRequest{
		Stages: stages, Revision: revision, TestScope: testScope,
		TestHistoryPath: filepath.Join(d.stateDir, "test-history", string(workspace.Identity().ID)+".json"),
	}
	cacheKey := strings.Join([]string{
		string(workspace.Identity().ID), revision, strings.Join(stages, "\x1f"), testScope,
	}, "\x00")
	d.verificationMu.Lock()
	cached, cacheHit := d.verificationCache[cacheKey]
	d.verificationMu.Unlock()
	if cacheHit {
		return modernEnvelope(requestID, workspace, cached.Outcome, "", "Verification reused for the exact revision and stage selection", map[string]any{
			"verification": cached.Result, "cache": "revision_hit",
		})
	}
	d.providerMu.Lock()
	var stager *sandboxPlanStager
	for planID, candidate := range d.sandboxStagers {
		if planID == revision {
			stager = candidate
			break
		}
		if _, result, available := candidate.PreparedRequest(); available && result.Revision == revision {
			stager = candidate
			break
		}
	}
	d.providerMu.Unlock()

	var result workspacecore.VerificationResult
	var err error
	if stager != nil {
		result, err = stager.Verify(ctx, request)
	} else {
		identity := workspace.Identity()
		current := fmt.Sprintf("wsrev_%d", identity.StateSeq)
		if revision != current {
			return modernEnvelope(requestID, workspace, "conflict", "revision_changed",
				"Requested canonical revision is not current", map[string]any{
					"revision_or_transaction": revision, "current_revision": current,
				})
		}
		sandbox, materializeErr := workspacecore.MaterializeSandbox(
			ctx, identity.Root, filepath.Join(d.stateDir, "sandboxes"), identity.ID,
			"verify_"+requestID, 1, current, workspacecore.DefaultSandboxLimits(),
		)
		if materializeErr != nil {
			return modernFailure(requestID, workspace, "verification_sandbox_failed", materializeErr)
		}
		files, filesErr := sandbox.BaseStageFiles()
		if filesErr == nil {
			var policy workspacecore.PipelinePolicy
			policy, filesErr = workspacecore.LoadPipelinePolicy(identity.Root, "")
			if filesErr == nil {
				var diagnosticProvider provider.Provider
				if slices.Contains(stages, "diagnostics") {
					diagnosticProvider, filesErr = referenceProviders.Open(providerOpenConfig{
						Root: sandbox.Tree, InitFile: os.Getenv("AGENT99_HEADLESS_INIT"), RuntimePath: shippedRuntimePath(),
					})
					if filesErr == nil {
						defer diagnosticProvider.Close(context.Background())
						request.DiagnosticVerifier = func(verifyCtx context.Context, revision string, stageFiles []workspacecore.PlanStageFile) (workspacecore.VerificationStage, error) {
							report, evidenceErr := recordProviderDiagnostics(verifyCtx, workspace, diagnosticProvider, stageFiles, revision, "")
							return diagnosticVerificationStage(revision, report), evidenceErr
						}
					}
				}
				if filesErr == nil {
					result, err = workspacecore.RunVerificationPipeline(ctx, sandbox, policy, request, files)
				}
			}
		}
		if cleanupErr := sandbox.Cleanup(); err == nil && filesErr == nil && cleanupErr != nil {
			err = cleanupErr
		}
		if filesErr != nil {
			err = filesErr
		}
	}
	if err != nil {
		return modernEnvelope(requestID, workspace, "failed", "verification_failed", err.Error(), map[string]any{"verification": result})
	}
	outcome := "ok"
	for _, stage := range result.Stages {
		if stage.Status == workspacecore.VerificationSkipped {
			outcome = "partial"
			break
		}
	}
	d.verificationMu.Lock()
	d.verificationCache[cacheKey] = cachedVerification{Result: result, Outcome: outcome}
	d.verificationMu.Unlock()
	return modernEnvelope(requestID, workspace, outcome, "", "Verification completed against exact sandbox bytes", map[string]any{
		"verification": result, "cache": "revision_miss",
	})
}

func uintArgument(value any) uint64 {
	number, _ := value.(float64)
	return uint64(number)
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
