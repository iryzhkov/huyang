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
	ProfileEdit   Profile = "edit" // Alias of full for one release.
	ProfileLean   Profile = "lean"
	// ProfileExperimental is the frozen surface plus whatever input shape is
	// being designed. A caller opts into it knowingly and loses nothing by
	// doing so; frozen profiles never acquire an unfinished tool,
	// because their schemas are a contract with every agent that cached them.
	ProfileExperimental Profile = "experimental"
)

type ToolDescriptor struct {
	Name        string
	Description string
	InputSchema map[string]any
	// ProfileSchemas replace the entire input shape for a fixed profile.
	// The server validates calls against the selected schema before dispatch.
	ProfileSchemas map[Profile]map[string]any
	ReadOnly       bool
	Destructive    bool
	Idempotent     bool
	// Experimental marks a tool whose input shape is still being designed. It
	// is advertised in ProfileExperimental only, under APIVersionExperimental,
	// and may change without the deliberation the frozen catalog requires.
	Experimental bool
	// ExperimentalProperties are input properties a frozen tool grows only in
	// the experimental catalog. The frozen schema is a contract with every
	// agent that cached it, so a new argument is offered to callers who asked
	// for the experimental profile and to nobody else.
	ExperimentalProperties map[string]any
	// Class is the scheduler class the handler runs under. It is declared next
	// to the schema so a tool cannot be registered without one; ClassForCall
	// refines it for argument-dependent behaviour.
	Class SchedulerClass
}

const (
	MaxToolArgumentBytes = 32 << 20
	MaxPlanOperations    = 32
	MaxPlanContentBytes  = 4 << 20
	// MaxReadTargets is the most targets one read call takes.
	MaxReadTargets = 32
	// MaxSearchContextLines is the most context search attaches to a hit. A
	// larger request is reduced to it with a warning, not refused, so the
	// bound is stated in the description rather than as a schema maximum.
	MaxSearchContextLines = 20
)

// ProfileOrder is the frozen catalog: these are compared byte for byte
// against their fixtures. ProfileExperimental is deliberately absent.
var ProfileOrder = []Profile{ProfileFull, ProfileOrient, ProfileEdit, ProfileLean}

var modernProfileNames = map[Profile][]string{
	ProfileFull: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics",
		"edit_apply", "change_plan", "revision_diff", "evidence_get",
		"language_server_status", "language_server_setup",
	},
	ProfileOrient: {
		"workspace_open", "workspace_inspect", "search", "symbol_find", "navigate", "read", "diagnostics", "evidence_get",
	},
	ProfileLean: {"read", "search", "edit_apply"},
}

// experimentalProfileNames is the full frozen catalog plus every tool marked
// experimental, assembled once at startup so the two cannot drift.
func experimentalProfileNames() []string {
	names := append([]string(nil), modernProfileNames[ProfileFull]...)
	for _, descriptor := range Tools {
		if descriptor.Experimental {
			names = append(names, descriptor.Name)
		}
	}
	return names
}

var Tools = buildModernTools()

// jsonSchemaDialect is declared once, on each top-level schema. Nested
// objects inherit the dialect of the document they sit in, so stamping it on
// every one of them only made the catalog longer.
const jsonSchemaDialect = "https://json-schema.org/draft/2020-12/schema"

// withDialect states the JSON Schema dialect on a top-level schema.
func withDialect(schema map[string]any) map[string]any {
	schema["$schema"] = jsonSchemaDialect
	return schema
}

