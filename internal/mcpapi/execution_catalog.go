package mcpapi

func executionGraphTool() ToolDescriptor {
	return ToolDescriptor{
		Name: "execution_graph", Description: "Build an experimental revision-keyed execution snapshot. Source boundaries remain unresolved where no call adapter supplies evidence; partial graphs cannot prove unreachability.",
		Experimental: true, Profiles: []Profile{ProfileExperimental}, Class: ClassProviderRead,
		ReadOnly: true, Idempotent: true,
		InputSchema: schemaObject(map[string]any{"workspace_id": workspaceIDProperty(), "root": rootProperty()}),
	}
}
