package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iryzhkov/huyang/internal/handlers"
	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

var errGlobalToolCallTimeout = errors.New("global tool-call timeout exceeded")

// directWorkspaces is the service-side dispatcher: it owns the workspace
// registry, the idempotency receipts and the scheduler, and runs every tool
// call through them before handing the call to the handlers.
type directWorkspaces struct {
	registry    *workspaceRegistry
	receipts    *receiptStore
	scheduler   *workspaceScheduler
	handlers    *handlers.Handlers
	toolTimeout time.Duration
	requests    atomic.Uint64
	loadErr     error
}

func newDirectWorkspaces(stateDir string) *directWorkspaces {
	return newDirectWorkspacesWithQuotas(stateDir, 4, 2)
}

func newDirectWorkspacesWithQuotas(stateDir string, providerQuota, externalJobQuota int) *directWorkspaces {
	registry := newWorkspaceRegistry(stateDir)
	receipts := newReceiptStore(stateDir, defaultReceiptLimits())
	scheduler := newWorkspaceScheduler(providerQuota, externalJobQuota)
	direct := &directWorkspaces{
		registry:    registry,
		receipts:    receipts,
		scheduler:   scheduler,
		toolTimeout: providerpool.DefaultCallTimeout,
		handlers: handlers.New(handlers.Config{
			Registry:      registry,
			Provenance:    receipts,
			Pool:          providerpool.New(filepath.Join(stateDir, "sandboxes"), providerpool.DefaultFactory),
			StateDir:      stateDir,
			ToolTimeout:   providerpool.DefaultCallTimeout,
			SchedulerInfo: scheduler.description,
		}),
	}
	direct.loadErr = direct.loadState()
	return direct
}

// loadState restores the registry and the receipts, migrating a version 1
// registry's embedded receipts into per-workspace files, and reaps sandboxes
// left by an earlier process.
func (d *directWorkspaces) loadState() error {
	legacy, migrate, err := d.registry.load()
	if err != nil {
		return err
	}
	if err := d.receipts.loadReceipts(); err != nil {
		return err
	}
	if migrate {
		if err := d.receipts.migrateLegacyReceipts(legacy); err != nil {
			return fmt.Errorf("migrate legacy receipts: %w", err)
		}
		if err := d.registry.persist(); err != nil {
			return fmt.Errorf("rewrite legacy registry: %w", err)
		}
	}
	return workspacecore.ReapSandboxes(d.handlers.ProviderPool().SandboxBaseDir(), nil)
}

// setToolTimeout changes the global tool-call timeout the dispatcher
// enforces and the handlers advertise.
func (d *directWorkspaces) setToolTimeout(timeout time.Duration) {
	d.toolTimeout = timeout
	d.handlers.SetToolTimeout(timeout)
}

func (d *directWorkspaces) get(id workspacecore.ID) *workspacecore.Workspace {
	return d.registry.Lookup(id)
}

func (d *directWorkspaces) closeProviders() {
	d.handlers.ProviderPool().Close()
}

func (d *directWorkspaces) call(ctx context.Context, name string, arguments map[string]any) map[string]any {
	return mcpapi.FinalizeEnvelope(name, d.callUnfinalized(ctx, name, arguments))
}

func (d *directWorkspaces) callUnfinalized(ctx context.Context, name string, arguments map[string]any) map[string]any {
	ctx, cancel := context.WithTimeoutCause(ctx, d.toolTimeout, errGlobalToolCallTimeout)
	defer cancel()
	requestID := fmt.Sprintf("req_%d", d.requests.Add(1))
	if d.loadErr != nil {
		return mcpapi.Envelope(requestID, nil, "failed", "service_state_unavailable", d.loadErr.Error(), map[string]any{})
	}
	if !isStatefulModernTool(name) {
		result := d.executeScheduled(ctx, requestID, name, arguments)
		normalizeToolTimeout(ctx, d.toolTimeout, name, result)
		return result
	}
	idempotencyKey, _ := arguments["idempotency_key"].(string)
	if idempotencyKey == "" {
		return mcpapi.Envelope(requestID, nil, "failed", "missing_idempotency_key", "Stateful calls require idempotency_key", map[string]any{})
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return mcpapi.Envelope(requestID, nil, "failed", "invalid_arguments", err.Error(), map[string]any{})
	}
	hash := sha256.Sum256(encoded)
	argumentsHash := fmt.Sprintf("%x", hash[:])
	workspaceID, _ := arguments["workspace_id"].(string)
	replayKey := workspaceID + "\x00" + name + "\x00" + idempotencyKey

	previous, pending := d.receipts.lookupOrBegin(replayKey, argumentsHash)
	if previous != nil {
		select {
		case <-previous.done:
			return d.replayReceipt(requestID, name, idempotencyKey, argumentsHash, previous)
		case <-ctx.Done():
			result := mcpapi.Envelope(requestID, nil, "failed", "request_cancelled", ctx.Err().Error(), map[string]any{})
			normalizeToolTimeout(ctx, d.toolTimeout, name, result)
			return result
		}
	}

	ctx = handlers.WithReceiptCheckpoint(ctx, func(receipt map[string]any) error {
		persisted := mcpapi.CloneEnvelope(receipt)
		persisted["idempotency"] = "created"
		persisted["idempotency_persisted"] = true
		return d.receipts.checkpoint(replayKey, pending, persisted)
	})
	result := d.executeScheduled(ctx, requestID, name, arguments)
	timedOut := normalizeToolTimeout(ctx, d.toolTimeout, name, result)
	if timedOut {
		if checkpointed, ok := d.receipts.checkpointedResult(pending); ok {
			result = mcpapi.CloneEnvelope(checkpointed)
			result["request_id"] = requestID
			result["outcome"] = "provisional"
			result["warnings"] = append(result["warnings"].([]string),
				"Post-mutation work exceeded the tool timeout; the canonical mutation receipt was already persisted.")
			timedOut = false
		}
	}
	result["idempotency"] = "created"
	result["idempotency_persisted"] = !timedOut
	if timedOut {
		d.receipts.abandon(replayKey, pending)
		return result
	}
	var stored map[string]any
	if err := d.receipts.storeReceipt(replayKey, pending, mcpapi.CloneEnvelope(result)); err != nil {
		result["warnings"] = append(result["warnings"].([]string), "idempotency receipt was not persisted: "+err.Error())
		result["idempotency_persisted"] = false
		stored = mcpapi.CloneEnvelope(result)
	}
	d.receipts.finish(pending, stored)
	return result
}