func schemaObject(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
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

// invariantsSchema declares what a plan's result must satisfy. Only the
// experimental catalog offers it, so the frozen schemas stay as they are.
func invariantsSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"maxItems":    16,
		"description": "Assertions on the prepared revision. Required violations or unknown results block apply; advisory ones are provisional. Edits invalidate proofs. api_compatible compares Go/TypeScript exports.",
		"items": schemaObject(map[string]any{
			"id":          stringSchema("Stable identifier, so the answer can be matched to the assertion. Assigned when omitted."),
			"kind":        enumSchema("no_new_diagnostics", "tests_pass", "no_references", "symbol_exists", "symbol_absent", "api_compatible"),
			"enforcement": enumSchema("required", "advisory"),
			"scope": schemaObject(map[string]any{
				"paths":    map[string]any{"type": "array", "items": stringSchema("Workspace-relative path."), "description": "Narrows a diagnostic assertion to these files; every staged file when omitted."},
				"symbol":   schemaObject(map[string]any{"path": stringSchema("File path."), "name_path": stringSchema("Declaration name path.")}, "path", "name_path"),
				"severity": enumSchema("error", "warning", "info", "hint"),
			}),
		}, "kind"),
	}
}

func targetSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"oneOf": []any{
			schemaObject(map[string]any{"handle": stringSchema("Opaque revision-bound handle.")}, "handle"),
			schemaObject(map[string]any{"symbol_locator": schemaObject(map[string]any{
				"path":      stringSchema("File path."),
				"name_path": stringSchema("Declaration name path."),
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

// mutationRangeTargetSchema is the handle alternative alone: replace_range
// replaces exactly the bytes a search or symbol_find handle names.
func mutationRangeTargetSchema() map[string]any {
	schema := targetSchema()
	schema["oneOf"] = append([]any{}, schema["oneOf"].([]any)[:1]...)
	return schema
}

func workspaceIDProperty() map[string]any {
	return stringSchema("Workspace ID; alternatively pass root.")
}

// rootProperty is the implicit alternative to workspace_id: the project root
// is opened (or reused) inside the call, saving the separate open.
func rootProperty() map[string]any {
	return stringSchema("Project root; opens or reuses its workspace.")
}

func statefulProperties() map[string]any {
	return map[string]any{
		"workspace_id":    workspaceIDProperty(),
		"root":            rootProperty(),
		"idempotency_key": stringSchema("Stable retry key; generated if omitted."),
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
		"from":                    stringSchema("Source path for move_file or copy_file."),
		"to":                      stringSchema("Destination path for move_file or copy_file."),
		"revision_id":             stringSchema("Expected document revision; create binds missing-target revision if omitted."),
		"destination_revision_id": stringSchema("Expected destination document revision for move_file and copy_file."),
		"expected_sha256":         stringSchema("copy_file: hash the source must have; bound at creation when omitted. The source may be an absolute path outside the workspace."),
		"depends_on":              map[string]any{"type": "array", "items": stringSchema("Predecessor op_id.")},
		"indentation":             enumSchema("exact", "syntax_anchor", "formatter"),
	}, "op_id", "kind")
}

// undescribed is the same schema with its property descriptions removed. A
// tool that offers one operation and a list of the same operations sends
// that shape twice, and the second copy is a few hundred tokens of prose the
// reader has already been given on the same page; the names, the types and
// the enum of kinds, which is where the meaning is, stay.
func undescribed(schema map[string]any) map[string]any {
	copied := CloneEnvelope(schema)
	delete(copied, "description")
	if items, ok := schema["items"].(map[string]any); ok {
		copied["items"] = undescribed(items)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return copied
	}
	stripped := make(map[string]any, len(properties))
	for name, value := range properties {
		property, isObject := value.(map[string]any)
		if !isObject {
			stripped[name] = value
			continue
		}
		stripped[name] = undescribed(property)
	}
	copied["properties"] = stripped
	return copied
}

// editOperationSchema is one edit_apply operation. replace_range needs a
// handle from a workspace that exists, so it is offered only for the
// single operation, not in the list.
func editOperationSchema(withRange bool) map[string]any {
	kinds := []string{"replace_literal", "create_file", "move_file", "copy_file", "delete_file"}
	if withRange {
		kinds = append(kinds, "replace_range")
	}
	properties := map[string]any{
		"kind":                    enumSchema(kinds...),
		"path":                    stringSchema("File path; replace_literal searches the workspace if omitted."),
		"old":                     stringSchema("Exact old text; uniform indentation differences are reported."),
		"new":                     stringSchema("replace_literal: replacement text."),
		"expected_count":          map[string]any{"type": "integer", "minimum": 1, "description": "Required match count (default 1); mismatch changes nothing."},
		"content":                 stringSchema("create_file: the file content; replace_range: exact replacement bytes."),
		"replace":                 map[string]any{"type": "boolean", "description": "Overwrite only with its current revision_id."},
		"from":                    stringSchema("Source; copy_file also accepts external absolute paths."),
		"to":                      stringSchema("Missing destination; parents are created."),
		"revision_id":             stringSchema("Read revision; required for overwrite or delete (unless hash)."),
		"destination_revision_id": stringSchema("Optional missing-destination revision."),
		"expected_sha256":         stringSchema("Delete guard (alternative to revision_id); optional copy source guard."),
	}
	if withRange {
		properties["target"] = mutationRangeTargetSchema()
	}
	return schemaObject(properties, "kind")
}

func changePlanSchema(stateful map[string]any, operationKinds []string) map[string]any {
	operation := planOperationSchema(operationKinds)
	operations := map[string]any{
		"type": "array", "items": operation, "maxItems": MaxPlanOperations,
		"description": fmt.Sprintf("At most %d operations; extend with action=edit and edit.mode=add.", MaxPlanOperations),
	}
	return schemaObject(map[string]any{
		"workspace_id": stateful["workspace_id"], "root": stateful["root"], "idempotency_key": stateful["idempotency_key"],
		"action":             enumSchema("create", "edit", "preview", "inspect", "prepare", "apply", "discard"),
		"operations":         operations,
		"plan_id":            stringSchema("Required after creation unless prepare supplies operations inline."),
		"plan_revision":      map[string]any{"type": "integer", "minimum": 1, "description": "Required with plan_id."},
		"prepared_revision":  stringSchema("Required when action=apply."),
		"accept_provisional": map[string]any{"type": "boolean", "description": "With action=apply, commit a PROVISIONAL plan by explicitly accepting its incomplete diagnostic evidence."},
		"edit": schemaObject(map[string]any{
			"mode": enumSchema("add", "update", "remove", "reorder", "replace_all"), "operations": undescribed(operations),
			"op_ids": map[string]any{"type": "array", "items": stringSchema("Operation identifier.")},
		}, "mode"),
	}, "action")
}

// buildModernTools assembles the catalog from its tool families. Which
// profile carries a tool is decided by modernProfileNames alone.
func buildModernTools() []ToolDescriptor {
	tools := orientationTools()
	tools = append(tools, editTools()...)
	for index := range tools {
		tools[index].InputSchema = withDialect(tools[index].InputSchema)
		if schema := leanSchema(tools[index].Name); schema != nil {
			tools[index].ProfileSchemas = map[Profile]map[string]any{ProfileLean: withDialect(schema)}
		}
	}
	return tools
}

// searchSchema is the closed search input: exactly one of a query, a
// result-set refinement or a Git history source.
func searchSchema() map[string]any {
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
	properties := map[string]any{
		"workspace_id": workspaceIDProperty(), "root": rootProperty(),
		"query": stringSchema("Text, regular expression, or for the semantic modes a symbol name."), "mode": enumSchema("literal", "regex", "references", "definition", "implementation", "type_definition", "incoming_calls", "outgoing_calls"),
		"paths":             map[string]any{"type": "array", "items": stringSchema("Path substring or glob (whole path or base name)."), "description": "Keep only hits under these paths."},
		"context_lines":     map[string]any{"type": "integer", "minimum": 0, "description": fmt.Sprintf("Context lines per hit; at most %d (larger values reduced to %d with a warning).", MaxSearchContextLines, MaxSearchContextLines)},
		"result_set_handle": stringSchema("Frozen current-source result set."), "refine": refinement,
		"git_history": historySource, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		"include_handles": map[string]any{"type": "boolean", "description": "Add an editable handle and the column to every hit, for edit_apply kind=replace_range."},
	}
	return schemaObject(properties)
}

// workspaceKindSchema is workspace_open's kind, which may be omitted: what
// was sent already says which kind is meant.
func workspaceKindSchema() map[string]any {
	schema := enumSchema("project", "documents")
	schema["description"] = "Defaults to project when root is given and to documents when only files are."
	return schema
}

// orientationTools defines read-only tools; fixed profile membership is listed above.
func orientationTools() []ToolDescriptor {
	return []ToolDescriptor{
		{Class: ClassProviderRead, Name: "workspace_open", Description: "Open a project or exact documents workspace; return overview, capabilities and preparation commands. Other tools accept root directly.", ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"kind":     workspaceKindSchema(),
			"root":     stringSchema("Project root; required for kind=project."),
			"files":    map[string]any{"type": "array", "items": stringSchema("Allowlisted document."), "minItems": 1},
			"overview": enumSchema("compact", "full"),
		})},
		{Class: ClassProviderRead, Name: "workspace_inspect", Description: "Inspect workspace revision, provider health, coverage and limits.", ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "root": rootProperty(), "view": enumSchema("status", "overview", "map"),
		})},
		{Class: ClassPureRead, Name: "search", Description: "Search text (literal default, regex per line) or ask the language server about a symbol; semantic modes fall back to literal. Scope with paths; context_lines adds context. Refine result sets or search git_history.", ReadOnly: true, Idempotent: true, InputSchema: searchSchema()},
		{Class: ClassProviderRead, Name: "symbol_find", Description: "Find declarations by name substring; return read/edit handles. Go and Python resolve natively; other languages use the provider.", ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "root": rootProperty(), "query": stringSchema("Name path: Name, Parent/Name, Parent.Name, Parent::Name or (*Parent).Name."), "include_source": map[string]any{"type": "boolean"},
		}, "query")},
		{Class: ClassProviderRead, Name: "navigate", Description: "Ask the language server for a declaration's definition, references, callers or hover. Supply symbol or target; use search for text.", ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "root": rootProperty(), "relation": enumSchema("definition", "type_definition", "implementation", "references", "incoming_calls", "outgoing_calls", "hover"), "target": targetSchema(),
			"symbol": stringSchema("Declaration name; resolved to its one declaration so no target is needed."),
		}, "relation"), ExperimentalProperties: preparedSelectorProperties()},
		{Class: ClassPureRead, Name: "read", Description: "Read a path, declaration or handle; targets batches reads. Returns revision_id and line count. Window with start_line/end_line; max_lines caps delivery, view=outline lists declarations.", ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "root": rootProperty(), "target": readTargetSchema(), "view": enumSchema("source", "outline", "history", "changes"),
			"targets": map[string]any{"type": "array", "minItems": 1, "maxItems": MaxReadTargets, "description": "Batch reads; per-target options override call defaults.", "items": schemaObject(map[string]any{
				"path": stringSchema("Workspace-relative file path."), "start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1},
				"max_lines":      map[string]any{"type": "integer", "minimum": 1, "description": "Cap for this target; overrides the call-level max_lines."},
				"view":           enumSchema("source", "outline"),
				"numbered":       map[string]any{"type": "boolean", "description": "Prefix each line with its number; overrides the call-level numbered."},
				"symbol_locator": schemaObject(map[string]any{"path": stringSchema("File path."), "name_path": stringSchema("Declaration name path.")}, "path", "name_path"),
			})},
			"start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1},
			"max_lines": map[string]any{"type": "integer", "minimum": 1, "description": "Line cap per target; reports truncation and total lines."},
			"numbered":  map[string]any{"type": "boolean", "description": "Prefix each line with its number and a tab."},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "description": "history and changes views: number of entries."},
		}), ExperimentalProperties: preparedSelectorProperties()},
		{Class: ClassProviderRead, Name: "language_server_status", Description: "Inspect provider health and language-server attachment. languages starts named servers now; other incidental languages start on demand.", ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "root": rootProperty(),
			"languages": map[string]any{"type": "array", "items": stringSchema("Filetype, such as python or typescript."), "description": "Start the servers for these languages now instead of on first use."},
		})},
		{Class: ClassPureRead, Name: "diagnostics", Description: "Inspect diagnostic evidence, coverage and provenance; full includes finding bodies.", ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "root": rootProperty(), "since": stringSchema("Optional diagnostic cursor."),
			"full": map[string]any{"type": "boolean", "description": "Return the complete report including finding bodies and per-dimension evidence IDs."},
		}), ExperimentalProperties: preparedSelectorProperties()},
		{Class: ClassPureRead, Name: "evidence_get", Description: "Page diff, diagnostic, command or provenance evidence.", ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "root": rootProperty(), "evidence_id": stringSchema("Evidence identifier."), "cursor": stringSchema("Optional page cursor."),
		}, "evidence_id")},
	}
}

