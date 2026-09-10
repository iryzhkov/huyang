//go:build linux

package embed

import (
	"os/exec"
	"syscall"
)

func setDeathSignal(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
