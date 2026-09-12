package handlers

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// WorkspaceLookup is the handlers' view of the workspace registry: find an
// open workspace, register a newly opened one, and persist an identity that
// moved. The service owns the registry and its durable file.
type WorkspaceLookup interface {
	Lookup(id workspacecore.ID) *workspacecore.Workspace
	Adopt(opened *workspacecore.Workspace, files []string) (*workspacecore.Workspace, bool, error)
	PersistIdentity(id workspacecore.ID) error
}

// RevisionProvenance answers the two receipt-backed questions the handlers
// ask: which native edits produced the revisions in a range, and which
// paths changed at one revision.
type RevisionProvenance interface {
	RecordedRevisionDiffs(workspaceID string, fromSeq, toSeq uint64) []RecordedRevisionDiff
	CanonicalChangedPaths(workspaceID workspacecore.ID, target uint64) ([]string, error)
}

// RecordedRevisionDiff is one native edit receipt in revision order.
type RecordedRevisionDiff struct {
	From, To                  uint64
	Path, BeforeSHA, AfterSHA string
	Diff                      any
}

// ReplayCheckpoint persists an early receipt for a canonical mutation before
// its post-mutation work runs. The service installs it in the request
// context of every stateful call; edit_apply invokes it once the canonical
// bytes are written.
type ReplayCheckpoint func(map[string]any) error

type replayCheckpointKey struct{}

func WithReceiptCheckpoint(ctx context.Context, checkpoint ReplayCheckpoint) context.Context {
	return context.WithValue(ctx, replayCheckpointKey{}, checkpoint)
}

func checkpointStatefulReceipt(ctx context.Context, result map[string]any) error {
	checkpoint, _ := ctx.Value(replayCheckpointKey{}).(ReplayCheckpoint)
	if checkpoint == nil {
		return nil
	}
	return checkpoint(result)
}

// Handlers implements every tool over the workspace core and the
// provider pool. It holds no replay or scheduling state: the service
// decides when a handler runs, the handlers decide what it does.
type Handlers struct {
	registry     WorkspaceLookup
	provenance   RevisionProvenance
	pool         *providerpool.Pool
	verification *verificationCache
	notices      *noticeDelivery
	stateDir     string
	toolTimeout  time.Duration
	// schedulerInfo describes the scheduler classes and quotas for
	// workspace_inspect; the scheduler itself belongs to the service.
	schedulerInfo func() map[string]any
}

// Config wires the handlers to what the service owns.
type Config struct {
	Registry   WorkspaceLookup
	Provenance RevisionProvenance
	Pool       *providerpool.Pool
	StateDir   string
	// ToolTimeout is advertised in service_limits; the service enforces it.
	ToolTimeout   time.Duration
	SchedulerInfo func() map[string]any
}

func New(config Config) *Handlers {
	return &Handlers{
		registry:      config.Registry,
		provenance:    config.Provenance,
		pool:          config.Pool,
		verification:  newVerificationCache(),
		notices:       newNoticeDelivery(),
		stateDir:      config.StateDir,
		toolTimeout:   config.ToolTimeout,
		schedulerInfo: config.SchedulerInfo,
	}
}

// providerPool exposes the pool so the service can close it on shutdown.
func (h *Handlers) ProviderPool() *providerpool.Pool {
	return h.pool
}

// setToolTimeout changes the advertised tool-call timeout.
func (h *Handlers) SetToolTimeout(timeout time.Duration) {
	h.toolTimeout = timeout
}

