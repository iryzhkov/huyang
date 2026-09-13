package handlers

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// decodeOpenOptions reads what to open. The second result is the reply that
// says why the arguments name nothing openable.
func (h *Handlers) decodeOpenOptions(requestID string, arguments map[string]any) (workspacecore.OpenOptions, map[string]any) {
	kind, _ := arguments["kind"].(string)
	options := workspacecore.OpenOptions{Kind: workspacecore.Kind(kind), StateDir: h.stateDir, ProviderEpoch: 1, Sectioner: workspacecore.NativeSectioner{}}
	switch kind {
	case "project":
		options.Root, _ = arguments["root"].(string)
		// A relative root would be resolved in this process, whose working
		// directory no caller can see: benchmark agents that named
		// "bench/agent-efficiency/fixtures/go" opened and edited that path
		// under the service's own directory, in a different repository,
		// and were told the literal they were replacing was not there.
		if options.Root != "" && !filepath.IsAbs(options.Root) {
			result := mcpapi.Envelope(requestID, nil, "failed", "root_not_absolute",
				fmt.Sprintf("root must be an absolute path; %q would be resolved against the service's own working directory, not yours", options.Root),
				map[string]any{"root": options.Root})
			result["next"] = []any{map[string]any{"action": "name_the_repository_root_by_its_absolute_path"}}
			return options, result
		}
	case "documents":
		for _, value := range mcpapi.AnySlice(arguments["files"]) {
			if name, ok := value.(string); ok {
				absolute, err := filepath.Abs(name)
				if err != nil {
					return options, mcpapi.Envelope(requestID, nil, "failed", "workspace_open_failed", err.Error(), map[string]any{})
				}
				options.Files = append(options.Files, filepath.Clean(absolute))
			}
		}
		sort.Strings(options.Files)
	default:
		return options, mcpapi.Envelope(requestID, nil, "failed", "invalid_workspace_kind", "kind must be project or documents", map[string]any{})
	}
	return options, nil
}

