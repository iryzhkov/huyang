//go:build !unix

package procguard

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(command *exec.Cmd) { _ = command.Process.Kill() }
