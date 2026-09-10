//go:build !linux

package embed

import "os/exec"

func setDeathSignal(command *exec.Cmd) {}
