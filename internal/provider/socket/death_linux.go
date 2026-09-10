//go:build linux

package socket

import (
	"os/exec"
	"syscall"
)

func setDeathSignal(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
