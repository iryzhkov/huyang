package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// defaultToolCallTimeout bounds every tool call and every provider call
// that carries no deadline of its own.
const defaultToolCallTimeout = 2 * time.Minute

var errGlobalToolCallTimeout = errors.New("global tool-call timeout exceeded")

type directWorkspaces struct {
	mu          sync.RWMutex
	items       map[workspacecore.ID]*workspacecore.Workspace
	records     map[workspacecore.ID]persistedWorkspace
	stateDir    string
	toolTimeout time.Duration
	requests    atomic.Uint64

	replayMu      sync.Mutex
	replays       map[string]*directReplay
	receiptLimits receiptLimits

	persistMu    sync.Mutex
	registryPath string
	loadErr      error
	scheduler    *workspaceScheduler

	// providerMu guards only the two maps below; provider and stager
	// operations run outside it (see providerSlot and sandboxPlanStager).
	providerMu     sync.Mutex
	providers      map[workspacecore.ID]*providerSlot
	sandboxStagers map[stagerKey]*sandboxPlanStager

	verificationMu    sync.Mutex
	verificationCache map[string]cachedVerification
	verificationOrder []string

	notices *noticeDelivery
}

type replayCheckpoint func(map[string]any) error

type replayCheckpointKey struct{}

func checkpointStatefulReceipt(ctx context.Context, result map[string]any) error {
	checkpoint, _ := ctx.Value(replayCheckpointKey{}).(replayCheckpoint)
	if checkpoint == nil {
		return nil
	}
	return checkpoint(result)
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
		receiptLimits:     defaultReceiptLimits(),
		registryPath:      filepath.Join(stateDir, "registry.json"),
		scheduler:         newWorkspaceScheduler(providerQuota, externalJobQuota),
		providers:         make(map[workspacecore.ID]*providerSlot),
		sandboxStagers:    make(map[stagerKey]*sandboxPlanStager),
		verificationCache: make(map[string]cachedVerification),
		notices:           newNoticeDelivery(),
	}
	direct.loadErr = direct.loadRegistry()
	if direct.loadErr == nil {
		direct.loadErr = workspacecore.ReapSandboxes(filepath.Join(stateDir, "sandboxes"), nil)
	}
	return direct
}

func (d *directWorkspaces) call(ctx context.Context, name string, arguments map[string]any) map[string]any {
	return finalizeEnvelope(name, d.callUnfinalized(ctx, name, arguments))
}

