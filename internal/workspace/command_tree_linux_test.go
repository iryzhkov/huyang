//go:build linux

package workspace

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const treeScript = "sleep 300 & echo $!\nwait"

// treeProcessGone reports whether pid no longer runs; a zombie counts as gone.
func treeProcessGone(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
	return len(fields) == 0 || fields[0] == "Z" || fields[0] == "X"
}

func readGrandchild(t *testing.T, reader io.Reader) int {
	t.Helper()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		if pid, err := strconv.Atoi(strings.TrimSpace(scanner.Text())); err == nil {
			t.Cleanup(func() {
				if !treeProcessGone(pid) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			return pid
		}
	}
	t.Fatalf("no grandchild pid: %v", scanner.Err())
	return 0
}

func waitTreeGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !treeProcessGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("verification grandchild %d outlived its owner", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCommandTreeOwnerHelper is the owner process for
// TestCommandTreeDiesWithItsOwner; it does nothing in a normal run.
func TestCommandTreeOwnerHelper(t *testing.T) {
	if os.Getenv("HUYANG_TEST_COMMAND_TREE_OWNER") == "" {
		t.Skip("helper process only")
	}
	command := exec.CommandContext(context.Background(), "sh", "-c", treeScript)
	command.Stdout = os.Stdout
	_ = runCommandTree(command)
	os.Exit(0)
}

// A verification command's tree must die when the process running it is
// SIGKILLed, which cancels no context.
func TestCommandTreeDiesWithItsOwner(t *testing.T) {
	owner := exec.Command(os.Args[0], "-test.run=^TestCommandTreeOwnerHelper$")
	owner.Env = append(os.Environ(), "HUYANG_TEST_COMMAND_TREE_OWNER=1")
	stdout, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	grandchild := readGrandchild(t, stdout)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	waitTreeGone(t, grandchild)
}

func TestCommandTreeCancelKillsGrandchildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	command := exec.CommandContext(ctx, "sh", "-c", treeScript)
	command.Stdout = writer
	result := make(chan error, 1)
	go func() { result <- runCommandTree(command) }()
	grandchild := readGrandchild(t, reader)
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled command reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled command did not return")
	}
	// Closed only once the command has returned: the start reads the
	// descriptor, and closing it earlier races with that read.
	_ = writer.Close()
	waitTreeGone(t, grandchild)
}
