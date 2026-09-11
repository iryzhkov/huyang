package mcpapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Profile string

const (
	ProfileFull   Profile = "full"
	ProfileOrient Profile = "orient"
	ProfileEdit   Profile = "edit"
	ProfileDebug  Profile = "debug"
)

type ToolDescriptor struct {
	Name        string
	Description string
	Profiles    []Profile
	InputSchema map[string]any
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	// Class is the scheduler class the handler runs under. It is declared next
	// to the schema so a tool cannot be registered without one; ClassForCall
	// refines it for argument-dependent behaviour.
	Class SchedulerClass
}

const (
	MaxToolArgumentBytes = 32 << 20
	MaxPlanOperations    = 8
	MaxPlanContentBytes  = 4 << 20
)

var ProfileOrder = []Profile{ProfileFull, ProfileOrient, ProfileEdit, ProfileDebug}

var modernProfileNames = map[Profile][]string{
	ProfileFull: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics",
		"code_actions", "edit_apply", "change_plan", "verify_run", "revision_diff", "evidence_get",
		"language_server_status", "language_server_setup", "debug_session", "debug_breakpoints", "debug_control", "debug_inspect",
	},
	ProfileOrient: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics", "evidence_get",
	},
	ProfileEdit: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics", "evidence_get",
		"code_actions", "edit_apply", "change_plan", "verify_run", "revision_diff",
	},
	ProfileDebug: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics", "evidence_get",
		"debug_session", "debug_breakpoints", "debug_control", "debug_inspect",
	},
}

var Tools = buildModernTools()

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
		"op_id":  stringSchema("Stable operation identifier unique within the plan."),
		"kind":   enumSchema(operationKinds...),
		"target": targetSchema(),
		"content": map[string]any{
			"type": "string", "maxLength": MaxPlanContentBytes,
			"description": "Exact UTF-8 content for the declared operation (maximum 4 MiB).",
		},
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
	operations := map[string]any{
		"type": "array", "items": operation, "maxItems": MaxPlanOperations,
		"description": "At most 8 operations per request. Build larger plans incrementally with action=edit and edit.mode=add.",
	}
	return schemaObject(map[string]any{
		"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
		"action":             enumSchema("create", "edit", "preview", "inspect", "prepare", "apply", "discard"),
		"operations":         operations,
		"plan_id":            stringSchema("Required after creation unless prepare supplies operations inline."),
		"plan_revision":      map[string]any{"type": "integer", "minimum": 1, "description": "Required with plan_id."},
		"prepared_revision":  stringSchema("Required when action=apply."),
		"accept_provisional": map[string]any{"type": "boolean", "description": "With action=apply, commit a PROVISIONAL plan by explicitly accepting its incomplete diagnostic evidence."},
		"edit": schemaObject(map[string]any{
			"mode": enumSchema("add", "update", "remove", "reorder", "replace_all"), "operations": operations,
			"op_ids": map[string]any{"type": "array", "items": stringSchema("Operation identifier.")},
		}, "mode"),
	}, "workspace_id", "idempotency_key", "action")
}

