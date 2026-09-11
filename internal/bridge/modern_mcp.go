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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	modernAPIVersion                 = "huyang.workspace/v1alpha1"
	defaultToolCallTimeout           = 2 * time.Minute
	verificationProviderAttachWaitMS = 1500
	verificationDiagnosticSettleWait = 1500 * time.Millisecond
)

var errGlobalToolCallTimeout = errors.New("global tool-call timeout exceeded")

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
		"language_server_status", "language_server_setup", "debug_session", "debug_breakpoints", "debug_control", "debug_inspect",
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
		"revision_id":             stringSchema("Expected source/path document revision. Optional for create_file; Huyang binds the current missing-target revision.."),
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
		{Name: "language_server_status", Description: "Inspect the owned Neovim provider and probe language-server attachment for languages in this workspace.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(),
		}, "workspace_id")},
		{Name: "language_server_setup", Description: "Explicitly install or restart a workspace language server through the owned Neovim provider.", Profiles: edit, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
			"action": enumSchema("install", "restart"), "language": stringSchema("Filetype or extension, required for install."),
			"server": stringSchema("Optional Mason package or lspconfig name; none installs only the parser."),
			"parser": map[string]any{"type": "boolean"},
		}, "workspace_id", "idempotency_key", "action")},
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
		return "", errors.New("usage: huyang mcp [--profile full|orient|edit|debug|legacy]")
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
		client := modernClientName(request)
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
		envelope := direct.call(ctx, descriptor.Name, arguments)
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

func compactTextEnvelope(envelope map[string]any) map[string]any {
	compact := map[string]any{
		"api_version": envelope["api_version"], "request_id": envelope["request_id"],
		"outcome": envelope["outcome"], "summary": envelope["summary"],
		"data": compactTextData(envelope["data"]), "evidence": envelope["evidence"],
		"warnings": envelope["warnings"], "next": envelope["next"],
	}
	for _, key := range []string{"code", "workspace", "transaction", "idempotency", "idempotency_persisted"} {
		if value, ok := envelope[key]; ok {
			compact[key] = value
		}
	}
	return compact
}

func compactTextData(value any) any {
	compacted := compactStructuredData(value)
	data, ok := compacted.(map[string]any)
	if !ok {
		return compacted
	}
	for _, key := range []string{"overview", "map"} {
		orientation, ok := data[key].(map[string]any)
		if !ok {
			continue
		}
		delete(orientation, "entries")
	}
	return data
}

const maxStructuredEntries = 100

func compactStructuredEnvelope(envelope map[string]any) map[string]any {
	compact := cloneEnvelope(envelope)
	compact["data"] = compactStructuredData(envelope["data"])
	return compact
}

func compactStructuredData(value any) any {
	data, ok := value.(map[string]any)
	if !ok {
		return value
	}
	compact := make(map[string]any, len(data))
	for key, item := range data {
		switch typed := item.(type) {
		case workspacecore.Orientation:
			entries := typed.Entries
			truncated := len(entries) > maxStructuredEntries
			if truncated {
				entries = entries[:maxStructuredEntries]
			}
			compact[key] = map[string]any{
				"workspace": typed.Workspace, "coverage": typed.Coverage,
				"entries": entries, "entry_count": len(typed.Entries), "entries_truncated": truncated,
			}
		case workspacecore.PlanRecord:
			compact[key] = compactPlanRecord(typed)
		default:
			compact[key] = item
		}
	}
	return compact
}

