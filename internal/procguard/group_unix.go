//go:build unix && !linux

package procguard

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(command *exec.Cmd) {
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
		_ = command.Process.Kill()
	}
}
