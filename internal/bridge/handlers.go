package bridge

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// workspaceLookup is the handlers' view of the workspace registry: find an
// open workspace, register a newly opened one, and persist an identity that
// moved. The service owns the registry and its durable file.
type workspaceLookup interface {
	lookup(id workspacecore.ID) *workspacecore.Workspace
	adopt(opened *workspacecore.Workspace, files []string) (*workspacecore.Workspace, bool, error)
	persistIdentity(id workspacecore.ID) error
}

// revisionProvenance answers the two receipt-backed questions the handlers
// ask: which native edits produced the revisions in a range, and which
// paths changed at one revision.
type revisionProvenance interface {
	recordedRevisionDiffs(workspaceID string, fromSeq, toSeq uint64) []recordedRevisionDiff
	canonicalChangedPaths(workspaceID workspacecore.ID, target uint64) ([]string, error)
}

// recordedRevisionDiff is one native edit receipt in revision order.
type recordedRevisionDiff struct {
	from, to                  uint64
	path, beforeSHA, afterSHA string
	diff                      any
}

// replayCheckpoint persists an early receipt for a canonical mutation before
// its post-mutation work runs. The service installs it in the request
// context of every stateful call; edit_apply invokes it once the canonical
// bytes are written.
type replayCheckpoint func(map[string]any) error

type replayCheckpointKey struct{}

func withReceiptCheckpoint(ctx context.Context, checkpoint replayCheckpoint) context.Context {
	return context.WithValue(ctx, replayCheckpointKey{}, checkpoint)
}

func checkpointStatefulReceipt(ctx context.Context, result map[string]any) error {
	checkpoint, _ := ctx.Value(replayCheckpointKey{}).(replayCheckpoint)
	if checkpoint == nil {
		return nil
	}
	return checkpoint(result)
}

// toolHandlers implements every tool over the workspace core and the
// provider pool. It holds no replay or scheduling state: the service
// decides when a handler runs, the handlers decide what it does.
type toolHandlers struct {
	registry     workspaceLookup
	provenance   revisionProvenance
	pool         *providerPool
	verification *verificationCache
	notices      *noticeDelivery
	stateDir     string
	toolTimeout  time.Duration
	// schedulerInfo describes the scheduler classes and quotas for
	// workspace_inspect; the scheduler itself belongs to the service.
	schedulerInfo func() map[string]any
}

// execute runs one tool call against the workspace it names. workspace_open
// and a path-only read need no workspace ID; every other tool fails without
// a registered one.
func (h *toolHandlers) execute(ctx context.Context, requestID, name string, arguments map[string]any) map[string]any {
	if err := ctx.Err(); err != nil {
		return modernEnvelope(requestID, nil, "failed", "request_cancelled", err.Error(), map[string]any{})
	}
	if name == "workspace_open" {
		return h.open(ctx, requestID, arguments)
	}
	workspaceID, _ := arguments["workspace_id"].(string)
	if name == "read" && strings.TrimSpace(workspaceID) == "" {
		target, _ := arguments["target"].(map[string]any)
		path, _ := target["path"].(string)
		if strings.TrimSpace(path) == "" {
			return modernEnvelope(requestID, nil, "failed", "workspace_required", "workspace_id is required for handle, range, symbol, history, and changes reads", map[string]any{})
		}
		opened := h.open(ctx, requestID, map[string]any{"kind": "documents", "files": []any{path}})
		if opened["outcome"] != "ok" {
			return opened
		}
		identity, _ := opened["workspace"].(workspacecore.Identity)
		implicitID := strings.TrimSpace(string(identity.ID))
		if implicitID == "" {
			return modernEnvelope(requestID, nil, "failed", "workspace_open_failed", "implicit document workspace did not return an ID", map[string]any{})
		}
		arguments["workspace_id"] = implicitID
		result := h.execute(ctx, requestID, name, arguments)
		result["warnings"] = append(result["warnings"].([]string), "Implicitly opened an exact one-document workspace; reuse the returned workspace_id and revision for guarded edits.")
		if data, ok := result["data"].(map[string]any); ok {
			data["implicit_workspace"] = true
		}
		return result
	}
	workspace := h.registry.lookup(workspacecore.ID(workspaceID))
	if workspace == nil {
		return modernEnvelope(requestID, nil, "failed", "workspace_not_found", "Unknown or missing workspace_id", map[string]any{"workspace_id": workspaceID})
	}
	// Provider-touching calls are serialised by the scheduler lanes in
	// executeScheduled, and plans stage in isolated sandboxes with their own
	// providers, so the canonical provider never shows a staged view. The
	// workspace-level CheckProviderAccess lease is a no-op today and the bridge
	// deliberately advertises no workspace_busy guard for provider reads; the
	// only workspace_busy result comes from PreparePlan when the same plan is
	// already preparing.
	switch name {
	case "language_server_status":
		return h.languageServerStatus(ctx, requestID, workspace)
	case "language_server_setup":
		return h.languageServerSetup(ctx, requestID, workspace, arguments)
	case "navigate":
		return h.navigateProvider(ctx, requestID, workspace, arguments)
	case "code_actions":
		return h.codeActionsProvider(ctx, requestID, workspace, arguments)
	case "workspace_inspect":
		if _, refreshErr := workspace.RefreshKnownDocuments(); refreshErr != nil {
			return modernFailure(requestID, workspace, "workspace_refresh_failed", refreshErr)
		}
		inspection := workspace.Inspect()
		var semanticProvider map[string]any
		if backend, providerErr := h.pool.canonical(ctx, workspace); providerErr == nil {
			inspection.Optional["provider"] = "available"
			inspection.Optional["lsp"] = "probe_with_language_server_status"
			semanticProvider = canonicalProviderStatus(ctx, backend)
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
		reconcilePipelineCapabilities(&inspection, policy)
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
		return h.search(requestID, workspace, arguments)
	case "symbol_find":
		return h.symbolFind(ctx, requestID, workspace, arguments)
	case "read":
		return h.read(ctx, requestID, workspace, arguments)
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
		full, _ := arguments["full"].(bool)
		result := modernEnvelope(requestID, workspace, outcome, "", summary, compactDiagnosticReport(report, outcome, full))
		ids := append([]string(nil), report.EvidenceIDs...)
		sort.Strings(ids)
		result["evidence"] = map[string]any{"ids": nonNilStrings(uniqueStrings(ids)), "truncated": false}
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
		return h.edit(ctx, requestID, workspace, arguments)
	case "change_plan":
		return h.changePlan(ctx, requestID, workspace, arguments)
	case "verify_run":
		return h.verify(ctx, requestID, workspace, arguments)
	case "revision_diff":
		return h.revisionDiff(requestID, workspace, arguments)
	case "debug_session", "debug_breakpoints", "debug_control", "debug_inspect":
		return h.debug(ctx, requestID, name, workspace, arguments)
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
