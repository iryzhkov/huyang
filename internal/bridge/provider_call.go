package bridge

import (
	"context"
	"errors"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// openReferenceProvider starts one owned Neovim provider rooted at root with
// the shipped runtime and the configured headless init file. Every provider
// the bridge spawns, canonical, debug, sandbox or verification, goes through
// this constructor so the configuration cannot drift between call sites.
func openReferenceProvider(root string, debug bool) (provider.Provider, error) {
	return referenceProviders.Open(providerOpenConfig{
		Root: root, InitFile: huyangHeadlessInit(),
		RuntimePath: shippedRuntimePath(), Debug: debug,
	})
}

// providerCall names one request to a provider. TransactionID is set for
// plan-scoped calls so the provider can label staged views; canonical calls
// leave it empty.
type providerCall struct {
	RequestID     string
	TransactionID string
	Timeout       time.Duration
}

// callProvider is the single request-context assembly for provider calls. It
// applies the default timeout when the caller context carries no deadline,
// stamps the workspace and provider epoch on the request, and resynchronises
// the workspace epoch with the provider afterwards.
func callProvider(ctx context.Context, workspace *workspacecore.Workspace, backend provider.Provider, call providerCall, operation string, arguments map[string]any) (provider.Result, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok && call.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, call.Timeout)
		defer cancel()
	}
	deadline, _ := ctx.Deadline()
	descriptor := backend.Descriptor()
	result, err := backend.Call(ctx, provider.Request{
		Context: provider.RequestContext{
			RequestID: call.RequestID, WorkspaceID: string(workspace.Identity().ID),
			Epoch: descriptor.Epoch, TransactionID: call.TransactionID,
			Deadline: deadline, Cancellation: descriptor.Cancellation,
		},
		Operation: operation, Arguments: arguments,
	})
	workspace.SyncProviderEpoch(backend.Descriptor().Epoch)
	return result, err
}

// callCanonicalProvider issues one canonical provider call bounded by the
// default tool-call timeout. A transaction_id argument, when the caller asks
// for a staged view, is carried on the request context so the kernel can
// label the view it serves.
func callCanonicalProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, backend provider.Provider, operation string, arguments map[string]any) (any, error) {
	transactionID, _ := arguments["transaction_id"].(string)
	result, err := callProvider(ctx, workspace, backend, providerCall{
		RequestID: requestID, TransactionID: transactionID, Timeout: defaultToolCallTimeout,
	}, operation, arguments)
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

// modernProviderFailure maps a kernel operation failure onto a tool result
// using the provider's stable error code rather than its message text. The
// fallback code names the operation that failed when the kernel gave none.
func modernProviderFailure(requestID string, workspace *workspacecore.Workspace, fallbackCode string, err error) map[string]any {
	switch provider.ErrorCode(err) {
	case "workspace_busy":
		return modernEnvelope(requestID, workspace, "conflict", "workspace_busy", err.Error(), map[string]any{})
	case "lsp_not_configured":
		result := modernEnvelope(requestID, workspace, "unavailable", "language_server_unavailable", err.Error(), map[string]any{
			"coverage": map[string]any{"complete": false, "unavailable": []string{"language_server"}},
		})
		result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "inspect_attachment_and_install_options"}}
		return result
	case "provider_cancelled":
		return modernEnvelope(requestID, workspace, "failed", "request_cancelled", err.Error(), map[string]any{})
	}
	var failure *provider.Failure
	if errors.As(err, &failure) && failure.Code != "" {
		return modernFailure(requestID, workspace, string(failure.Code), err)
	}
	return modernFailure(requestID, workspace, fallbackCode, err)
}

// canonicalProviderStatus reports the provider descriptor and its health as
// observed within the request deadline.
func canonicalProviderStatus(ctx context.Context, backend provider.Provider) map[string]any {
	if ctx == nil {
		ctx = context.Background()
	}
	descriptor := backend.Descriptor()
	health := backend.Health(ctx)
	return map[string]any{
		"state": health.State, "detail": health.Detail, "failure_code": health.FailureCode,
		"backend": descriptor.Backend, "provider_id": descriptor.ID,
		"epoch": descriptor.Epoch, "process_id": descriptor.ProcessID,
		"endpoint": descriptor.Endpoint, "languages": descriptor.Languages,
		"capabilities": descriptor.Capabilities, "observed_at": health.ObservedAt,
	}
}