func compactPlanRecord(plan workspacecore.PlanRecord) map[string]any {
	operations := make([]map[string]any, 0, len(plan.Operations))
	for _, operation := range plan.Operations {
		item := map[string]any{
			"op_id": operation.OpID, "kind": operation.Kind,
			"path": operation.Path, "from": operation.From, "to": operation.To,
			"revision_id": operation.Revision, "destination_revision_id": operation.DestinationRevision,
			"depends_on": operation.DependsOn, "indentation": operation.Indentation,
		}
		if operation.Content != "" {
			sum := sha256.Sum256([]byte(operation.Content))
			item["content_bytes"] = len(operation.Content)
			item["content_sha256"] = fmt.Sprintf("%x", sum[:])
		}
		if operation.Target != nil {
			target := map[string]any{}
			if operation.Target.Handle != "" {
				target["handle"] = operation.Target.Handle
			}
			if operation.Target.FileRange != nil {
				target["file_range"] = map[string]any{
					"path":        operation.Target.FileRange.Path,
					"revision_id": operation.Target.FileRange.Revision,
				}
			}
			if operation.Target.SymbolLocator != nil {
				target["symbol_locator"] = operation.Target.SymbolLocator
			}
			item["target"] = target
		}
		operations = append(operations, item)
	}
	result := map[string]any{
		"plan_id": plan.PlanID, "workspace_id": plan.WorkspaceID, "state": plan.State,
		"plan_revision": plan.PlanRevision, "base_state_seq": plan.BaseStateSeq,
		"operations": operations, "operation_count": len(plan.Operations),
		"event_count": len(plan.Events), "created_at": plan.CreatedAt, "updated_at": plan.UpdatedAt,
	}
	if plan.Preview != nil {
		diffs := make([]map[string]any, 0, len(plan.Preview.Diffs))
		for _, diff := range plan.Preview.Diffs {
			diffs = append(diffs, map[string]any{
				"path": diff.Path, "before_sha256": diff.BeforeSHA256, "after_sha256": diff.AfterSHA256,
				"before_bytes": len(diff.Before), "after_bytes": len(diff.After), "patch_bytes": len(diff.Patch),
			})
		}
		result["preview"] = map[string]any{
			"preview_revision": plan.Preview.PreviewRevision, "plan_revision": plan.Preview.PlanRevision,
			"outcome": plan.Preview.Outcome, "normalized_order": plan.Preview.NormalizedOrder,
			"affected_files": plan.Preview.AffectedFiles, "diffs": diffs,
			"conflicts": plan.Preview.Conflicts, "canonical_changed": plan.Preview.CanonicalChanged,
		}
	}
	if plan.Preparation != nil {
		preparation := *plan.Preparation
		preparation.ToolDelta = append([]workspacecore.ToolDelta(nil), plan.Preparation.ToolDelta...)
		preparation.CommittedDiffs = append([]workspacecore.ExactDiff(nil), plan.Preparation.CommittedDiffs...)
		for index := range preparation.CommittedDiffs {
			preparation.CommittedDiffs[index].Before = nil
			preparation.CommittedDiffs[index].After = nil
			preparation.CommittedDiffs[index].Patch = ""
		}
		for index := range preparation.ToolDelta {
			preparation.ToolDelta[index].Before = nil
			preparation.ToolDelta[index].After = nil
		}
		result["preparation"] = preparation
	}
	return result
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
	mu          sync.RWMutex
	items       map[workspacecore.ID]*workspacecore.Workspace
	records     map[workspacecore.ID]persistedWorkspace
	stateDir    string
	toolTimeout time.Duration
	requests    atomic.Uint64

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
		toolTimeout:       defaultToolCallTimeout,
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
	ctx, cancel := context.WithTimeoutCause(ctx, d.toolTimeout, errGlobalToolCallTimeout)
	defer cancel()
	requestID := fmt.Sprintf("req_%d", d.requests.Add(1))
	if d.loadErr != nil {
		return modernEnvelope(requestID, nil, "failed", "service_state_unavailable", d.loadErr.Error(), map[string]any{})
	}
	if !isStatefulModernTool(name) {
		result := d.executeScheduled(ctx, requestID, name, arguments)
		normalizeToolTimeout(ctx, d.toolTimeout, name, result)
		return result
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
				result := modernEnvelope(requestID, nil, "conflict", "idempotency_key_reused",
					"Idempotency key was already used with different arguments; use a new key for a different request", map[string]any{
						"tool": name, "idempotency_key": idempotencyKey,
					})
				result["next"] = []any{map[string]any{
					"tool": name, "action": "retry_with_new_idempotency_key",
				}}
				return result
			}
			replayed := cloneEnvelope(previous.result)
			replayed["request_id"] = requestID
			replayed["idempotency"] = "replayed"
			replayed["summary"] = "Idempotent replay returned the original receipt; this call made no new mutation"
			if originalData, ok := previous.result["data"].(map[string]any); ok {
				replayData := make(map[string]any, len(originalData)+2)
				for key, value := range originalData {
					replayData[key] = value
				}
				if changed, ok := replayData["canonical_changed"].(bool); ok {
					replayData["original_canonical_changed"] = changed
					replayData["canonical_changed"] = false
				}
				replayData["replayed_request"] = true
				replayed["data"] = replayData
			}
			return replayed
		case <-ctx.Done():
			result := modernEnvelope(requestID, nil, "failed", "request_cancelled", ctx.Err().Error(), map[string]any{})
			normalizeToolTimeout(ctx, d.toolTimeout, name, result)
			return result
		}
	}
	pending := &directReplay{argumentsHash: argumentsHash, done: make(chan struct{})}
	d.replays[replayKey] = pending
	d.replayMu.Unlock()

	result := d.executeScheduled(ctx, requestID, name, arguments)
	timedOut := normalizeToolTimeout(ctx, d.toolTimeout, name, result)
	result["idempotency"] = "created"
	result["idempotency_persisted"] = !timedOut
	if timedOut {
		d.replayMu.Lock()
		pending.result = cloneEnvelope(result)
		pending.complete = true
		delete(d.replays, replayKey)
		close(pending.done)
		d.replayMu.Unlock()
		return result
	}
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
	case "edit_apply", "change_plan", "verify_run", "language_server_setup", "debug_session", "debug_breakpoints", "debug_control":
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

