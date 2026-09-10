package socket

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
)

func TestAttachedProviderHasDeterministicIdentityAndExplicitCapabilities(t *testing.T) {
	first := Attach("/workspace", "/tmp/one.sock")
	second := Attach("/workspace", "/tmp/two.sock")
	other := Attach("/other", "/tmp/one.sock")

	if first.Descriptor().ID != second.Descriptor().ID {
		t.Fatalf("same configuration IDs differ: %q != %q",
			first.Descriptor().ID, second.Descriptor().ID)
	}
	if first.Descriptor().ID == other.Descriptor().ID {
		t.Fatalf("different roots share ID %q", first.Descriptor().ID)
	}
	if first.Descriptor().Backend != "socket" {
		t.Fatalf("backend = %q", first.Descriptor().Backend)
	}
	if len(first.Descriptor().Capabilities) == 0 {
		t.Fatal("socket provider advertises no capabilities")
	}
	health := first.Health(context.Background())
	if health.State != provider.HealthFailed ||
		health.Cancellation != provider.CancellationUnsupported {
		t.Fatalf("health = %#v", health)
	}
}

func TestRootPrefixKeepsLegacySocketNaming(t *testing.T) {
	root := "/workspace"
	sum := sha1.Sum([]byte(root))
	want := hex.EncodeToString(sum[:6])
	if got := rootPrefix(root); got != want {
		t.Fatalf("rootPrefix(%q) = %q, want %q", root, got, want)
	}
}
