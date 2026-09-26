package workspace

import (
	"os"
	"os/exec"
	"testing"
)

// Liveness is judged by PID and start time together: this process is alive,
// the same PID with another start time is a recycled PID and counts as dead,
// and a process that has exited is dead.
func TestProcessOwnerAliveUsesStartTime(t *testing.T) {
	start := processStartToken(os.Getpid())
	if start == "" {
		t.Fatal("no start time for this process")
	}
	if !processOwnerAlive(os.Getpid(), start) {
		t.Fatal("this process was reported dead")
	}
	if processOwnerAlive(os.Getpid(), start+"0") {
		t.Fatal("a recycled PID was reported alive")
	}
	command := exec.Command("true")
	if err := command.Run(); err != nil {
		t.Skipf("cannot run true: %v", err)
	}
	if processOwnerAlive(command.Process.Pid, "1") {
		t.Fatal("an exited process was reported alive")
	}
}
