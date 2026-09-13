//go:build live

package livetest

// ledgerTarget is the read target for the fixture's ledger file, which every
// prepared-revision probe reads. It names the one repeated literal in these
// tests so the call sites say what they read rather than how the target is
// shaped.
func ledgerTarget() map[string]any {
	return map[string]any{"path": "ledger.go"}
}
