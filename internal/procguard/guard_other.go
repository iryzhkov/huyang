//go:build !linux

package procguard

import (
	"errors"
	"os/exec"
	"sync"
)

// Guard is the owner's handle on one command. Outside Linux there is no
// guard process: the command runs in its own process group where the
// platform has them, and Stop kills that group.
type Guard struct {
	command *exec.Cmd
	mu      sync.Mutex
	waited  bool
}

// Wrap prepares a command that has not been started.
func Wrap(command *exec.Cmd) (*Guard, error) {
	if command.Err != nil {
		return nil, command.Err
	}
	if command.Process != nil {
		return nil, errors.New("procguard: command already started")
	}
	if command.SysProcAttr != nil {
		return nil, errors.New("procguard: SysProcAttr is reserved for the guard")
	}
	setProcessGroup(command)
	return &Guard{command: command}, nil
}

// Start starts the command.
func (g *Guard) Start() error { return g.command.Start() }

// Stop kills the command and, where process groups exist, its group. It is a
// no-op once Wait has returned, so it never signals a reused pid.
func (g *Guard) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waited || g.command.Process == nil {
		return
	}
	killProcessGroup(g.command)
}

// Wait waits for the command.
func (g *Guard) Wait() error {
	err := g.command.Wait()
	g.mu.Lock()
	g.waited = true
	g.mu.Unlock()
	return err
}
