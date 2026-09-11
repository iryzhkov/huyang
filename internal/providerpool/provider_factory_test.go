package providerpool

import "testing"

func TestProviderBackendDefaultsToEmbedded(t *testing.T) {
	t.Setenv("HUYANG_PROVIDER_BACKEND", "")
	t.Setenv("AGENT99_PROVIDER_BACKEND", "")
	if got := providerBackend(); got != "" {
		t.Fatalf("providerBackend() = %q, want empty default selecting embed", got)
	}
}

func TestProviderBackendPrefersHuyangName(t *testing.T) {
	t.Setenv("HUYANG_PROVIDER_BACKEND", "embed")
	t.Setenv("AGENT99_PROVIDER_BACKEND", "other")
	if got := providerBackend(); got != "embed" {
		t.Fatalf("providerBackend() = %q, want embed", got)
	}
}

func TestProviderBackendAcceptsMigrationAlias(t *testing.T) {
	t.Setenv("HUYANG_PROVIDER_BACKEND", "")
	t.Setenv("AGENT99_PROVIDER_BACKEND", "embed")
	if got := providerBackend(); got != "embed" {
		t.Fatalf("providerBackend() = %q, want embed", got)
	}
}
