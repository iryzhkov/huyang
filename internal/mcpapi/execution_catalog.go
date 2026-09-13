package mcpapi

func executionGraphTool() ToolDescriptor {
	return ToolDescriptor{
		Name: "execution_graph", Description: "Build an experimental revision-keyed execution snapshot. Source boundaries remain unresolved where no call adapter supplies evidence; partial graphs cannot prove unreachability.",
		Experimental: true, Profiles: []Profile{ProfileExperimental}, Class: ClassProviderRead,
		ReadOnly: true, Idempotent: true,
		InputSchema: schemaObject(map[string]any{"workspace_id": workspaceIDProperty(), "root": rootProperty(), "expand_functions": map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string"}, "description": "Expand bounded flow evidence for selected function IDs from a prior graph."}, "use_provider": map[string]any{"type": "boolean", "description": "Use the embedded batch contributor (default true); false retains native parser and source-boundary evidence."}}),
	}
}