// Execute runs one tool call against the workspace it names. workspace_open
// and a path-only read need no workspace ID; every other tool fails without
// a registered one.
func (h *Handlers) Execute(ctx context.Context, requestID, name string, arguments map[string]any) map[string]any {
	if err := ctx.Err(); err != nil {
		return mcpapi.Envelope(requestID, nil, "failed", "request_cancelled", err.Error(), map[string]any{})
	}
	if name == "workspace_open" {
		return h.open(ctx, requestID, arguments)
	}
	workspaceID, _ := arguments["workspace_id"].(string)
	if root, _ := arguments["root"].(string); strings.TrimSpace(workspaceID) == "" && strings.TrimSpace(root) != "" {
		if failure := h.adoptImplicitProject(ctx, requestID, root, arguments); failure != nil {
			return failure
		}
		workspaceID, _ = arguments["workspace_id"].(string)
	}
	if name == "read" && strings.TrimSpace(workspaceID) == "" {
		return h.readImplicitDocument(ctx, requestID, arguments)
	}
	if name == "edit_apply" && strings.TrimSpace(workspaceID) == "" {
		return h.editImplicitDocument(ctx, requestID, arguments)
	}
	workspace := h.registry.Lookup(workspacecore.ID(workspaceID))
	if workspace == nil {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_not_found", "Unknown or missing workspace_id", map[string]any{"workspace_id": workspaceID})
	}
	// Provider-touching calls are serialised by the scheduler lanes in the
	// service, and plans stage in isolated sandboxes with their own
	// providers, so the canonical provider never shows a staged view. The
	// workspace-level CheckProviderAccess lease is a no-op today and the
	// handlers deliberately advertise no workspace_busy guard for provider
	// reads; the only workspace_busy result comes from PreparePlan when the
	// same plan is already preparing.
	switch name {
	case "workspace_inspect":
		return h.inspect(ctx, requestID, workspace, arguments)
	case "search":
		return h.search(ctx, requestID, workspace, arguments)
	case "symbol_find":
		return h.symbolFind(ctx, requestID, workspace, arguments)
	case "read":
		return h.read(ctx, requestID, workspace, arguments)
	case "diagnostics":
		return diagnostics(requestID, workspace, arguments)
	case "evidence_get":
		return evidenceGet(requestID, workspace, arguments)
	case "edit_apply":
		return h.edit(ctx, requestID, workspace, arguments)
	case "change_plan":
		return h.changePlan(ctx, requestID, workspace, arguments)
	case "verify_run":
		return h.verify(ctx, requestID, workspace, arguments)
	case "revision_diff":
		return h.revisionDiff(requestID, workspace, arguments)
	case "language_server_status":
		return h.languageServerStatus(ctx, requestID, workspace)
	case "language_server_setup":
		return h.languageServerSetup(ctx, requestID, workspace, arguments)
	case "navigate":
		return h.navigateProvider(ctx, requestID, workspace, arguments)
	case "code_actions":
		return h.codeActionsProvider(ctx, requestID, workspace, arguments)
	case "debug_session", "debug_breakpoints", "debug_control", "debug_inspect":
		return h.debug(ctx, requestID, name, workspace, arguments)
	default:
		result := mcpapi.Envelope(requestID, workspace, "unavailable", "semantic_provider_unavailable",
			fmt.Sprintf("%s requires a semantic provider that is not available for this workspace", name),
			map[string]any{"tool": name, "coverage": map[string]any{"complete": false, "unavailable": []string{"semantic_provider"}}})
		result["next"] = []any{
			map[string]any{"tool": "search", "action": "literal_fallback"},
			map[string]any{"tool": "read", "action": "read_known_path"},
		}
		return result
	}
}

// AdoptProjectRoot lets the service resolve a root before it schedules the
// call; see adoptImplicitProject.
func (h *Handlers) AdoptProjectRoot(ctx context.Context, requestID string, arguments map[string]any) map[string]any {
	root, _ := arguments["root"].(string)
	return h.adoptImplicitProject(ctx, requestID, root, arguments)
}

// adoptImplicitProject serves any call that names a project root instead of
// a workspace_id: the project workspace is opened, or reused when it is
// already open, and its ID is written into the arguments so the call
// proceeds as if workspace_open had been called first. This saves the
// separate open call on every task that starts in a known repository.
func (h *Handlers) adoptImplicitProject(ctx context.Context, requestID, root string, arguments map[string]any) map[string]any {
	opened := h.open(ctx, requestID, map[string]any{"kind": "project", "root": root})
	if opened["outcome"] != "ok" {
		return opened
	}
	identity, _ := opened["workspace"].(workspacecore.Identity)
	if strings.TrimSpace(string(identity.ID)) == "" {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_open_failed", "implicit project workspace did not return an ID", map[string]any{})
	}
	arguments["workspace_id"] = string(identity.ID)
	delete(arguments, "root")
	return nil
}

// readImplicitDocument serves a read that names a path but no workspace by
// opening an exact one-document workspace first and reading through it.
func (h *Handlers) readImplicitDocument(ctx context.Context, requestID string, arguments map[string]any) map[string]any {
	target, _ := arguments["target"].(map[string]any)
	path, _ := target["path"].(string)
	if strings.TrimSpace(path) == "" {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_required", "workspace_id is required for handle, range, symbol, history, and changes reads", map[string]any{})
	}
	if !filepath.IsAbs(path) {
		// The service's working directory is not the agent's, so a relative
		// path without a workspace names nothing useful.
		return mcpapi.Envelope(requestID, nil, "failed", "invalid_target", "without workspace_id or root the path must be absolute; pass root for a repository file", map[string]any{"path": path})
	}
	opened := h.open(ctx, requestID, map[string]any{"kind": "documents", "files": []any{path}})
	if opened["outcome"] != "ok" {
		return opened
	}
	identity, _ := opened["workspace"].(workspacecore.Identity)
	implicitID := strings.TrimSpace(string(identity.ID))
	if implicitID == "" {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_open_failed", "implicit document workspace did not return an ID", map[string]any{})
	}
	arguments["workspace_id"] = implicitID
	result := h.Execute(ctx, requestID, "read", arguments)
	result["warnings"] = append(result["warnings"].([]string), "Implicitly opened an exact one-document workspace; reuse the returned workspace_id and revision for guarded edits.")
	if data, ok := result["data"].(map[string]any); ok {
		data["implicit_workspace"] = true
	}
	return result
}