// open answers workspace_open. delivered says whether the reply reaches the
// client: the same function opens a workspace on the way in when a call
// named a root, and that reply is discarded, so it must not count as having
// shown anybody the overview.
func (h *Handlers) open(ctx context.Context, requestID string, arguments map[string]any, delivered bool) map[string]any {
	kind, _ := arguments["kind"].(string)
	options, failure := h.decodeOpenOptions(requestID, arguments)
	if failure != nil {
		return failure
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
	// Opening is where a client first meets a workspace; from here on it is
	// told what changes, not what was already there.
	h.notices.Register(opened.Identity().ID, clientIdentity(ctx), opened.DiagnosticNoticeHead())
	var canonicalBackend provider.Provider
	if opened.Identity().Kind == workspacecore.KindProject && providerpool.ShippedRuntimePath() != "" {
		canonicalBackend, _ = h.pool.Canonical(ctx, opened)
		h.warmLanguageServers(opened, canonicalBackend)
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
	var commands map[string]any
	if policy, policyErr := workspacecore.LoadPipelinePolicy(opened.Identity().Root, ""); policyErr == nil {
		reconcilePipelineCapabilities(&capabilities, policy)
		commands = describeCommands(policy)
	}
	action := "Opened"
	if !created {
		action = "Reopened"
	}
	mode, _ := arguments["overview"].(string)
	// Agents open a workspace they already hold several times a session:
	// 786 opens across 99 sessions in four days of fleet spool. The second
	// answer is the tree, the commands and the capabilities the client was
	// already shown. When none of it has changed, say that instead.
	fingerprint := fmt.Sprintf("%s|%d|%d|%v", mode, opened.Identity().StateSeq, len(orientation.Entries), commands)
	if delivered && mode != "full" && h.notices.TakeOverview(opened.Identity().ID, clientIdentity(ctx), fingerprint) {
		return mcpapi.Envelope(requestID, opened, "ok", "",
			fmt.Sprintf("Reopened %s workspace with %d entries; overview, commands and capabilities unchanged since this session first opened it", kind, len(orientation.Entries)),
			map[string]any{
				"revision":          fmt.Sprintf("wsrev_%d", opened.Identity().StateSeq),
				"entry_count":       len(orientation.Entries),
				"overview_repeated": true,
			})
	}
	var overview any = mcpapi.CompactOrientation(orientation)
	if mode == "full" {
		overview = orientation
	}
	result := mcpapi.Envelope(requestID, opened, "ok", "", fmt.Sprintf("%s %s workspace with %d entries", action, kind, len(orientation.Entries)), map[string]any{
		"revision":          fmt.Sprintf("wsrev_%d", opened.Identity().StateSeq),
		"capabilities":      compactCapabilities(capabilities),
		"semantic_provider": compactProviderStatus(semanticProvider),
		"service_limits":    map[string]any{"tool_call_timeout_ms": h.toolTimeout.Milliseconds()},
		"overview":          overview,
		"recent_commits":    mcpapi.CompactRecentCommits(recent, 3),
		"commands":          commands,
	})
	return result
}

// cheapestCallRules are the rules that change what a session costs, in the
// order they come up. They are measured, not advisory: each one was a habit
// the benchmark and the friction spool showed agents falling into, and each
// costs calls or tokens every time. The tool descriptions carry everything
// else; this stays a handful of lines.
var cheapestCallRules = []string{
	"Name the repository with root on any call; workspace_open is for this overview only.",
	"Change text you know with edit_apply replace_literal: one call, no search first.",
	"Several edits at once: edit_apply operations; change_plan when they must be atomic.",
	"Move, copy and delete files with edit_apply, then run the git command under next.",
	"A file over 500 lines: read view=outline or max_lines before the whole file.",
}

// warmAttachWait is the per-language attach wait the background probe
// allows itself; the servers it starts keep starting after it returns.
const warmAttachWait = 250

// warmLanguageServers starts the language servers of a project workspace
// in the background once per provider generation, so the first edit finds
// a warm client instead of paying the attach deadline. The open never
// waits for it; the probe is the same workspace_support call
// language_server_status makes, with a short attach wait, and it loads
// one sample buffer per language so the servers auto-attach as they come
// up. It is bounded by its own timeout and by the languages present.
func (h *Handlers) warmLanguageServers(workspace *workspacecore.Workspace, backend provider.Provider) {
	if backend == nil {
		return
	}
	key := fmt.Sprintf("%s@%d", workspace.Identity().ID, backend.Descriptor().Epoch)
	if _, done := h.warmed.LoadOrStore(key, true); done {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = providerpool.Call(ctx, workspace, backend, providerpool.CallSpec{RequestID: "warm_" + string(workspace.Identity().ID)},
			"workspace_support", map[string]any{"root": workspace.Identity().Root, "attach_wait_ms": warmAttachWait, "warm": true})
	}()
}

// compactCapabilities keeps what an agent decides on: which native and
// optional facilities exist and the semantic coverage label. Limits and
// environment failures stay behind workspace_inspect.
func compactCapabilities(inspection workspacecore.Inspection) map[string]any {
	compact := map[string]any{"semantic": inspection.Coverage.Semantic}
	// The native facilities are always present and every optional one
	// that is available needs no mention; only what is missing or degraded
	// changes what the agent does next.
	missing := map[string]any{}
	for name, state := range inspection.Optional {
		if text := fmt.Sprint(state); text != "available" {
			missing[name] = state
		}
	}
	if len(missing) > 0 {
		compact["not_available"] = missing
	}
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
	if policy.ProjectConfig != "" || len(policy.Detected) > 0 {
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
	if arguments["view"] == "revision" {
		return mcpapi.Envelope(requestID, workspace, "ok", "", "Current workspace revision", map[string]any{"revision": fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)})
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
	case len(policy.Detected) > 0 && policy.Trusted:
		return "detected_trusted", fmt.Sprintf("Commands were detected from %s (no .huyang.toml) and this workspace root is trusted; write .huyang.toml to override them.", strings.Join(policy.Detected, ", "))
	case len(policy.Detected) > 0:
		return "detected_untrusted", fmt.Sprintf("Commands were detected from %s (no .huyang.toml) but are disabled until this workspace root is listed under [trust] roots in the user policy.", strings.Join(policy.Detected, ", "))
	default:
		return "not_configured", "No project .huyang.toml is present and no repository layout was recognised; only built-in parser checks are available."
	}
}
