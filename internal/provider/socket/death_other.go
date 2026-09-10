//go:build !linux

package socket

import "os/exec"

func setDeathSignal(command *exec.Cmd) {}
