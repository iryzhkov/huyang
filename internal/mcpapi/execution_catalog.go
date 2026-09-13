package mcpapi

func pathExplainTool() ToolDescriptor {
	endpoint := map[string]any{"oneOf": []any{map[string]any{"type": "string"}, schemaObject(map[string]any{"path": stringSchema("Workspace-relative declaration file."), "name_path": stringSchema("Function name.")}, "path", "name_path")}}
	return ToolDescriptor{Name: "path_explain", Description: "Explain bounded static candidate paths between functions, including source evidence, selected flow, uncertainty and narrowly proven absence. Never launches a program.", Experimental: true, Profiles: []Profile{ProfileExperimental}, Class: ClassProviderRead, ReadOnly: true, Idempotent: true,
		InputSchema: schemaObject(map[string]any{"workspace_id": workspaceIDProperty(), "root": rootProperty(),
			"revision": stringSchema("Current content revision or a prepared revision."), "plan_id": stringSchema("Read this plan's prepared revision."),
			"plan_revision": map[string]any{"type": "integer", "minimum": 1}, "from": endpoint, "to": endpoint,
			"mode": enumSchema("static", "combined"), "trace_id": stringSchema("Reserved for combined trace overlays."),
			"use_provider": map[string]any{"type": "boolean"}, "max_paths": map[string]any{"type": "integer", "minimum": 1, "maximum": 16},
			"max_depth": map[string]any{"type": "integer", "minimum": 1, "maximum": 64}}, "from", "to")}
}

func executionGraphTool() ToolDescriptor {
	return ToolDescriptor{
		Name: "execution_graph", Description: "Build an experimental revision-keyed execution snapshot. Source boundaries remain unresolved where no call adapter supplies evidence; partial graphs cannot prove unreachability.",
		Experimental: true, Profiles: []Profile{ProfileExperimental}, Class: ClassProviderRead,
		ReadOnly: true, Idempotent: true,
		InputSchema: schemaObject(map[string]any{"workspace_id": workspaceIDProperty(), "root": rootProperty(), "revision": stringSchema("Current content revision or a prepared revision."), "plan_id": stringSchema("Read the prepared sandbox for this plan."), "plan_revision": map[string]any{"type": "integer", "minimum": 1}, "expand_functions": map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string"}, "description": "Expand bounded flow evidence for selected function IDs from a prior graph."}, "use_provider": map[string]any{"type": "boolean", "description": "Use the embedded batch contributor (default true); false retains native parser and source-boundary evidence."}}),
	}
}