func buildModernTools() []ToolDescriptor {
	orient := []Profile{ProfileFull, ProfileOrient, ProfileEdit, ProfileDebug}
	edit := []Profile{ProfileFull, ProfileEdit}
	debug := []Profile{ProfileFull, ProfileDebug}
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
		"include_ranges": map[string]any{"type": "boolean", "description": "Return exact byte anchors (range, byte offsets) on every hit; the default hit carries only the editable handle."},
	}
	searchSchema := schemaObject(searchProperties, "workspace_id")
	searchSchema["oneOf"] = []any{
		schemaObject(searchProperties, "workspace_id", "query"),
		schemaObject(searchProperties, "workspace_id", "result_set_handle", "refine"),
		schemaObject(searchProperties, "workspace_id", "git_history"),
	}
	return []ToolDescriptor{
		{Class: ClassProviderRead, Name: "workspace_open", Description: "Open a project or exact document allowlist and return its revision, capabilities, compact overview, bounded local commits, and limits.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"kind":     enumSchema("project", "documents"),
			"root":     stringSchema("Project root; required for kind=project."),
			"files":    map[string]any{"type": "array", "items": stringSchema("Allowlisted document."), "minItems": 1},
			"overview": enumSchema("compact", "full"),
		}, "kind")},
		{Class: ClassProviderRead, Name: "workspace_inspect", Description: "Inspect revision, provider health, semantic coverage, pipeline availability, and limits without mutation.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "view": enumSchema("status", "overview", "map"),
		}, "workspace_id")},
		{Class: ClassPureRead, Name: "search", Description: "Search current source, monotonically refine a frozen set, or search bounded local Git history without mutation.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: searchSchema},
		{Class: ClassProviderRead, Name: "symbol_find", Description: "Find declarations and return ranked revision-bound handles when a semantic provider is available.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "query": stringSchema("Declaration name or path."), "include_source": map[string]any{"type": "boolean"},
		}, "workspace_id", "query")},
		{Class: ClassProviderRead, Name: "navigate", Description: "Navigate one semantic relationship from a shared revision-bound target.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "relation": enumSchema("definition", "type_definition", "implementation", "references", "incoming_calls", "outgoing_calls", "hover"), "target": targetSchema(),
		}, "workspace_id", "relation", "target")},
		{Class: ClassPureRead, Name: "read", Description: "Read exact or line-bounded source by path or revision-bound handle. A path may implicitly open an exact one-document workspace and returns its workspace ID and revision for guarded follow-up edits.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "target": readTargetSchema(), "view": enumSchema("source", "outline", "history", "changes"),
			"start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		}, "target")},
		{Class: ClassProviderRead, Name: "language_server_status", Description: "Inspect the owned Neovim provider and probe language-server attachment for languages in this workspace.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(),
		}, "workspace_id")},
		{Class: ClassCanonicalWrite, Name: "language_server_setup", Description: "Explicitly install or restart a workspace language server through the owned Neovim provider.", Profiles: edit, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
			"action": enumSchema("install", "restart"), "language": stringSchema("Filetype or extension, required for install."),
			"server": stringSchema("Optional Mason package or lspconfig name; none installs only the parser."),
			"parser": map[string]any{"type": "boolean"},
		}, "workspace_id", "idempotency_key", "action")},
		{Class: ClassPureRead, Name: "diagnostics", Description: "Inspect normalized diagnostic evidence, confidence, coverage, and provenance.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "since": stringSchema("Optional diagnostic cursor."),
			"full": map[string]any{"type": "boolean", "description": "Return the complete report including finding bodies and per-dimension evidence IDs."},
		}, "workspace_id")},
		{Class: ClassProviderRead, Name: "code_actions", Description: "List revision-bound quick fixes or refactors without applying them.", Profiles: edit, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(readTarget, "workspace_id", "target")},
		{Class: ClassCanonicalWrite, Name: "edit_apply", Description: "Preview or apply exactly one guarded range replacement through the native workspace core.", Profiles: edit, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
			"preview_only": map[string]any{"type": "boolean"},
			"operation": schemaObject(map[string]any{
				"kind": enumSchema("replace_range"), "target": mutationRangeTargetSchema(), "content": stringSchema("Exact replacement bytes as UTF-8 text."),
			}, "kind", "target"),
		}, "workspace_id", "idempotency_key", "operation")},
		{Class: ClassSandboxWrite, Name: "change_plan", Description: "Create, edit, preview, prepare, inspect, apply, or discard one coherent multi-operation plan.", Profiles: edit, Destructive: true, InputSchema: changePlanSchema(stateful, operationKinds)},
		{Class: ClassExternalJob, Name: "verify_run", Description: "Run selected formatting, parser, diagnostic, check, or test stages against an exact revision.", Profiles: edit, Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "idempotency_key": stateful["idempotency_key"],
			"stages":                  map[string]any{"type": "array", "items": enumSchema("format_gate", "parser", "diagnostics", "check", "tests"), "minItems": 1},
			"revision_or_transaction": stringSchema("Canonical revision, prepared revision, or transaction ID."),
			"test_scope":              enumSchema("affected", "full"),
		}, "workspace_id", "idempotency_key", "stages", "revision_or_transaction")},
		{Class: ClassCanonicalWrite, Name: "revision_diff", Description: "Explain changes between two revisions or a stale mutation refusal.", Profiles: edit, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "from_revision": stringSchema("Earlier revision."), "to_revision_or_current": stringSchema("Later revision or current."),
		}, "workspace_id", "from_revision", "to_revision_or_current")},
		{Class: ClassPureRead, Name: "evidence_get", Description: "Page pending or final diff, diagnostic, command, or provenance evidence.", Profiles: orient, ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "evidence_id": stringSchema("Evidence identifier."), "cursor": stringSchema("Optional page cursor."),
		}, "workspace_id", "evidence_id")},
		modernDebugSessionTool(debug),
		modernDebugBreakpointsTool(debug),
		modernDebugControlTool(debug),
		modernDebugInspectTool(debug),
	}
}