func normalizeToolTimeout(ctx context.Context, timeout time.Duration, name string, result map[string]any) bool {
	if !errors.Is(context.Cause(ctx), errGlobalToolCallTimeout) {
		return false
	}
	stateful := isStatefulModernTool(name)
	result["outcome"] = "failed"
	result["code"] = "request_timeout"
	result["summary"] = fmt.Sprintf("%s exceeded the global %s tool-call timeout; the operation was cancelled", name, timeout)
	result["data"] = map[string]any{
		"tool": name, "timeout_ms": timeout.Milliseconds(), "retry_safe": true,
	}
	if stateful {
		result["warnings"] = []string{"The timed-out operation completed cancellation and cleanup; retry with the same idempotency key."}
		result["next"] = []any{map[string]any{
			"tool": name, "action": "retry_after_timeout", "reuse_idempotency_key": true,
		}}
	} else {
		result["warnings"] = []string{"The timed-out read was cancelled; retry the request when the workspace is less busy."}
		result["next"] = []any{map[string]any{
			"tool": name, "action": "retry_after_timeout",
		}}
	}
	return true
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
		return d.open(ctx, requestID, arguments)
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
	case "language_server_status":
		return d.languageServerStatus(ctx, requestID, workspace)
	case "language_server_setup":
		return d.languageServerSetup(ctx, requestID, workspace, arguments)
	case "navigate":
		return d.navigateProvider(ctx, requestID, workspace, arguments)
	case "code_actions":
		return d.codeActionsProvider(ctx, requestID, workspace, arguments)
	case "workspace_inspect":
		if _, refreshErr := workspace.RefreshKnownDocuments(); refreshErr != nil {
			return modernFailure(requestID, workspace, "workspace_refresh_failed", refreshErr)
		}
		inspection := workspace.Inspect()
		var semanticProvider map[string]any
		if backend, providerErr := d.canonicalProvider(ctx, workspace); providerErr == nil {
			inspection.Optional["provider"] = "available"
			inspection.Optional["lsp"] = "probe_with_language_server_status"
			semanticProvider = canonicalProviderStatus(backend)
		}
		policy, policyErr := workspacecore.LoadPipelinePolicy(workspace.Identity().Root, "")
		if policyErr != nil {
			result := modernFailure(requestID, workspace, "workspace_policy_invalid", policyErr)
			result["next"] = []any{map[string]any{
				"tool": "workspace_inspect", "action": "repair_pipeline_configuration",
				"project_config": filepath.Join(workspace.Identity().Root, ".huyang.toml"),
			}}
			return result
		}
		view, _ := arguments["view"].(string)
		if view == "" {
			view = "status"
		}
		pipelineState := "not_configured"
		pipelineReason := "No project .huyang.toml is present; only built-in parser checks are available."
		if policy.ProjectConfig != "" && policy.Trusted {
			pipelineState = "configured_trusted"
			pipelineReason = "Project commands are configured and this workspace root is trusted."
		} else if policy.ProjectConfig != "" {
			pipelineState = "configured_untrusted"
			pipelineReason = "Project commands are configured but disabled until this workspace root is trusted in the user policy."
		}
		base := map[string]any{
			"view": view, "revision": fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
			"service_limits": map[string]any{"tool_call_timeout_ms": d.toolTimeout.Milliseconds()},
			"inspection":     inspection, "semantic_provider": semanticProvider, "scheduler": d.scheduler.description(), "pipeline_policy": policy,
			"pipeline_state": map[string]any{
				"state": pipelineState, "configured": policy.ProjectConfig != "", "trusted": policy.Trusted,
				"reason": pipelineReason, "project_config": policy.ProjectConfig, "user_config": policy.UserConfig,
			},
		}
		summary := "Workspace inspection is current"
		if view == "overview" || view == "map" {
			orientation, err := workspace.Orient()
			if err != nil {
				return modernFailure(requestID, workspace, "workspace_map_failed", err)
			}
			base["overview"] = orientation
			summary = fmt.Sprintf("%d workspace entries", len(orientation.Entries))
		}
		result := modernEnvelope(requestID, workspace, "ok", "", summary, base)
		if pipelineState == "configured_untrusted" {
			result["warnings"] = []string{pipelineReason}
			result["next"] = []any{map[string]any{
				"tool": "workspace_inspect", "action": "trust_workspace_root",
				"root": workspace.Identity().Root, "user_config": policy.UserConfig,
			}}
		}
		return result
	case "search":
		return d.search(requestID, workspace, arguments)
	case "symbol_find":
		return d.symbolFind(ctx, requestID, workspace, arguments)
	case "read":
		return d.read(requestID, workspace, arguments)
	case "diagnostics":
		since, _ := arguments["since"].(string)
		report, err := workspace.Diagnostics(since)
		if err != nil {
			return modernFailure(requestID, workspace, "diagnostic_cursor_invalid", err)
		}
		outcome := "ok"
		summary := "Diagnostic evidence retrieved from the durable workspace inbox"
		if report.Confidence == workspacecore.ConfidenceProvisional {
			outcome = "provisional"
			summary = "Only provisional diagnostic evidence is available"
		} else if report.Confidence == workspacecore.ConfidenceUnavailable {
			outcome = "unavailable"
			summary = "Diagnostic evidence is unavailable for this workspace"
		}
		result := modernEnvelope(requestID, workspace, outcome, "", summary, map[string]any{"diagnostics": report})
		result["evidence"] = map[string]any{"ids": nonNilStrings(report.EvidenceIDs), "truncated": false}
		if outcome == "unavailable" {
			result["next"] = []any{
				map[string]any{"tool": "workspace_inspect", "action": "inspect_provider_and_pipeline_status", "view": "status"},
				map[string]any{"tool": "verify_run", "action": "run_configured_diagnostics_for_exact_revision"},
			}
		}
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
		return d.edit(ctx, requestID, workspace, arguments)
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

func (d *directWorkspaces) open(ctx context.Context, requestID string, arguments map[string]any) map[string]any {
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
	var canonicalBackend provider.Provider
	if opened.Identity().Kind == workspacecore.KindProject && shippedRuntimePath() != "" {
		canonicalBackend, _ = d.canonicalProvider(ctx, opened)
	}
	orientation, err := opened.Orient()
	if err != nil {
		return modernFailure(requestID, opened, "workspace_overview_failed", err)
	}
	recent, recentErr := opened.RecentCommits(3)
	if recentErr != nil {
		recent = workspacecore.CommitList{Coverage: workspacecore.GitCoverage{Complete: false, Unavailable: []string{"git_history"}}}
	}
	capabilities := opened.Inspect()
	var semanticProvider map[string]any
	if canonicalBackend != nil {
		capabilities.Optional["provider"] = "available"
		capabilities.Optional["lsp"] = "probe_with_language_server_status"
		semanticProvider = canonicalProviderStatus(canonicalBackend)
	}
	action := "Opened"
	if !created {
		action = "Reopened"
	}
	return modernEnvelope(requestID, opened, "ok", "", fmt.Sprintf("%s %s workspace with %d entries", action, kind, len(orientation.Entries)), map[string]any{
		"revision":     fmt.Sprintf("wsrev_%d", opened.Identity().StateSeq),
		"capabilities": capabilities, "semantic_provider": semanticProvider,
		"service_limits": map[string]any{"tool_call_timeout_ms": d.toolTimeout.Milliseconds()},
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
		noun := "matches"
		if len(result.Hits) == 1 {
			noun = "match"
		}
		return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d historical %s", len(result.Hits), noun), result)
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
		code := "search_failed"
		failure := modernFailure(requestID, workspace, code, err)
		if mode == workspacecore.SearchRegex {
			failure["code"] = "invalid_regex"
			failure["summary"] = "The regular expression is invalid: " + err.Error()
			failure["next"] = []any{map[string]any{
				"tool": "search", "action": "retry_as_literal", "query": query, "mode": "literal",
			}}
		}
		return failure
	}
	limit := argInt(arguments, "limit", 50)
	hits, truncated := compactSearchHits(result.Hits, limit)
	summary := fmt.Sprintf("%d matches", len(result.Hits))
	if len(result.Hits) == 1 {
		summary = "1 match"
	}
	if !result.Coverage.Complete {
		summary += "; search coverage incomplete"
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

func providerLineByteRange(content []byte, lines string) (int, int, error) {
	parts := strings.SplitN(lines, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid provider line range %q", lines)
	}
	first, err := strconv.Atoi(parts[0])
	if err != nil || first < 1 {
		return 0, 0, fmt.Errorf("invalid provider start line %q", lines)
	}
	last, err := strconv.Atoi(parts[1])
	if err != nil || last < first {
		return 0, 0, fmt.Errorf("invalid provider end line %q", lines)
	}
	start, end, line := 0, len(content), 1
	for index, value := range content {
		if value != '\n' {
			continue
		}
		if line < first {
			start = index + 1
		}
		if line == last {
			end = index + 1
			break
		}
		line++
	}
	if line < first || start >= len(content) {
		return 0, 0, fmt.Errorf("provider line range %q exceeds document", lines)
	}
	return start, end, nil
}

func (d *directWorkspaces) symbolFind(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	query, _ := arguments["query"].(string)
	records, coverage, err := workspace.FindSymbols(query)
	if err != nil {
		return modernFailure(requestID, workspace, "symbol_find_failed", err)
	}
	providerEvidence := any(nil)
	providerWarning := ""
	if !coverage.Complete {
		backend, providerErr := d.canonicalProvider(ctx, workspace)
		if providerErr == nil {
			providerEvidence, providerErr = callCanonicalProvider(ctx, requestID, workspace, backend, "find_symbol", map[string]any{
				"root": workspace.Identity().Root, "name": query, "include_body": false,
			})
		}
		if providerErr != nil {
			providerWarning = "Embedded semantic provider fallback failed: " + providerErr.Error()
		} else {
			value, _ := providerEvidence.(map[string]any)
			providerRecords := make([]workspacecore.HandleRecord, 0, len(anySlice(value["matches"])))
			for _, raw := range anySlice(value["matches"]) {
				match, _ := raw.(map[string]any)
				path, name, kind := fmt.Sprint(match["file"]), fmt.Sprint(match["name_path"]), fmt.Sprint(match["kind"])
				read, readErr := workspace.Read(path)
				if readErr != nil {
					providerWarning = "Some embedded semantic provider matches could not be registered: " + readErr.Error()
					continue
				}
				start, end, rangeErr := providerLineByteRange(read.Content, fmt.Sprint(match["lines"]))
				if rangeErr != nil {
					providerWarning = "Some embedded semantic provider matches could not be registered: " + rangeErr.Error()
					continue
				}
				record, registerErr := workspace.RegisterSymbolHandle(path, name, kind, start, end)
				if registerErr != nil {
					providerWarning = "Some embedded semantic provider matches could not be registered: " + registerErr.Error()
					continue
				}
				providerRecords = append(providerRecords, record)
			}
			records = providerRecords
			coverage = workspacecore.Coverage{Complete: providerWarning == "", Semantic: "embedded_nvim"}
		}
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
	outcome, code := "ok", ""
	if !coverage.Complete {
		outcome, code = "partial", "semantic_coverage_partial"
	}
	summary := fmt.Sprintf("%d symbols found", len(records))
	if len(records) == 0 && !coverage.Complete {
		summary = "Symbol search could not establish results because semantic coverage is incomplete"
	}
	data := map[string]any{"ranked_handles": items, "coverage": coverage}
	if providerEvidence != nil {
		data["provider_evidence"] = providerEvidence
	}
	result := modernEnvelope(requestID, workspace, outcome, code, summary, data)
	if providerWarning != "" {
		result["warnings"] = []string{providerWarning}
	}
	if !coverage.Complete {
		result["next"] = []any{
			map[string]any{"tool": "language_server_status", "action": "inspect_attachment"},
			map[string]any{"tool": "search", "action": "literal_fallback", "query": query},
		}
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
			return modernHandleConflict(requestID, workspace, resolution)
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
			name, _ := symbol["name_path"].(string)
			matches, coverage, findErr := workspace.FindSymbols(name)
			if findErr != nil {
				return modernFailure(requestID, workspace, "symbol_read_failed", findErr)
			}
			var exact []workspacecore.HandleRecord
			for _, match := range matches {
				if match.Locator.Path == path && match.Locator.NamePath == name {
					exact = append(exact, match)
				}
			}
			if len(exact) != 1 {
				outcome, code, summary := "conflict", "symbol_not_found", "Symbol locator did not resolve uniquely"
				if !coverage.Complete {
					outcome, code, summary = "unavailable", "semantic_provider_unavailable", "Symbol read requires parser coverage that is unavailable"
				}
				result := modernEnvelope(requestID, workspace, outcome, code, summary, map[string]any{"coverage": coverage, "matches": exact})
				result["next"] = []any{map[string]any{"tool": "search", "action": "literal_fallback", "query": name, "path": path}, map[string]any{"tool": "read", "action": "read_known_path", "path": path}}
				return result
			}
			resolved, resolveErr := workspace.ResolveHandle(exact[0].Handle)
			if resolveErr != nil || resolved.Current == nil {
				if resolveErr != nil {
					return modernFailure(requestID, workspace, "symbol_read_failed", resolveErr)
				}
				return modernHandleConflict(requestID, workspace, resolved)
			}
			read, readErr := workspace.Read(path)
			if readErr != nil {
				return modernFailure(requestID, workspace, "read_failed", readErr)
			}
			current := resolved.Current
			return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("Read symbol %s", name), map[string]any{
				"path": path, "content": string(read.Content[current.ByteStart:current.ByteEnd]), "snapshot": read.Snapshot,
				"coverage": coverage, "resolution": resolved,
			})
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

func modernHandleConflict(requestID string, workspace *workspacecore.Workspace, resolution workspacecore.HandleResolution) map[string]any {
	summary := "The revision-bound target changed and must be refreshed"
	switch resolution.Code {
	case workspacecore.ConflictTargetDeleted:
		summary = "The revision-bound target no longer exists"
	case workspacecore.ConflictSymbolAmbiguous:
		summary = "The revision-bound target now resolves to multiple candidates"
	case workspacecore.ConflictSymbolSignatureChanged:
		summary = "The target symbol signature changed"
	case workspacecore.ConflictDocumentChanged:
		summary = "The target document changed since this handle was issued"
	}
	result := modernEnvelope(requestID, workspace, "conflict", string(resolution.Code), summary, resolution)
	path := resolution.Original.Path
	next := []any{}
	if resolution.Code != workspacecore.ConflictTargetDeleted {
		next = append(next, map[string]any{"tool": "read", "action": "refresh_path", "path": path})
	} else if destination, ok := workspace.RecentRenameDestination(path); ok {
		result["summary"] = fmt.Sprintf("The revision-bound target moved from %s to %s", path, destination)
		next = append(next, map[string]any{
			"tool": "read", "action": "recover_at_detected_rename",
			"path": destination, "source_path": path,
		})
	} else {
		next = append(next, map[string]any{
			"tool": "search", "action": "inspect_git_rename_history",
			"git_history": map[string]any{"query": path, "fields": []string{"path", "diff"}},
		})
	}
	if resolution.Original.NamePath != "" {
		next = append(next, map[string]any{"tool": "symbol_find", "action": "relocate_target", "query": resolution.Original.NamePath})
	}
	result["next"] = next
	return result
}

func (d *directWorkspaces) edit(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
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
			return modernHandleConflict(requestID, workspace, resolved)
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
			result := modernEnvelope(requestID, workspace, "conflict", string(conflict.Code), conflict.Error(), map[string]any{
				"target": handle, "current_revision": fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
			})
			result["next"] = []any{map[string]any{"tool": "read", "action": "refresh_path", "path": handle.Path}, map[string]any{"tool": "search", "action": "relocate_target", "path": handle.Path}}
			return result
		}
		return modernFailure(requestID, workspace, "edit_failed", err)
	}
	summary, outcome := "Guarded range preview is ready; canonical bytes unchanged", "ok"
	data := map[string]any{
		"change": compactTextChange(change), "resolution": resolution, "tool_delta": []any{},
		"diagnostic_delta": map[string]any{"new": []any{}, "resolved": []any{}},
		"from_revision":    fmt.Sprintf("wsrev_%d", change.Before.Workspace.StateSeq),
		"changed_paths":    []string{change.Diff.Path}, "canonical_changed": !preview,
	}
	evidenceIDs := make([]string, 0)
	diagnosticRecovery := false
	if !preview {
		revision := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
		data["document_revision"] = after.Revision
		verification := map[string]any{"confidence": "unavailable", "reasons": []string{"semantic_provider_unavailable"}}
		backend, providerErr := d.canonicalProvider(ctx, workspace)
		if providerErr == nil {
			report, evidenceErr := recordProviderDiagnostics(ctx, workspace, backend, []workspacecore.PlanStageFile{{
				Path: change.Diff.Path, Before: change.Diff.Before, After: change.Diff.After,
				BeforeExists: true, AfterExists: true,
			}}, revision, "edit_"+requestID)
			if evidenceErr == nil {
				evidenceIDs = append(evidenceIDs, report.EvidenceIDs...)
				data["diagnostic_delta"] = map[string]any{"new": report.New, "resolved": report.Resolved}
				verification = map[string]any{
					"confidence": report.Confidence,
					"reasons":    nonNilStrings(report.ProvisionalReasons),
				}
				if report.Confidence == workspacecore.ConfidenceAuthoritative || report.Confidence == workspacecore.ConfidenceCorroborated {
					outcome = "ok"
					summary = "Guarded range edit applied; current semantic diagnostics captured"
				} else {
					outcome = "provisional"
					summary = "Guarded range edit applied; semantic diagnostics remain incomplete"
					diagnosticRecovery = true
				}
			} else {
				providerErr = evidenceErr
			}
		}
		if providerErr != nil {
			outcome = "provisional"
			summary = "Guarded range edit applied; semantic diagnostic refresh failed"
			verification = map[string]any{"confidence": "unavailable", "reasons": []string{providerErr.Error()}}
			diagnosticRecovery = true
		}
		data["verification"] = verification
	}
	data["revision"] = fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	if resolution != nil && resolution.Status == workspacecore.ResolutionRelocated {
		if preview {
			summary = "Guarded range preview is ready after safely relocating the stale target; canonical bytes unchanged"
		} else if outcome == "ok" {
			summary = "Guarded range edit applied after safely relocating the stale target; current semantic diagnostics captured"
		} else {
			summary = "Guarded range edit applied after safely relocating the stale target; semantic diagnostics remain incomplete"
		}
	}
	result := modernEnvelope(requestID, workspace, outcome, "", summary, data)
	result["evidence"] = map[string]any{"ids": nonNilStrings(evidenceIDs), "truncated": false}
	if diagnosticRecovery {
		result["next"] = []any{
			map[string]any{"tool": "language_server_status", "action": "inspect_attachment_and_install_options"},
			map[string]any{"tool": "verify_run", "action": "retry_diagnostics_for_exact_revision", "revision_or_transaction": data["revision"]},
		}
	}
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
	recorded := d.recordedRevisionDiffs(string(workspace.Identity().ID), fromSeq, toSeq)
	sort.Slice(recorded, func(i, j int) bool {
		if recorded[i].from != recorded[j].from {
			return recorded[i].from < recorded[j].from
		}
		if recorded[i].to != recorded[j].to {
			return recorded[i].to < recorded[j].to
		}
		return recorded[i].path < recorded[j].path
	})
	known := make([]recordedRevisionDiff, 0, len(recorded))
	segments := make([]any, 0, len(recorded))
	gaps := make([]any, 0)
	cursor := fromSeq
	for _, item := range recorded {
		sameRevision := len(known) > 0 &&
			known[len(known)-1].from == item.from &&
			known[len(known)-1].to == item.to
		if item.to <= cursor {
			if !sameRevision {
				continue
			}
		} else if item.from < cursor {
			continue
		}
		if item.from > cursor {
			gaps = append(gaps, map[string]any{
				"from_revision": fmt.Sprintf("wsrev_%d", cursor),
				"to_revision":   fmt.Sprintf("wsrev_%d", item.from),
			})
		}
		known = append(known, item)
		segments = append(segments, map[string]any{
			"from_revision": fmt.Sprintf("wsrev_%d", item.from),
			"to_revision":   fmt.Sprintf("wsrev_%d", item.to),
			"path":          item.path,
		})
		if item.to > cursor {
			cursor = item.to
		}
	}
	if cursor < toSeq {
		gaps = append(gaps, map[string]any{
			"from_revision": fmt.Sprintf("wsrev_%d", cursor),
			"to_revision":   fmt.Sprintf("wsrev_%d", toSeq),
		})
	}
	if len(gaps) > 0 {
		diffs := make([]any, 0, len(known))
		paths := make([]string, 0, len(known))
		for _, item := range known {
			diffs = append(diffs, compactRevisionDiff(item.diff))
			if item.path != "" {
				paths = append(paths, item.path)
			}
		}
		sort.Strings(paths)
		paths = uniqueStrings(paths)
		result := modernEnvelope(requestID, workspace, "partial", "diff_evidence_incomplete", "Known native edit segments are returned with explicit uncovered revision gaps", map[string]any{
			"from_revision": from, "to_revision": to, "current_revision": current,
			"known_segments": segments, "gaps": gaps, "diffs": diffs,
		})
		result["warnings"] = []string{"Uncovered gaps may contain external writes, provider edits, or expired receipts; no diff is inferred for them."}
		next := []any{map[string]any{"tool": "workspace_inspect", "action": "record_current_revision_as_new_baseline", "view": "status"}}
		if len(paths) > 0 {
			next = append([]any{map[string]any{"tool": "read", "action": "inspect_known_changed_paths", "paths": paths}}, next...)
		}
		result["next"] = next
		return result
	}
	type endpoints struct{ before, after string }
	byPath := map[string]endpoints{}
	for _, item := range known {
		ends, exists := byPath[item.path]
		if !exists {
			ends.before = item.beforeSHA
		}
		ends.after = item.afterSHA
		byPath[item.path] = ends
	}
	diffs := make([]any, 0, len(known))
	for _, item := range known {
		ends := byPath[item.path]
		if ends.before != "" && ends.before == ends.after {
			continue
		}
		diffs = append(diffs, compactRevisionDiff(item.diff))
	}
	netChangedPaths := 0
	for _, ends := range byPath {
		if ends.before == "" || ends.before != ends.after {
			netChangedPaths++
		}
	}
	summary := fmt.Sprintf("%d native edit events cover %d net-changed paths between %s and %s", len(diffs), netChangedPaths, from, to)
	return modernEnvelope(requestID, workspace, "ok", "", summary, map[string]any{
		"from_revision": from, "to_revision": to, "current_revision": current, "diffs": diffs,
		"semantics": "net endpoint identity with ordered edit evidence",
	})
}

// compactRevisionDiff keeps the default revision history response bounded by
// omitting complete endpoint bodies. The patch and endpoint hashes retain exact,
// independently checkable evidence without JSON's base64 expansion of []byte.
func compactRevisionDiff(value any) any {
	switch diff := value.(type) {
	case workspacecore.ExactDiff:
		return map[string]any{"path": diff.Path, "before_sha256": diff.BeforeSHA256, "after_sha256": diff.AfterSHA256, "patch": diff.Patch}
	case map[string]any:
		return map[string]any{"path": diff["path"], "before_sha256": diff["before_sha256"], "after_sha256": diff["after_sha256"], "patch": diff["patch"]}
	default:
		return map[string]any{"detail": "diff receipt unavailable", "value_type": fmt.Sprintf("%T", value)}
	}
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
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

const (
	modernVerificationListLimit   = 20
	modernVerificationOutputLimit = 4096
)

func boundedVerificationStrings(values []string) ([]string, bool) {
	if len(values) <= modernVerificationListLimit {
		return nonNilStrings(values), false
	}
	return append([]string(nil), values[:modernVerificationListLimit]...), true
}

func compactVerificationImpact(graph *workspacecore.ImpactGraph) (map[string]any, bool) {
	if graph == nil {
		return nil, false
	}
	changed, changedTruncated := boundedVerificationStrings(graph.Changed)
	affected, affectedTruncated := boundedVerificationStrings(graph.Affected)
	untested, untestedTruncated := boundedVerificationStrings(graph.Untested)
	riskKinds := map[string]int{}
	for _, risk := range graph.Risks {
		riskKinds[risk.Kind]++
	}
	truncated := changedTruncated || affectedTruncated || untestedTruncated ||
		len(graph.Nodes) > 0 || len(graph.Edges) > 0 || len(graph.Risks) > 0
	return map[string]any{
		"revision": graph.Revision,
		"changed":  changed, "changed_count": len(graph.Changed), "changed_truncated": changedTruncated,
		"affected": affected, "affected_count": len(graph.Affected), "affected_truncated": affectedTruncated,
		"affected_without_tests": untested, "affected_without_tests_count": len(graph.Untested),
		"affected_without_tests_truncated": untestedTruncated,
		"node_count":                       len(graph.Nodes), "edge_count": len(graph.Edges),
		"risk_count": len(graph.Risks), "risk_kind_counts": riskKinds,
		"coverage": graph.Coverage, "adapters": nonNilStrings(graph.Adapters),
		"variants_included": nonNilStrings(graph.Included), "variants_omitted": nonNilStrings(graph.Omitted),
		"recommend_full": graph.RecommendFull, "details_truncated": truncated,
	}, truncated
}

func compactVerificationResult(result workspacecore.VerificationResult) (map[string]any, []string) {
	compacted := map[string]any{"revision": result.Revision}
	var evidenceIDs []string
	detailsTruncated := false
	stages := make([]any, 0, len(result.Stages))
	for _, stage := range result.Stages {
		output := stage.Output
		outputTruncated := len(output) > modernVerificationOutputLimit
		if outputTruncated {
			output = output[:modernVerificationOutputLimit]
			detailsTruncated = true
		}
		scope, scopeTruncated := boundedVerificationStrings(stage.Scope)
		writes, writesTruncated := boundedVerificationStrings(stage.Writes)
		executed, executedTruncated := boundedVerificationStrings(stage.ExecutedTests)
		selected := make([]any, 0, min(len(stage.SelectedTests), modernVerificationListLimit))
		for _, test := range stage.SelectedTests[:min(len(stage.SelectedTests), modernVerificationListLimit)] {
			selected = append(selected, map[string]any{
				"name": test.Name, "reasons": nonNilStrings(test.Reasons), "variants": nonNilStrings(test.Variants),
			})
		}
		selectedTruncated := len(stage.SelectedTests) > modernVerificationListLimit
		detailsTruncated = detailsTruncated || scopeTruncated || writesTruncated || executedTruncated || selectedTruncated
		stageData := map[string]any{
			"stage": stage.Stage, "mode": stage.Mode, "started_revision": stage.StartedRevision,
			"exit": stage.Exit, "status": stage.Status, "duration_ms": stage.DurationMS,
			"coverage": stage.Coverage, "evidence_ids": nonNilStrings(stage.EvidenceIDs),
			"scope": scope, "scope_count": len(stage.Scope), "scope_truncated": scopeTruncated,
			"writes": writes, "writes_count": len(stage.Writes), "writes_truncated": writesTruncated,
			"output": output, "output_bytes": len(stage.Output), "output_truncated": outputTruncated,
			"test_scope": stage.TestScope, "test_verdict": stage.TestVerdict,
			"selected_tests": selected, "selected_test_count": len(stage.SelectedTests),
			"selected_tests_truncated": selectedTruncated,
			"executed_tests":           executed, "executed_test_count": len(stage.ExecutedTests),
			"executed_tests_truncated": executedTruncated,
		}
		stages = append(stages, stageData)
		evidenceIDs = append(evidenceIDs, stage.EvidenceIDs...)
	}
	compacted["stages"] = stages
	compacted["stage_count"] = len(result.Stages)
	if impact, truncated := compactVerificationImpact(result.Impact); impact != nil {
		compacted["impact"] = impact
		detailsTruncated = detailsTruncated || truncated
	}
	if result.Targeted != nil {
		selected := make([]any, 0, min(len(result.Targeted.Selected), modernVerificationListLimit))
		for _, test := range result.Targeted.Selected[:min(len(result.Targeted.Selected), modernVerificationListLimit)] {
			selected = append(selected, map[string]any{
				"name": test.Name, "reasons": nonNilStrings(test.Reasons), "variants": nonNilStrings(test.Variants),
			})
		}
		executed, executedTruncated := boundedVerificationStrings(result.Targeted.Executed)
		selectedTruncated := len(result.Targeted.Selected) > modernVerificationListLimit
		compacted["targeted_tests"] = map[string]any{
			"status": result.Targeted.Status, "selected": selected,
			"selected_count": len(result.Targeted.Selected), "selected_truncated": selectedTruncated,
			"executed": executed, "executed_count": len(result.Targeted.Executed),
			"executed_truncated": executedTruncated,
			"graph_revision":     result.Targeted.Graph.Revision,
		}
		detailsTruncated = detailsTruncated || selectedTruncated || executedTruncated || len(result.Targeted.Graph.Nodes) > 0 ||
			len(result.Targeted.Graph.Edges) > 0 || len(result.Targeted.Graph.Risks) > 0
	}
	if len(result.ToolDelta) > 0 {
		deltas := make([]any, 0, min(len(result.ToolDelta), modernVerificationListLimit))
		for _, delta := range result.ToolDelta[:min(len(result.ToolDelta), modernVerificationListLimit)] {
			deltas = append(deltas, map[string]any{
				"path": delta.Path, "classification": delta.Classification,
				"before_exists": delta.BeforeExists, "after_exists": delta.AfterExists,
				"before_kind": delta.BeforeKind, "after_kind": delta.AfterKind,
				"before_mode": delta.BeforeMode, "after_mode": delta.AfterMode,
				"before_target": delta.BeforeTarget, "after_target": delta.AfterTarget,
				"before_bytes": len(delta.Before), "after_bytes": len(delta.After),
			})
		}
		compacted["tool_delta"] = deltas
		compacted["tool_delta_count"] = len(result.ToolDelta)
		compacted["tool_delta_truncated"] = len(result.ToolDelta) > modernVerificationListLimit
		detailsTruncated = true
	}
	if result.FullTestGate != "" {
		compacted["full_test_gate"] = result.FullTestGate
	}
	compacted["details_truncated"] = detailsTruncated
	sort.Strings(evidenceIDs)
	return compacted, nonNilStrings(uniqueStrings(evidenceIDs))
}

func fullVerificationFallback(result workspacecore.VerificationResult) map[string]any {
	if result.Impact == nil || !result.Impact.RecommendFull || result.Targeted == nil || result.Targeted.Status != "unavailable" {
		return nil
	}
	stages := make([]string, 0, len(result.Stages))
	seen := map[string]bool{}
	for _, stage := range result.Stages {
		if stage.Stage != "" && !seen[stage.Stage] {
			stages = append(stages, stage.Stage)
			seen[stage.Stage] = true
		}
	}
	return map[string]any{
		"tool":                    "verify_run",
		"action":                  "run_full_verification",
		"revision_or_transaction": result.Revision,
		"stages":                  stages,
		"test_scope":              "full",
		"use_new_idempotency_key": true,
	}
}

func modernVerificationEnvelope(requestID string, workspace *workspacecore.Workspace, outcome, code, summary, cache string, result workspacecore.VerificationResult) map[string]any {
	compacted, evidenceIDs := compactVerificationResult(result)
	envelope := modernEnvelope(requestID, workspace, outcome, code, summary, map[string]any{
		"verification": compacted, "cache": cache,
	})
	envelope["evidence"] = map[string]any{"ids": evidenceIDs, "truncated": false}
	next := make([]any, 0, len(evidenceIDs)+1)
	if fallback := fullVerificationFallback(result); fallback != nil {
		next = append(next, fallback)
	}
	for _, id := range evidenceIDs {
		next = append(next, map[string]any{"tool": "evidence_get", "action": "inspect_verification_evidence", "evidence_id": id})
	}
	if len(next) > 0 {
		envelope["next"] = next
	}
	return envelope
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
		data := map[string]any{"plan": plan}
		if plan.State == workspacecore.PlanCommitted && plan.Preparation != nil {
			fromRevision := plan.Preparation.CanonicalFromRevision
			if fromRevision == "" {
				fromRevision = plan.Preparation.BaseRevision
			}
			data["from_revision"] = fromRevision
			data["revision"] = plan.Preparation.CanonicalRevision
			data["changed_paths"] = append([]string(nil), plan.Preparation.AffectedFiles...)
			data["canonical_changed"] = plan.Preparation.CanonicalChanged
		}
		result := modernEnvelope(requestID, workspace, "ok", "", summary, data)
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
		if err != nil && planID != "" {
			if failed, inspectErr := workspace.InspectPlan(planID, revision); inspectErr == nil {
				plan = failed
			}
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
				if current.State == workspacecore.PlanFailed && strings.Contains(stagerErr.Error(), "prepared sandbox is not available") {
					plan, err = workspace.DiscardPlan(planID, revision)
				} else {
					err = stagerErr
				}
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
		wasProvisional := false
		if current, inspectErr := workspace.InspectPlan(planID, revision); inspectErr == nil {
			wasProvisional = current.State == workspacecore.PlanProvisional
		}
		var stager workspacecore.PlanStager
		stager, err = d.planStager(workspace, planID, revision, false)
		if err == nil {
			plan, err = workspace.CommitPlan(ctx, planID, revision, preparedRevision, stager)
		}
		if err == nil {
			result := planResult("Prepared plan applied through the durable commit journal; canonical provider resynced", plan)
			if wasProvisional {
				result["outcome"] = "provisional"
				result["summary"] = "Prepared plan applied with explicitly accepted incomplete diagnostic evidence; canonical provider resynced"
				result["warnings"] = append(result["warnings"].([]string), "The plan was PROVISIONAL because diagnostic evidence was incomplete; the exact prepared revision was explicitly accepted.")
				result["data"].(map[string]any)["applied_from_provisional"] = true
			}
			return result
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
		result := modernEnvelope(requestID, workspace, outcome, code, err.Error(), data)
		if action == "prepare" && plan.PlanID != "" {
			result["next"] = []any{
				map[string]any{"tool": "change_plan", "action": "inspect", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision},
				map[string]any{"tool": "change_plan", "action": "discard", "plan_id": plan.PlanID, "plan_revision": plan.PlanRevision},
			}
		}
		return result
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
		return modernVerificationEnvelope(requestID, workspace, cached.Outcome, "", "Verification reused for the exact revision and stage selection", "revision_hit", cached.Result)
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
			var changedPaths []string
			changedPaths, provenanceErr := d.canonicalChangedPaths(identity.ID, identity.StateSeq)
			if provenanceErr != nil && testScope != "affected" {
				for _, file := range files {
					changedPaths = append(changedPaths, file.Path)
				}
			} else {
				filesErr = provenanceErr
			}
			if filesErr == nil {
				changed := make(map[string]bool, len(changedPaths))
				for _, path := range changedPaths {
					changed[path] = true
				}
				filtered := files[:0]
				for _, file := range files {
					if changed[file.Path] {
						filtered = append(filtered, file)
					}
				}
				files = filtered
			}
		}
		if filesErr == nil {
			var policy workspacecore.PipelinePolicy
			policy, filesErr = workspacecore.LoadPipelinePolicy(identity.Root, "")
			var diagnosticProvider provider.Provider
			providerOpened := false
			wantsDiagnostics := false
			for _, stage := range stages {
				if stage == "diagnostics" {
					wantsDiagnostics = true
					break
				}
			}

			if filesErr == nil && wantsDiagnostics {
				var providerErr error
				diagnosticProvider, providerErr = referenceProviders.Open(providerOpenConfig{
					Root: sandbox.Tree, InitFile: huyangHeadlessInit(),
					RuntimePath: shippedRuntimePath(), Debug: false,
				})
				providerOpened = providerErr == nil
				if providerErr == nil {
					_, providerErr = callCanonicalProvider(ctx, "workspace_support_"+requestID, workspace, diagnosticProvider,
						"workspace_support", map[string]any{"root": sandbox.Tree, "attach_wait_ms": verificationProviderAttachWaitMS})
				}
				if providerErr == nil {

					request.DiagnosticVerifier = func(verifyCtx context.Context, revision string, staged []workspacecore.PlanStageFile) (workspacecore.VerificationStage, error) {
						report, evidenceErr := recordProviderDiagnostics(verifyCtx, workspace, diagnosticProvider, staged, revision, "verify_"+requestID, verificationDiagnosticSettleWait)
						return diagnosticVerificationStage(revision, report), evidenceErr
					}
				}
			}
			if filesErr == nil {
				result, err = workspacecore.RunVerificationPipeline(ctx, sandbox, policy, request, files)
			}
			if providerOpened {
				if closeErr := diagnosticProvider.Close(context.Background()); err == nil && closeErr != nil {
					err = closeErr
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
		code, summary := "verification_failed", err.Error()
		resultEnvelope := modernVerificationEnvelope(requestID, workspace, "failed", code, summary, "", result)
		if timeoutCode, timeoutSummary, recovery, ok := verificationTimeoutRecovery(revision, stages, testScope, result); ok {
			resultEnvelope["code"] = timeoutCode
			resultEnvelope["summary"] = timeoutSummary
			next, _ := resultEnvelope["next"].([]any)
			resultEnvelope["next"] = append(next, recovery)
		}
		return resultEnvelope
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
	return modernVerificationEnvelope(requestID, workspace, outcome, "", "Verification completed against exact sandbox bytes", "revision_miss", result)
}

func (d *directWorkspaces) canonicalChangedPaths(workspaceID workspacecore.ID, target uint64) ([]string, error) {
	if target == 1 {
		return []string{}, nil
	}
	type receipt struct {
		from, to uint64
		path     string
	}
	d.replayMu.Lock()
	var receipts []receipt
	for key, replay := range d.replays {
		if !replay.complete || !strings.HasPrefix(key, string(workspaceID)+"\x00") {
			continue
		}
		data, _ := replay.result["data"].(map[string]any)
		if changed, _ := data["canonical_changed"].(bool); !changed {
			continue
		}
		from, fromErr := workspaceRevisionSequence(fmt.Sprint(data["from_revision"]))
		to, toErr := workspaceRevisionSequence(fmt.Sprint(data["revision"]))
		if fromErr != nil || toErr != nil || from+1 != to || to != target {
			continue
		}
		var changedPaths []string
		switch values := data["changed_paths"].(type) {
		case []string:
			changedPaths = append(changedPaths, values...)
		case []any:
			for _, value := range values {
				if path, ok := value.(string); ok {
					changedPaths = append(changedPaths, path)
				}
			}
		}
		if len(changedPaths) == 0 {
			change, _ := data["change"].(map[string]any)
			diff, _ := change["diff"].(map[string]any)
			if path, _ := diff["path"].(string); path != "" {
				changedPaths = append(changedPaths, path)
			}
		}
		for _, path := range changedPaths {
			if path != "" {
				receipts = append(receipts, receipt{from: from, to: to, path: path})
			}
		}
	}
	d.replayMu.Unlock()
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].from < receipts[j].from })
	paths := map[string]bool{}
	for _, item := range receipts {
		paths[item.path] = true
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("changed_file_evidence_incomplete: no native receipt covers wsrev_%d through wsrev_%d; run full verification or make a new native change", target-1, target)
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
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
	expected := map[mcpProfile]int{profileFull: 19, profileOrient: 8, profileEdit: 13, profileDebug: 12}
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
