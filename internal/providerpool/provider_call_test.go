package providerpool

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type slowHealthProvider struct {
	stubProvider
}

func (p *slowHealthProvider) Health(ctx context.Context) provider.Health {
	<-ctx.Done()
	return provider.Health{State: provider.HealthFailed, Detail: ctx.Err().Error()}
}

func TestCanonicalProviderStatusHonoursRequestDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	status := Status(ctx, &slowHealthProvider{})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("status ignored the deadline: %s", elapsed)
	}
	if status["state"] != provider.HealthFailed {
		t.Fatalf("status = %#v", status)
	}
}

type recordingProvider struct {
	stubProvider
	mu       sync.Mutex
	requests []provider.Request
}

func (p *recordingProvider) Call(ctx context.Context, request provider.Request) (provider.Result, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	return p.stubProvider.Call(ctx, request)
}

func TestProviderCallsCarryOneRequestContextShape(t *testing.T) {
	root := t.TempDir()
	workspace, err := workspacecore.New(workspacecore.KindProject, root, 3)
	if err != nil {
		t.Fatal(err)
	}
	backend := &recordingProvider{}
	backend.descriptor = provider.Descriptor{ID: "record", Backend: "test", Epoch: 3, Root: root, Cancellation: provider.CancellationCooperative}

	if _, err := CallCanonical(context.Background(), "req_canonical", workspace, backend, "workspace_support", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), workspace, backend, CallSpec{
		RequestID: "req_debug", TransactionID: "tx_debug", Timeout: DefaultCallTimeout,
	}, "debug_threads", map[string]any{"transaction_id": "tx_debug"}); err != nil {
		t.Fatal(err)
	}
	stager := &providerPlanStager{workspace: workspace, provider: backend, epoch: 3}
	if err := stager.Commit(context.Background(), "plan_1"); err != nil {
		t.Fatal(err)
	}
	if len(backend.requests) != 3 {
		t.Fatalf("requests = %d", len(backend.requests))
	}
	for index, request := range backend.requests {
		if request.Context.WorkspaceID != string(workspace.Identity().ID) || request.Context.Epoch != 3 ||
			request.Context.Deadline.IsZero() || request.Context.Cancellation != provider.CancellationCooperative {
			t.Fatalf("request %d context = %#v", index, request.Context)
		}
	}
	if backend.requests[0].Context.RequestID != "req_canonical" || backend.requests[0].Context.TransactionID != "" {
		t.Fatalf("canonical context = %#v", backend.requests[0].Context)
	}
	if backend.requests[1].Context.TransactionID != "tx_debug" {
		t.Fatalf("debug context = %#v", backend.requests[1].Context)
	}
	if backend.requests[2].Context.TransactionID != "plan_1" {
		t.Fatalf("plan context = %#v", backend.requests[2].Context)
	}
}

// stubProvider answers every operation with an empty result.
type stubProvider struct {
	descriptor provider.Descriptor
}

func (p *stubProvider) Descriptor() provider.Descriptor { return p.descriptor }
func (p *stubProvider) Health(context.Context) provider.Health {
	return provider.Health{State: provider.HealthHealthy, Epoch: p.descriptor.Epoch}
}
func (p *stubProvider) Call(context.Context, provider.Request) (provider.Result, error) {
	return provider.Result{Value: map[string]any{}}, nil
}
func (p *stubProvider) Close(context.Context) error { return nil }
func (p *stubProvider) Done() <-chan struct{}       { return make(chan struct{}) }