func Catalog(profile Profile) []ToolDescriptor {
	byName := make(map[string]ToolDescriptor, len(Tools))
	for _, descriptor := range Tools {
		byName[descriptor.Name] = descriptor
	}
	names := modernProfileNames[profile]
	catalog := make([]ToolDescriptor, 0, len(names))
	for _, name := range names {
		if descriptor, ok := byName[name]; ok {
			catalog = append(catalog, descriptor)
		}
	}
	return catalog
}

func OutputEnvelopeSchema() map[string]any {
	return schemaObject(map[string]any{
		"api_version": map[string]any{"const": APIVersion},
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
		"diagnostic_updates_truncated": map[string]any{"type": "boolean"},
		"idempotency":                  enumSchema("created", "replayed"),
		"idempotency_persisted":        map[string]any{"type": "boolean"},
	}, "api_version", "request_id", "outcome", "summary", "data", "evidence", "warnings", "next")
}

func CatalogJSON(profile Profile) ([]byte, error) {
	catalog := Catalog(profile)
	tools := make([]map[string]any, 0, len(catalog))
	for _, descriptor := range catalog {
		tools = append(tools, map[string]any{
			"name": descriptor.Name, "description": descriptor.Description,
			"inputSchema": descriptor.InputSchema, "outputSchema": OutputEnvelopeSchema(),
		})
	}
	return json.Marshal(tools)
}

func CatalogNames(profile Profile) []string {
	catalog := Catalog(profile)
	names := make([]string, len(catalog))
	for index, descriptor := range catalog {
		names[index] = descriptor.Name
	}
	return names
}

func ValidateRegistry() error {
	seen := map[string]bool{}
	for _, descriptor := range Tools {
		if seen[descriptor.Name] {
			return fmt.Errorf("duplicate modern tool %q", descriptor.Name)
		}
		seen[descriptor.Name] = true
		if descriptor.InputSchema["additionalProperties"] != false {
			return fmt.Errorf("tool %s schema is not closed", descriptor.Name)
		}
		if !KnownClass(descriptor.Class) {
			return fmt.Errorf("tool %s declares no scheduler class", descriptor.Name)
		}
	}
	expected := map[Profile]int{ProfileFull: 19, ProfileOrient: 8, ProfileEdit: 13, ProfileDebug: 12}
	for _, profile := range ProfileOrder {
		if len(Catalog(profile)) != expected[profile] {
			return fmt.Errorf("profile %s has %d tools, want %d", profile, len(Catalog(profile)), expected[profile])
		}
	}
	return nil
}

func ProfileSummary() string {
	names := make([]string, 0, len(ProfileOrder))
	for _, profile := range ProfileOrder {
		names = append(names, fmt.Sprintf("%s=%d", profile, len(Catalog(profile))))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
