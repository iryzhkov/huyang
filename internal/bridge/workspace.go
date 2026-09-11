package bridge

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

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
	if err := opened.PrimeDocuments(); err != nil {
		return modernFailure(requestID, opened, "workspace_baseline_failed", err)
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
		semanticProvider = canonicalProviderStatus(ctx, canonicalBackend)
	}
	if policy, policyErr := workspacecore.LoadPipelinePolicy(opened.Identity().Root, ""); policyErr == nil {
		reconcilePipelineCapabilities(&capabilities, policy)
	}
	action := "Opened"
	if !created {
		action = "Reopened"
	}
	var overview any = compactOrientation(orientation)
	if mode, _ := arguments["overview"].(string); mode == "full" {
		overview = orientation
	}
	return modernEnvelope(requestID, opened, "ok", "", fmt.Sprintf("%s %s workspace with %d entries", action, kind, len(orientation.Entries)), map[string]any{
		"revision":     fmt.Sprintf("wsrev_%d", opened.Identity().StateSeq),
		"capabilities": capabilities, "semantic_provider": semanticProvider,
		"service_limits": map[string]any{"tool_call_timeout_ms": d.toolTimeout.Milliseconds()},
		"overview":       overview,
		"recent_commits": compactRecentCommits(recent, 3),
		"registry":       map[string]any{"persistent": true, "reused": !created},
	})
}

func reconcilePipelineCapabilities(inspection *workspacecore.Inspection, policy workspacecore.PipelinePolicy) {
	inspection.Optional["parser"] = "available_for_go_json_jsonl_toml_yaml_markdown"
	commandsConfigured := len(policy.Check) > 0 || len(policy.Tests) > 0
	formatConfigured := len(policy.Format.Gate.Command) > 0 || len(policy.Format.Transform.Command) > 0
	state := "unavailable"
	if policy.ProjectConfig != "" {
		state = "configured_untrusted"
		if policy.Trusted {
			state = "available"
		}
	}
	if commandsConfigured {
		inspection.Optional["project_commands"] = state
	}
	if formatConfigured {
		inspection.Optional["formatter"] = state
	}
}
