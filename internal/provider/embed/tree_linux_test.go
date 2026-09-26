//go:build linux

package embed

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/procguard"
)

// processGone reports whether pid no longer runs; a zombie counts as gone.
func processGone(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
	return len(fields) == 0 || fields[0] == "Z" || fields[0] == "X"
}

// A generation spawned the way startGenerationLocked spawns Neovim, but
// running a shell that starts a long-lived grandchild the way Neovim starts a
// language server: terminate must take the grandchild down with it.
func TestTerminateKillsTheProviderTree(t *testing.T) {
	command := exec.Command("sh", "-c", "sleep 300 & echo $!\nwait")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := procguard.Wrap(command)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		tree.Stop()
		t.Fatal(err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		tree.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !processGone(grandchild) {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
		}
	})
	g := &generation{command: command, tree: tree, done: make(chan error, 1)}
	g.alive.Store(true)
	(&Backend{}).terminate(g)
	_ = tree.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for !processGone(grandchild) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d outlived the terminated generation", grandchild)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