// editTools are the mutation, preparation and revision tools of the coding profile.
func editTools() []ToolDescriptor {
	stateful := statefulProperties()
	operationKinds := []string{"replace_symbol", "delete_symbol", "insert_before", "insert_after", "replace_range", "create_file", "move_file", "copy_file", "delete_file", "rename_symbol", "safe_delete_symbol", "inline_symbol", "replace_matches", "apply_code_action"}
	return []ToolDescriptor{
		{Class: ClassCanonicalWrite, Name: "language_server_setup", Description: "Restart the provider and check attachment. Install servers and parsers outside Huyang using Mason or system packages.", Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": stateful["workspace_id"], "root": stateful["root"], "idempotency_key": stateful["idempotency_key"],
			"action": enumSchema("restart"),
		}, "action")},
		{Class: ClassCanonicalWrite, Name: "edit_apply", Description: "Apply a guarded edit and return revisions and diagnostics. replace_literal matches old text (expected_count defaults to 1); create_file refuses existing paths unless replace plus revision_id. delete_file needs revision_id or expected_sha256. Handles support replace_range. operations runs sequentially; a refusal keeps earlier edits. Use change_plan for atomic edits. Transfers preserve bytes; stage Git changes separately.", Destructive: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "root": rootProperty(), "idempotency_key": stateful["idempotency_key"],
			"preview_only": map[string]any{"type": "boolean", "description": "Compute the diff without changing canonical bytes."},
			"verbose":      map[string]any{"type": "boolean", "description": "Add the full change record, hashes and handle resolution to the response."},
			"format":       map[string]any{"type": "boolean", "description": "Format edits (default true); false preserves bytes. Transfers and deletes are never formatted."},
			"operation":    editOperationSchema(true),
			"operations":   map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "description": "Sequential edits, excluding replace_range; earlier edits remain on refusal. One formatter pass and receipt.", "items": undescribed(editOperationSchema(false))},
		})},
		{Class: ClassSandboxWrite, Name: "change_plan", Description: "Prepare atomic edits in a sandbox, then apply with plan_id, plan_revision and prepared_revision. Trusted preparation runs format, parser, check and test gates. replace_matches uses a search set handle; rename_symbol, inline_symbol, safe_delete_symbol and apply_code_action ask the language server. Required invariants cannot be waived.", Destructive: true, InputSchema: changePlanSchema(stateful, operationKinds), ExperimentalProperties: map[string]any{
			"view":       enumSchema("plan", "impact", "semantic"),
			"invariants": invariantsSchema(),
		}},
		{Class: ClassCanonicalWrite, Name: "revision_diff", Description: "Compare wsrev_N revisions (either end may be current). External changes are inferred by path; use git diff for Git refs.", ReadOnly: true, Idempotent: true, InputSchema: schemaObject(map[string]any{
			"workspace_id": workspaceIDProperty(), "root": rootProperty(), "from_revision": stringSchema("Earlier revision: wsrev_N or current."), "to_revision_or_current": stringSchema("Later revision: wsrev_N or current."),
		}, "from_revision", "to_revision_or_current"), ExperimentalProperties: map[string]any{
			"view": enumSchema("exact", "semantic"),
		}},
	}
}

