//go:build !unix

package workspace

// processStartToken has no portable source outside Unix; the PID alone names
// the owner there.
func processStartToken(int) string { return "" }

// processOwnerAlive cannot tell a live process from a dead one here without
// platform code, so it always says alive and the sweep keeps every owner
// directory rather than risk removing one in use.
func processOwnerAlive(int, string) bool { return true }
