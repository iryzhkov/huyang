package service

import (
	"fmt"
	"os"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// TestMain installs the output-schema validator on every envelope the package
// produces, so any test that drives a tool, directly or through a client
// session, fails when the result would violate the advertised schema.
func TestMain(m *testing.M) {
	mcpapi.SetEnvelopeAudit(func(tool string, envelope map[string]any) {
		if err := mcpapi.ValidateOutput(envelope); err != nil {
			panic(fmt.Sprintf("tool %s produced an envelope that violates the output schema: %v\n%#v", tool, err, envelope))
		}
	})
	// Every test spools into a scratch directory unless it names its own, so
	// the suite neither writes to nor applies retention to the developer's
	// real friction spool, which an `enabled` file there would otherwise arm.
	spool, err := os.MkdirTemp("", "huyang-friction-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HUYANG_FRICTION_DIR", spool)
	// Ambient opt-in settings must not turn ordinary test clients into
	// feedback producers. Spool tests explicitly opt in with their own dir.
	os.Setenv("HUYANG_FRICTION", "0")
	code := m.Run()
	os.RemoveAll(spool)
	os.Exit(code)
}

func assertModernOutputValid(t *testing.T, value map[string]any) {
	t.Helper()
	if err := mcpapi.ValidateOutput(value); err != nil {
		t.Fatalf("invalid modern output: %v\n%#v", err, value)
	}
}