func Catalog(profile Profile) []ToolDescriptor {
	if profile == ProfileEdit {
		profile = ProfileFull
	}
	byName := make(map[string]ToolDescriptor, len(Tools))
	for _, descriptor := range Tools {
		byName[descriptor.Name] = descriptor
	}
	names := modernProfileNames[profile]
	experimental := profile == ProfileExperimental
	if experimental {
		names = experimentalProfileNames()
	}
	catalog := make([]ToolDescriptor, 0, len(names))
	for _, name := range names {
		descriptor, ok := byName[name]
		if !ok {
			continue
		}
		if override := descriptor.ProfileSchemas[profile]; override != nil {
			descriptor.InputSchema = override
		}
		if profile == ProfileLean {
			descriptor.Description = leanDescriptions[name]
		}
		if experimental && len(descriptor.ExperimentalProperties) > 0 {
			descriptor.InputSchema = withExperimentalProperties(descriptor.InputSchema, descriptor.ExperimentalProperties)
		}
		if experimental {
			descriptor = usabilityDescriptor(descriptor)
		}
		catalog = append(catalog, descriptor)
	}
	return catalog
}

// withExperimentalProperties copies a frozen schema and adds the properties
// only the experimental catalog offers, leaving the original untouched so the
// frozen profiles cannot acquire them.
func withExperimentalProperties(schema map[string]any, extra map[string]any) map[string]any {
	copied := make(map[string]any, len(schema))
	for key, value := range schema {
		copied[key] = value
	}
	properties, _ := schema["properties"].(map[string]any)
	merged := make(map[string]any, len(properties)+len(extra))
	for key, value := range properties {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	copied["properties"] = merged
	return copied
}

// preparedSelectorProperties name a prepared revision to read or ask about,
// by its revision or by the plan that holds it. They are experimental because
// the frozen schemas are a contract, and because a call that answers about
// staged bytes is a different promise from one that answers about the
// repository.
func preparedSelectorProperties() map[string]any {
	return map[string]any{
		"revision":      stringSchema("Prepared revision (prep_...) to answer about; omit, or pass current, for the canonical workspace."),
		"plan_id":       stringSchema("Plan whose prepared revision to answer about, as an alternative to naming the revision."),
		"plan_revision": map[string]any{"type": "integer", "minimum": 1, "description": "Expected plan revision; a preparation replaced since is refused rather than answered."},
	}
}

func OutputEnvelopeSchema() map[string]any {
	return withDialect(schemaObject(map[string]any{
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
			"id": stringSchema("Stable diagnostic ID."), "kind": enumSchema("new", "persisting", "resolved"),
			"severity": map[string]any{"type": "integer"}, "path": stringSchema("Affected document, workspace-relative."),
			"line": map[string]any{"type": "integer"}, "message": stringSchema("What the finding says, bounded."),
			"attribution":      map[string]any{"type": "object"},
			"baseline_missing": map[string]any{"type": "boolean", "description": "First observation: finding may be new or pre-existing because no pre-edit baseline exists."},
		}, "id", "kind")},
		"diagnostic_updates_truncated": map[string]any{"type": "boolean"},
		"guide":                        map[string]any{"type": "array", "items": stringSchema("One rule for calling this server at its cheapest, sent once when a workspace is first opened.")},
		"idempotency":                  enumSchema("created", "replayed"),
		"idempotency_persisted":        map[string]any{"type": "boolean"},
		"retryable":                    map[string]any{"type": "boolean", "description": "Whether the same call, unchanged, can succeed later; present only where that is known."},
	}, "api_version", "request_id", "outcome", "summary", "data", "warnings", "next"))
}

// CatalogJSON renders the tools/list payload. The output envelope schema is
// deliberately not attached to every tool: it is identical for all of them
// and was a quarter of the catalog an agent loads into every session.
func CatalogJSON(profile Profile) ([]byte, error) {
	catalog := Catalog(profile)
	tools := make([]map[string]any, 0, len(catalog))
	for _, descriptor := range catalog {
		tools = append(tools, map[string]any{
			"name": descriptor.Name, "description": descriptor.Description,
			"inputSchema": descriptor.InputSchema,
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
	expected := map[Profile]int{ProfileFull: 13, ProfileOrient: 8, ProfileEdit: 13, ProfileLean: 3}
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
