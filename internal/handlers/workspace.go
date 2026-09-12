package handlers

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *Handlers) open(ctx context.Context, requestID string, arguments map[string]any) map[string]any {
	kind, _ := arguments["kind"].(string)
	options := workspacecore.OpenOptions{Kind: workspacecore.Kind(kind), StateDir: h.stateDir, ProviderEpoch: 1, Sectioner: workspacecore.NativeSectioner{}}
	switch kind {
	case "project":
		options.Root, _ = arguments["root"].(string)
	case "documents":
		for _, value := range mcpapi.AnySlice(arguments["files"]) {
			if name, ok := value.(string); ok {
				absolute, err := filepath.Abs(name)
				if err != nil {
					return mcpapi.Envelope(requestID, nil, "failed", "workspace_open_failed", err.Error(), map[string]any{})
				}
				options.Files = append(options.Files, filepath.Clean(absolute))
			}
		}
		sort.Strings(options.Files)
	default:
		return mcpapi.Envelope(requestID, nil, "failed", "invalid_workspace_kind", "kind must be project or documents", map[string]any{})
	}
	opened, err := workspacecore.Open(options)
	if err != nil {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_open_failed", err.Error(), map[string]any{})
	}
	opened, created, err := h.registry.Adopt(opened, options.Files)
	if err != nil {
		return mcpapi.Envelope(requestID, nil, "failed", "service_state_persist_failed", err.Error(), map[string]any{})
	}
	if err := opened.PrimeDocuments(); err != nil {
		return mcpapi.Failure(requestID, opened, "workspace_baseline_failed", err)
	}
	var canonicalBackend provider.Provider
	if opened.Identity().Kind == workspacecore.KindProject && providerpool.ShippedRuntimePath() != "" {
		canonicalBackend, _ = h.pool.Canonical(ctx, opened)
	}
	orientation, err := opened.Orient()
	if err != nil {
		return mcpapi.Failure(requestID, opened, "workspace_overview_failed", err)
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
		semanticProvider = providerpool.Status(ctx, canonicalBackend)
	}
	if policy, policyErr := workspacecore.LoadPipelinePolicy(opened.Identity().Root, ""); policyErr == nil {
		reconcilePipelineCapabilities(&capabilities, policy)
	}
	action := "Opened"
	if !created {
		action = "Reopened"
	}
	var overview any = mcpapi.CompactOrientation(orientation)
	if mode, _ := arguments["overview"].(string); mode == "full" {
		overview = orientation
	}
	return mcpapi.Envelope(requestID, opened, "ok", "", fmt.Sprintf("%s %s workspace with %d entries", action, kind, len(orientation.Entries)), map[string]any{
		"revision":          fmt.Sprintf("wsrev_%d", opened.Identity().StateSeq),
		"capabilities":      compactCapabilities(capabilities),
		"semantic_provider": compactProviderStatus(semanticProvider),
		"service_limits":    map[string]any{"tool_call_timeout_ms": h.toolTimeout.Milliseconds()},
		"overview":          overview,
		"recent_commits":    mcpapi.CompactRecentCommits(recent, 3),
		"registry":          map[string]any{"persistent": true, "reused": !created},
	})
}

// compactCapabilities keeps what an agent decides on: which native and
// optional facilities exist and the semantic coverage label. Limits and
// environment failures stay behind workspace_inspect.
func compactCapabilities(inspection workspacecore.Inspection) map[string]any {
	compact := map[string]any{"native": inspection.Native, "optional": inspection.Optional, "semantic": inspection.Coverage.Semantic}
	if len(inspection.Failures) > 0 {
		compact["failures"] = inspection.Failures
	}
	return compact
}

// compactProviderStatus keeps the provider's backend and health; the
// process, endpoint and runtime detail stay behind workspace_inspect.
func compactProviderStatus(status map[string]any) map[string]any {
	if status == nil {
		return nil
	}
	compact := map[string]any{"backend": status["backend"], "state": status["state"]}
	if code := fmt.Sprint(status["failure_code"]); code != "" && code != "<nil>" {
		compact["failure_code"] = code
	}
	return compact
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

// inspect answers workspace_inspect: the refreshed inspection, the provider
// status when one is running, and the pipeline policy with its trust state.
func (h *Handlers) inspect(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	if _, refreshErr := workspace.RefreshKnownDocuments(); refreshErr != nil {
		return mcpapi.Failure(requestID, workspace, "workspace_refresh_failed", refreshErr)
	}
	inspection := workspace.Inspect()
	var semanticProvider map[string]any
	if backend, providerErr := h.pool.Canonical(ctx, workspace); providerErr == nil {
		inspection.Optional["provider"] = "available"
		inspection.Optional["lsp"] = "probe_with_language_server_status"
		semanticProvider = providerpool.Status(ctx, backend)
	}
	policy, policyErr := workspacecore.LoadPipelinePolicy(workspace.Identity().Root, "")
	if policyErr != nil {
		result := mcpapi.Failure(requestID, workspace, "workspace_policy_invalid", policyErr)
		result["next"] = []any{map[string]any{
			"tool": "workspace_inspect", "action": "repair_pipeline_configuration",
			"project_config": filepath.Join(workspace.Identity().Root, ".huyang.toml"),
		}}
		return result
	}
	reconcilePipelineCapabilities(&inspection, policy)
	view, _ := arguments["view"].(string)
	if view == "" {
		view = "status"
	}
	pipelineState, pipelineReason := describePipelineState(policy)
	base := map[string]any{
		"view": view, "revision": fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
		"service_limits": map[string]any{"tool_call_timeout_ms": h.toolTimeout.Milliseconds()},
		"inspection":     inspection, "semantic_provider": semanticProvider, "scheduler": h.schedulerInfo(), "pipeline_policy": policy,
		"pipeline_state": map[string]any{
			"state": pipelineState, "configured": policy.ProjectConfig != "", "trusted": policy.Trusted,
			"reason": pipelineReason, "project_config": policy.ProjectConfig, "user_config": policy.UserConfig,
			"configuration_scope": "The project .huyang.toml declares commands; execution trust is granted separately by [trust].roots in the user config.",
		},
	}
	summary := "Workspace inspection is current"
	if view == "overview" || view == "map" {
		orientation, err := workspace.Orient()
		if err != nil {
			return mcpapi.Failure(requestID, workspace, "workspace_map_failed", err)
		}
		base["overview"] = orientation
		summary = fmt.Sprintf("%d workspace entries", len(orientation.Entries))
	}
	result := mcpapi.Envelope(requestID, workspace, "ok", "", summary, base)
	if pipelineState == "configured_untrusted" {
		result["warnings"] = []string{pipelineReason}
		result["next"] = []any{map[string]any{
			"tool": "workspace_inspect", "action": "trust_workspace_root",
			"root": workspace.Identity().Root, "user_config": policy.UserConfig,
		}}
	}
	return result
}

// describePipelineState names whether project commands are configured and
// trusted, with the reason a caller can act on.
func describePipelineState(policy workspacecore.PipelinePolicy) (string, string) {
	switch {
	case policy.ProjectConfig != "" && policy.Trusted:
		return "configured_trusted", "Project commands are configured and this workspace root is trusted."
	case policy.ProjectConfig != "":
		return "configured_untrusted", "Project commands are configured but disabled until this workspace root is trusted in the user policy."
	default:
		return "not_configured", "No project .huyang.toml is present; only built-in parser checks are available."
	}
}
