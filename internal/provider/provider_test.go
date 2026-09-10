package provider

import (
	"context"
	"testing"
)

type fakeProvider struct {
	descriptor Descriptor
}

func (f *fakeProvider) Descriptor() Descriptor { return f.descriptor }
func (f *fakeProvider) Health(context.Context) Health {
	return Health{State: HealthHealthy}
}
func (f *fakeProvider) Call(context.Context, Request) (Result, error) { return Result{}, nil }
func (f *fakeProvider) Save(context.Context) error                    { return nil }
func (f *fakeProvider) Close(context.Context) error                   { return nil }
func (f *fakeProvider) Done() <-chan struct{}                         { return make(chan struct{}) }

func fake(id ID, capabilities ...Capability) Provider {
	return &fakeProvider{descriptor: Descriptor{ID: id, Capabilities: capabilities}}
}

func TestAnalysisProfileRoutesPrimaryComplementaryAndFallback(t *testing.T) {
	primary := fake("tsserver_primary", CapabilityNavigation, CapabilityDiagnostics)
	complement := fake("eslint_complement", CapabilityDiagnostics)
	fallbackB := fake("z_fallback", CapabilityNavigation)
	fallbackA := fake("a_fallback", CapabilityNavigation)

	profile, err := NewAnalysisProfile("default", []Registration{
		{Provider: fallbackB, Languages: []string{"typescript"}, Capabilities: []Capability{CapabilityNavigation}, Role: RoleFallback, FallbackFor: "tsserver_primary"},
		{Provider: complement, Languages: []string{"typescript"}, Capabilities: []Capability{CapabilityDiagnostics}, Role: RoleComplementary},
		{Provider: primary, Languages: []string{"typescript"}, Role: RolePrimary},
		{Provider: fallbackA, Languages: []string{"typescript"}, Capabilities: []Capability{CapabilityNavigation}, Role: RoleFallback, FallbackFor: "tsserver_primary"},
	})
	if err != nil {
		t.Fatal(err)
	}

	navigation, err := profile.Route(CapabilityNavigation, "typescript")
	if err != nil {
		t.Fatal(err)
	}
	if got := navigation.Primary.Descriptor().ID; got != "tsserver_primary" {
		t.Fatalf("primary = %q", got)
	}
	if len(navigation.Complementary) != 0 {
		t.Fatalf("navigation complementary = %d", len(navigation.Complementary))
	}
	if got := []ID{navigation.Fallbacks[0].Descriptor().ID, navigation.Fallbacks[1].Descriptor().ID}; got[0] != "a_fallback" || got[1] != "z_fallback" {
		t.Fatalf("fallback order = %v", got)
	}

	diagnostics, err := profile.Route(CapabilityDiagnostics, "typescript")
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.Complementary) != 1 || diagnostics.Complementary[0].Descriptor().ID != "eslint_complement" {
		t.Fatalf("diagnostic complements = %#v", diagnostics.Complementary)
	}
}

func TestAnalysisProfileRejectsAmbiguousOrUnknownRoutes(t *testing.T) {
	a := fake("a", CapabilityNavigation)
	b := fake("b", CapabilityNavigation)
	profile, err := NewAnalysisProfile("ambiguous", []Registration{
		{Provider: a, Role: RolePrimary},
		{Provider: b, Role: RolePrimary},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.Route(CapabilityNavigation, "go"); err == nil {
		t.Fatal("multiple primaries accepted")
	}

	if _, err := NewAnalysisProfile("bad-fallback", []Registration{{
		Provider: a, Role: RoleFallback, FallbackFor: "missing",
	}}); err == nil {
		t.Fatal("fallback to unknown provider accepted")
	}
}

func TestAnalysisProfileCopiesRoutingInputs(t *testing.T) {
	p := fake("primary", CapabilityExecute)
	languages := []string{"go"}
	capabilities := []Capability{CapabilityExecute}
	profile, err := NewAnalysisProfile("default", []Registration{{
		Provider: p, Languages: languages, Capabilities: capabilities, Role: RolePrimary,
	}})
	if err != nil {
		t.Fatal(err)
	}
	languages[0] = "typescript"
	capabilities[0] = CapabilityDiagnostics
	if _, err := profile.Route(CapabilityExecute, "go"); err != nil {
		t.Fatalf("profile retained caller-owned slices: %v", err)
	}
}
