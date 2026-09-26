//go:build linux

package procguard

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as two helpers: an owner that guards a shell
// script and then blocks until it is killed, and an escapee that leaves its
// parent's process group and sleeps.
// The escapee inherits the owner's environment, so it is checked first.
func TestMain(m *testing.M) {
	if os.Getenv("PROCGUARD_TEST_ESCAPE") != "" {
		_ = syscall.Setpgid(0, 0)
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	}
	if script := os.Getenv("PROCGUARD_TEST_OWNER"); script != "" {
		os.Exit(runOwner(script))
	}
	os.Exit(m.Run())
}

func runOwner(script string) int {
	command := exec.Command("sh", "-c", script)
	command.Stdout = os.Stdout
	tree, err := Wrap(command)
	if err == nil {
		err = tree.Start()
	}
	if err != nil {
		return 1
	}
	_ = tree.Wait()
	return 0
}

// readPIDs reads count process ids, one per line.
func readPIDs(t *testing.T, reader io.Reader, count int) []int {
	t.Helper()
	scanner := bufio.NewScanner(reader)
	var pids []int
	for len(pids) < count && scanner.Scan() {
		if pid, err := strconv.Atoi(strings.TrimSpace(scanner.Text())); err == nil {
			pids = append(pids, pid)
		}
	}
	if len(pids) != count {
		t.Fatalf("read %d process ids, want %d: %v", len(pids), count, scanner.Err())
	}
	return pids
}

// gone reports whether a process no longer runs; a zombie waiting for init
// to reap it counts as gone.
func gone(pid int) bool {
	stat, err := readStat(pid)
	return err != nil || stat.state == 'Z' || stat.state == 'X'
}

func waitGone(t *testing.T, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for !gone(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d outlived its owner", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func killLater(t *testing.T, pids ...int) {
	t.Cleanup(func() {
		for _, pid := range pids {
			if !gone(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
}

func startTree(t *testing.T, script string) (*Guard, []int) {
	t.Helper()
	command := exec.Command("sh", "-c", script)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := Wrap(command)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Stop)
	pids := readPIDs(t, stdout, strings.Count(script, "echo $!"))
	killLater(t, pids...)
	return tree, pids
}

func TestOwnerSIGKILLKillsTheWholeTree(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	// A plain grandchild in the group, one that moved to its own group in
	// the same session, and one that detached into a new session.
	script := `sleep 300 & echo $!
PROCGUARD_TEST_ESCAPE=1 "$PROCGUARD_TEST_BIN" & echo $!
setsid sleep 300 & echo $!
wait`
	owner := exec.Command(self)
	owner.Env = append(os.Environ(), "PROCGUARD_TEST_OWNER="+script, "PROCGUARD_TEST_BIN="+self)
	stdout, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	pids := readPIDs(t, stdout, 3)
	killLater(t, pids...)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	waitGone(t, pids[0], pids[1])
	time.Sleep(50 * time.Millisecond)
	if gone(pids[2]) {
		t.Fatalf("the guard killed %d, which detached into its own session", pids[2])
	}
}

func TestStopKillsTheTreeAndWaitReturns(t *testing.T) {
	tree, pids := startTree(t, "sleep 300 & echo $!\nwait")
	tree.Stop()
	done := make(chan error, 1)
	go func() { done <- tree.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != -1 {
			t.Fatalf("Wait after Stop = %v; want death by signal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Stop")
	}
	waitGone(t, pids...)
}

func TestCommandExitKillsLeftoversAndKeepsItsStatus(t *testing.T) {
	tree, pids := startTree(t, "sleep 300 & echo $!\nexit 7")
	err := tree.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("Wait = %v; want exit status 7", err)
	}
	waitGone(t, pids...)
}

func TestWrapReportsAMissingExecutable(t *testing.T) {
	command := exec.Command(filepath.Join(t.TempDir(), "missing"))
	if _, err := Wrap(command); err == nil {
		t.Fatal("Wrap accepted a missing executable")
	}
}
