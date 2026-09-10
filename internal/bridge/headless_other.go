//go:build !linux

package bridge

import "os/exec"

// No parent-death signal outside Linux; the explicit cleanup on server
// exit still applies.
func setDeathSignal(cmd *exec.Cmd) {}
