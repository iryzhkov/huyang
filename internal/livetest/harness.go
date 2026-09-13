//go:build live

// Package livetest drives the real service the way an agent does: the daemon
// started as its own process, reached through the `huyang mcp` adapter over a
// Unix socket, against a real repository on disk.
//
// Everything else in this repository tests the handlers in process, which is
// where the logic is. It is also where a test can agree with the code and
// still be wrong about the system: three defects in the refactor seam were
// found by hand against the live service after their in-process tests passed,
// because the stub had encoded a reply shape the kernel does not send. This
// package exists so that class of mistake fails a gate instead of a session.
//
// It is behind the `live` build tag: it starts processes, spawns language
// servers and takes seconds rather than milliseconds, so it runs from `make
// live` and not from `go test ./...`.
package livetest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startDeadline bounds waiting for the daemon's socket. The daemon listens
// before it indexes anything, so this is generous.
const startDeadline = 30 * time.Second

// live is one disposable installation: its own socket, state directory, home
// and friction spool, none of them shared with the developer's own service.
type live struct {
	t           *testing.T
	binary      string
	socketPath  string
	stateDir    string
	homeDir     string
	spoolDir    string
	environment []string
}

// start runs the daemon with a disposable home, which is a machine where no
// language server has ever been installed: the arm of the live gate that
// proves an answer without a server is explicitly incomplete rather than
// clean.
func start(t *testing.T) *live {
	return launch(t, true)
}

// startWithLanguageServers keeps the developer's home, so the servers
// installed on this machine attach and the semantic paths are exercised for
// real. State, socket and spool are still disposable.
func startWithLanguageServers(t *testing.T) *live {
	return launch(t, false)
}

func launch(t *testing.T, isolateHome bool) *live {
	t.Helper()
	binary := filepath.Join(repositoryRoot(t), "bin", "huyang")
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("bin/huyang is not built: %v; run make build", err)
	}
	base := t.TempDir()
	instance := &live{
		t: t, binary: binary,
		// A Unix socket path is bounded at around 100 bytes and a test
		// temporary directory is long, so the socket lives elsewhere.
		socketPath: shortSocketPath(t),
		stateDir:   filepath.Join(base, "state"),
		homeDir:    filepath.Join(base, "home"),
		spoolDir:   filepath.Join(base, "spool"),
	}
	for _, directory := range []string{instance.stateDir, instance.homeDir, instance.spoolDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	instance.environment = append(os.Environ(),
		"HUYANG_FRICTION=1",
		"HUYANG_FRICTION_DIR="+instance.spoolDir,
		"HUYANG_SESSION_ID=livetest",
	)
	if isolateHome {
		instance.environment = append(instance.environment,
			"HOME="+instance.homeDir,
			"XDG_CONFIG_HOME="+filepath.Join(instance.homeDir, ".config"),
			"XDG_DATA_HOME="+filepath.Join(instance.homeDir, ".local", "share"),
			"XDG_STATE_HOME="+filepath.Join(instance.homeDir, ".local", "state"),
		)
	}
	instance.serve()
	return instance
}

// serve starts the daemon and waits for its socket, then stops it when the
// test ends. Its output is kept and reported only when the test fails, so a
// passing run stays quiet and a failing one has the log.
func (l *live) serve() {
	l.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, l.binary, "serve", "--socket", l.socketPath, "--state-dir", l.stateDir)
	command.Env = l.environment
	log := &syncBuffer{}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		cancel()
		l.t.Fatalf("start daemon: %v", err)
	}
	l.t.Cleanup(func() {
		cancel()
		_ = command.Wait()
		if l.t.Failed() {
			l.t.Logf("daemon log:\n%s", log.String())
		}
		_ = os.Remove(l.socketPath)
	})
	deadline := time.Now().Add(startDeadline)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(l.socketPath); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	l.t.Fatalf("the daemon did not listen on %s within %s:\n%s", l.socketPath, startDeadline, log.String())
}
