package bridge

import (
	"context"
	"testing"
	"time"

	"agent99/internal/provider"
)

type recordingProvider struct {
	descriptor provider.Descriptor
	request    provider.Request
}

func (p *recordingProvider) Descriptor() provider.Descriptor { return p.descriptor }
func (p *recordingProvider) Health(context.Context) provider.Health {
	return provider.Health{State: provider.HealthHealthy}
}
func (p *recordingProvider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	p.request = request
	return provider.Result{Value: map[string]any{"ok": true}}, nil
}
func (p *recordingProvider) Save(context.Context) error  { return nil }
func (p *recordingProvider) Close(context.Context) error { return nil }
func (p *recordingProvider) Done() <-chan struct{}       { return make(chan struct{}) }

func TestProviderCallRoutesThroughProfileWithRequestContext(t *testing.T) {
	backend := &recordingProvider{descriptor: provider.Descriptor{
		ID:           "recording",
		Cancellation: provider.CancellationUnsupported,
		Capabilities: []provider.Capability{provider.CapabilityExecute},
	}}
	profile, err := provider.NewAnalysisProfile("default", []provider.Registration{{
		Provider: backend,
		Role:     provider.RolePrimary,
	}})
	if err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{"file": "main.go"}
	before := time.Now()
	value, err := providerCall(session{
		Root:      "/workspace",
		Providers: profile,
		Provider:  backend,
		Client:    "client-a",
	}, "definition", arguments)
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["ok"] != true {
		t.Fatalf("result = %#v", value)
	}
	if backend.request.Operation != "definition" {
		t.Fatalf("operation = %q", backend.request.Operation)
	}
	if backend.request.Context.Actor != "client-a" ||
		backend.request.Context.Cancellation != provider.CancellationUnsupported {
		t.Fatalf("request context = %#v", backend.request.Context)
	}
	if backend.request.Context.RequestID == "" || !backend.request.Context.Deadline.After(before) {
		t.Fatalf("request lifetime = %#v", backend.request.Context)
	}
	if backend.request.Arguments["client"] != "client-a" {
		t.Fatalf("client argument = %#v", backend.request.Arguments)
	}
	if _, changed := arguments["client"]; changed {
		t.Fatal("providerCall mutated caller arguments")
	}
}

func TestProviderCallRequiresConfiguredPrimary(t *testing.T) {
	if _, err := providerCall(session{Root: "/workspace"}, "definition", nil); err == nil {
		t.Fatal("call without provider succeeded")
	}
}
