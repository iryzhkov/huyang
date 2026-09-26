//go:build linux

package procguard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// envKey carries the descriptor number of the owner pipe into the guard.
	// Its presence, together with guardName as argv[0], is what turns a
	// re-executed Huyang binary into a guard.
	envKey    = "HUYANG_PROCGUARD"
	guardName = "huyang-procguard"
	// ownerFD is where exec.Cmd places the first ExtraFiles entry.
	ownerFD = 3
	// stopGrace bounds how long Stop trusts the guard to clean up after the
	// owner pipe closes before the guard itself is killed. Killing the guard
	// still kills the command through its death signal, but descendants then
	// escape, so this is only the fallback for a guard that has hung.
	stopGrace = 5 * time.Second
)

// Guard is the owner's handle on one guarded command.
type Guard struct {
	command *exec.Cmd
	// owner is the write end of the pipe the guard watches. No other process
	// holds it: os.Pipe opens it close-on-exec.
	owner *os.File
	// peer is the read end, passed to the guard and closed here once the
	// guard has started.
	peer *os.File

	mu       sync.Mutex
	stopped  bool
	waited   bool
	fallback *time.Timer
}

// Wrap rewrites a command that has not been started so that it runs under a
// guard. The command keeps its directory, environment and standard streams.
// ExtraFiles and SysProcAttr must be unset, because the guard owns both.
func Wrap(command *exec.Cmd) (*Guard, error) {
	if command.Err != nil {
		return nil, command.Err
	}
	if command.Process != nil {
		return nil, errors.New("procguard: command already started")
	}
	if len(command.ExtraFiles) > 0 || command.SysProcAttr != nil {
		return nil, errors.New("procguard: ExtraFiles and SysProcAttr are reserved for the guard")
	}
	if err := checkExecutable(command.Path, command.Dir); err != nil {
		return nil, err
	}
	peer, owner, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	environment := command.Env
	if environment == nil {
		environment = os.Environ()
	}
	guarded := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, envKey+"=") {
			guarded = append(guarded, entry)
		}
	}
	command.Env = append(guarded, envKey+"="+strconv.Itoa(ownerFD))
	command.Args = append([]string{guardName, "--", command.Path}, command.Args...)
	command.Path = "/proc/self/exe"
	command.ExtraFiles = []*os.File{peer}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &Guard{command: command, owner: owner, peer: peer}, nil
}

// checkExecutable reports a missing or non-executable command before the
// guard starts, so a launch failure stays a Start error rather than an early
// exit of the guard. A relative path is resolved against the command's
// directory, as the guard will resolve it.
func checkExecutable(path, dir string) error {
	if !filepath.IsAbs(path) && dir != "" {
		path = filepath.Join(dir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return &exec.Error{Name: path, Err: err}
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return &exec.Error{Name: path, Err: syscall.EACCES}
	}
	return nil
}

// Start starts the guard, which starts the command.
func (g *Guard) Start() error {
	err := g.command.Start()
	_ = g.peer.Close()
	if err != nil {
		g.mu.Lock()
		g.stopped, g.waited = true, true
		_ = g.owner.Close()
		g.mu.Unlock()
	}
	return err
}

// Stop asks the guard to kill the command and all of its descendants by
// closing the owner pipe. It does not wait; Wait reports the outcome. It is
// safe to call more than once and after Wait.
func (g *Guard) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.stopped = true
	_ = g.owner.Close()
	if process := g.command.Process; process != nil && !g.waited {
		// Process.Kill is safe after Wait has reaped the guard: it then
		// reports os.ErrProcessDone instead of signalling a reused pid.
		g.fallback = time.AfterFunc(stopGrace, func() { _ = process.Kill() })
	}
}

// Wait waits for the guard, which exits after the command and its
// descendants are gone, and returns the command's exit status as exec.Cmd.Wait
// would. It releases the owner pipe.
func (g *Guard) Wait() error {
	err := g.command.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.waited = true
	if g.fallback != nil {
		g.fallback.Stop()
	}
	if !g.stopped {
		g.stopped = true
		_ = g.owner.Close()
	}
	return err
}