func (d *directWorkspaces) callUnfinalized(ctx context.Context, name string, arguments map[string]any) map[string]any {
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
			d.replayMu.Lock()
			evicted, trimmed, stored := previous.evicted, previous.trimmed, previous.result
			d.replayMu.Unlock()
			if evicted {
				result := modernEnvelope(requestID, nil, "conflict", receiptEvictedCode, receiptEvictedSummary, map[string]any{
					"tool": name, "idempotency_key": idempotencyKey,
				})
				result["next"] = []any{map[string]any{
					"tool": "workspace_inspect", "view": "status", "action": "confirm_current_revision_before_retrying_with_a_new_key",
				}}
				return result
			}
			replayed := cloneEnvelope(stored)
			replayed["request_id"] = requestID
			replayed["idempotency"] = "replayed"
			replayed["summary"] = "Idempotent replay returned the original receipt; this call made no new mutation"
			if originalData, ok := stored["data"].(map[string]any); ok {
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
			if trimmed {
				replayed["warnings"] = []string{receiptTrimmedWarning}
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

	ctx = context.WithValue(ctx, replayCheckpointKey{}, replayCheckpoint(func(receipt map[string]any) error {
		persisted := cloneEnvelope(receipt)
		persisted["idempotency"] = "created"
		persisted["idempotency_persisted"] = true
		d.replayMu.Lock()
		pending.checkpointed = true
		d.replayMu.Unlock()
		if err := d.storeReceipt(replayKey, pending, persisted); err != nil {
			d.replayMu.Lock()
			pending.complete = false
			pending.checkpointed = false
			d.replayMu.Unlock()
			return err
		}
		return nil
	}))
	result := d.executeScheduled(ctx, requestID, name, arguments)
	timedOut := normalizeToolTimeout(ctx, d.toolTimeout, name, result)
	if timedOut {
		d.replayMu.Lock()
		if pending.checkpointed {
			result = cloneEnvelope(pending.result)
			result["request_id"] = requestID
			result["outcome"] = "provisional"
			result["warnings"] = append(result["warnings"].([]string),
				"Post-mutation work exceeded the tool timeout; the canonical mutation receipt was already persisted.")
			timedOut = false
		}
		d.replayMu.Unlock()
	}
	result["idempotency"] = "created"
	result["idempotency_persisted"] = !timedOut
	if timedOut {
		d.replayMu.Lock()
		delete(d.replays, replayKey)
		close(pending.done)
		d.replayMu.Unlock()
		return result
	}
	if err := d.storeReceipt(replayKey, pending, cloneEnvelope(result)); err != nil {
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
	if name == "workspace_open" || (name == "read" && strings.TrimSpace(fmt.Sprint(arguments["workspace_id"])) == "") {
		// No workspace lane exists before the workspace ID is known; the
		// provider spawn inside open is serialised by the provider slot.
		return d.execute(ctx, requestID, name, arguments)
	}
	workspaceID, _ := arguments["workspace_id"].(string)
	var result map[string]any
	if name == "verify_run" {
		result = d.executeVerify(ctx, requestID, workspaceID, arguments)
	} else {
		class := classForCall(name, arguments)
		release, err := d.scheduler.acquire(ctx, workspaceID, class)
		if err != nil {
			return schedulerCancelled(requestID, workspaceID, class, err)
		}
		result = d.execute(ctx, requestID, name, arguments)
		release()
	}
	if name != "diagnostics" {
		if workspace := d.get(workspacecore.ID(workspaceID)); workspace != nil {
			d.attachDiagnosticUpdates(ctx, workspace, result)
		}
	}
	if !isStatefulModernTool(name) {
		if err := d.persistWorkspaceIdentity(workspacecore.ID(workspaceID)); err != nil {
			result["warnings"] = append(result["warnings"].([]string), "workspace state was not persisted: "+err.Error())
		}
	}
	return result
}

func schedulerCancelled(requestID, workspaceID string, class schedulerClass, err error) map[string]any {
	return modernEnvelope(requestID, nil, "failed", "scheduler_wait_cancelled", err.Error(), map[string]any{
		"workspace_id": workspaceID,
		"class":        class,
	})
}

// executeVerify runs verify_run in two scheduler phases: the canonical
// document refresh and stager recovery hold the workspace lane, then the
// pipeline itself runs as an external job without blocking the workspace.
func (d *directWorkspaces) executeVerify(ctx context.Context, requestID, workspaceID string, arguments map[string]any) map[string]any {
	if err := ctx.Err(); err != nil {
		return modernEnvelope(requestID, nil, "failed", "request_cancelled", err.Error(), map[string]any{})
	}
	workspace := d.get(workspacecore.ID(workspaceID))
	if workspace == nil {
		return modernEnvelope(requestID, nil, "failed", "workspace_not_found", "Unknown or missing workspace_id", map[string]any{"workspace_id": workspaceID})
	}
	releaseLane, err := d.scheduler.acquire(ctx, workspaceID, scheduleCanonicalWrite)
	if err != nil {
		return schedulerCancelled(requestID, workspaceID, scheduleCanonicalWrite, err)
	}
	job, early := d.verifyPrepare(ctx, requestID, workspace, arguments)
	releaseLane()
	if early != nil {
		return early
	}
	releaseJob, err := d.scheduler.acquire(ctx, workspaceID, scheduleExternalJob)
	if err != nil {
		return schedulerCancelled(requestID, workspaceID, scheduleExternalJob, err)
	}
	defer releaseJob()
	return d.verifyRun(ctx, requestID, workspace, job)
}

func (d *directWorkspaces) execute(ctx context.Context, requestID, name string, arguments map[string]any) map[string]any {
	if err := ctx.Err(); err != nil {
		return modernEnvelope(requestID, nil, "failed", "request_cancelled", err.Error(), map[string]any{})
	}
	if name == "workspace_open" {
		return d.open(ctx, requestID, arguments)
	}
	workspaceID, _ := arguments["workspace_id"].(string)
	if name == "read" && strings.TrimSpace(workspaceID) == "" {
		target, _ := arguments["target"].(map[string]any)
		path, _ := target["path"].(string)
		if strings.TrimSpace(path) == "" {
			return modernEnvelope(requestID, nil, "failed", "workspace_required", "workspace_id is required for handle, range, symbol, history, and changes reads", map[string]any{})
		}
		opened := d.open(ctx, requestID, map[string]any{"kind": "documents", "files": []any{path}})
		if opened["outcome"] != "ok" {
			return opened
		}
		identity, _ := opened["workspace"].(workspacecore.Identity)
		implicitID := strings.TrimSpace(string(identity.ID))
		if implicitID == "" {
			return modernEnvelope(requestID, nil, "failed", "workspace_open_failed", "implicit document workspace did not return an ID", map[string]any{})
		}
		arguments["workspace_id"] = implicitID
		result := d.execute(ctx, requestID, name, arguments)
		result["warnings"] = append(result["warnings"].([]string), "Implicitly opened an exact one-document workspace; reuse the returned workspace_id and revision for guarded edits.")
		if data, ok := result["data"].(map[string]any); ok {
			data["implicit_workspace"] = true
		}
		return result
	}
	workspace := d.get(workspacecore.ID(workspaceID))
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
			"service_limits": map[string]any{"tool_call_timeout_ms": d.toolTimeout.Milliseconds()},
			"inspection":     inspection, "semantic_provider": semanticProvider, "scheduler": d.scheduler.description(), "pipeline_policy": policy,
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
		return d.search(requestID, workspace, arguments)
	case "symbol_find":
		return d.symbolFind(ctx, requestID, workspace, arguments)
	case "read":
		return d.read(ctx, requestID, workspace, arguments)
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

func (d *directWorkspaces) get(id workspacecore.ID) *workspacecore.Workspace {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.items[id]
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