// replayReceipt answers a repeated stateful call from its recorded receipt:
// a conflict when the arguments differ or the receipt was evicted, and the
// original envelope, marked as a replay, otherwise.
func (d *directWorkspaces) replayReceipt(requestID, name, idempotencyKey, argumentsHash string, previous *directReplay) map[string]any {
	if previous.argumentsHash != argumentsHash {
		result := mcpapi.Envelope(requestID, nil, "conflict", "idempotency_key_reused",
			"Idempotency key was already used with different arguments; use a new key for a different request", map[string]any{
				"tool": name, "idempotency_key": idempotencyKey,
			})
		result["next"] = []any{map[string]any{
			"tool": name, "action": "retry_with_new_idempotency_key",
		}}
		return result
	}
	evicted, trimmed, stored := d.receipts.replaySnapshot(previous)
	if evicted {
		result := mcpapi.Envelope(requestID, nil, "conflict", receiptEvictedCode, receiptEvictedSummary, map[string]any{
			"tool": name, "idempotency_key": idempotencyKey,
		})
		result["next"] = []any{map[string]any{
			"tool": "workspace_inspect", "view": "status", "action": "confirm_current_revision_before_retrying_with_a_new_key",
		}}
		return result
	}
	replayed := mcpapi.CloneEnvelope(stored)
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
		return d.handlers.Execute(ctx, requestID, name, arguments)
	}
	workspaceID, _ := arguments["workspace_id"].(string)
	var result map[string]any
	if name == "verify_run" {
		result = d.executeVerify(ctx, requestID, workspaceID, arguments)
	} else {
		class := mcpapi.ClassForCall(name, arguments)
		release, err := d.scheduler.acquire(ctx, workspaceID, class)
		if err != nil {
			return schedulerCancelled(requestID, workspaceID, class, err)
		}
		result = d.handlers.Execute(ctx, requestID, name, arguments)
		release()
	}
	if name != "diagnostics" {
		if workspace := d.registry.Lookup(workspacecore.ID(workspaceID)); workspace != nil {
			d.handlers.AttachDiagnosticUpdates(ctx, workspace, result)
		}
	}
	if !isStatefulModernTool(name) {
		if err := d.registry.PersistIdentity(workspacecore.ID(workspaceID)); err != nil {
			result["warnings"] = append(result["warnings"].([]string), "workspace state was not persisted: "+err.Error())
		}
	}
	return result
}

func schedulerCancelled(requestID, workspaceID string, class mcpapi.SchedulerClass, err error) map[string]any {
	return mcpapi.Envelope(requestID, nil, "failed", "scheduler_wait_cancelled", err.Error(), map[string]any{
		"workspace_id": workspaceID,
		"class":        class,
	})
}

// executeVerify runs verify_run in two scheduler phases: the canonical
// document refresh and stager recovery hold the workspace lane, then the
// pipeline itself runs as an external job without blocking the workspace.
func (d *directWorkspaces) executeVerify(ctx context.Context, requestID, workspaceID string, arguments map[string]any) map[string]any {
	if err := ctx.Err(); err != nil {
		return mcpapi.Envelope(requestID, nil, "failed", "request_cancelled", err.Error(), map[string]any{})
	}
	workspace := d.registry.Lookup(workspacecore.ID(workspaceID))
	if workspace == nil {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_not_found", "Unknown or missing workspace_id", map[string]any{"workspace_id": workspaceID})
	}
	releaseLane, err := d.scheduler.acquire(ctx, workspaceID, mcpapi.ClassCanonicalWrite)
	if err != nil {
		return schedulerCancelled(requestID, workspaceID, mcpapi.ClassCanonicalWrite, err)
	}
	job, early := d.handlers.VerifyPrepare(ctx, requestID, workspace, arguments)
	releaseLane()
	if early != nil {
		return early
	}
	releaseJob, err := d.scheduler.acquire(ctx, workspaceID, mcpapi.ClassExternalJob)
	if err != nil {
		return schedulerCancelled(requestID, workspaceID, mcpapi.ClassExternalJob, err)
	}
	defer releaseJob()
	return d.handlers.VerifyRun(ctx, requestID, workspace, job)
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
