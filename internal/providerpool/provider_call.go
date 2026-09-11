package providerpool

import (
	"context"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// DefaultCallTimeout bounds every tool call and every provider call
// that carries no deadline of its own.
const DefaultCallTimeout = 2 * time.Minute

// CallSpec names one request to a provider. TransactionID is set for
// plan-scoped calls so the provider can label staged views; canonical calls
// leave it empty.
type CallSpec struct {
	RequestID     string
	TransactionID string
	Timeout       time.Duration
}

// Call is the single request-context assembly for provider calls. It
// applies the default timeout when the caller context carries no deadline,
// stamps the workspace and provider epoch on the request, and resynchronises
// the workspace epoch with the provider afterwards.
func Call(ctx context.Context, workspace *workspacecore.Workspace, backend provider.Provider, call CallSpec, operation string, arguments map[string]any) (provider.Result, error) {
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

// CallCanonical issues one canonical provider call bounded by the
// default tool-call timeout. A transaction_id argument, when the caller asks
// for a staged view, is carried on the request context so the kernel can
// label the view it serves.
func CallCanonical(ctx context.Context, requestID string, workspace *workspacecore.Workspace, backend provider.Provider, operation string, arguments map[string]any) (any, error) {
	transactionID, _ := arguments["transaction_id"].(string)
	result, err := Call(ctx, workspace, backend, CallSpec{
		RequestID: requestID, TransactionID: transactionID, Timeout: DefaultCallTimeout,
	}, operation, arguments)
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

// Status reports the provider descriptor and its health as
// observed within the request deadline.
func Status(ctx context.Context, backend provider.Provider) map[string]any {
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
