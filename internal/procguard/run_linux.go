//go:build linux

package procguard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// cleanupLimit bounds how long the guard keeps killing descendants that
// appear while it sweeps, such as children forked by a process the moment
// before it was killed.
const cleanupLimit = 3 * time.Second

// init turns this process into a guard when Wrap started it. It runs during
// package initialisation, before main or the test framework, so every binary
// that links this package (huyang and the test binaries of its users) can
// serve as its own guard through /proc/self/exe.
func init() {
	value, ok := os.LookupEnv(envKey)
	if !ok {
		return
	}
	_ = os.Unsetenv(envKey)
	if len(os.Args) == 0 || os.Args[0] != guardName {
		return
	}
	os.Exit(runGuard(value, os.Args))
}

// runGuard starts the command and waits for the command to exit or the owner
// to go away, whichever comes first, then kills what is left of the tree.
//
// The death signal on the command is tied to the thread that forks it, not
// to the process. The guard forks from the main goroutine during package
// initialisation, where the runtime keeps it locked to the main thread, and
// that thread lives until the process exits; the explicit LockOSThread only
// states the requirement. Nothing in the owner depends on thread lifetime,
// because the owner is watched through a pipe, which belongs to the process.
func runGuard(value string, args []string) int {
	runtime.LockOSThread()
	fd, err := strconv.Atoi(value)
	if err != nil || len(args) < 4 || args[1] != "--" {
		fmt.Fprintln(os.Stderr, guardName+": malformed invocation")
		return 125
	}
	syscall.CloseOnExec(fd)
	// Without the subreaper mark, a descendant whose parent dies (a
	// debuggee whose Delve was killed, a build step whose cargo was killed)
	// would be reparented to init and escape the sweep below.
	_ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	pid, err := syscall.ForkExec(args[2], args[3:], &syscall.ProcAttr{
		Env:   os.Environ(),
		Files: []uintptr{0, 1, 2},
		Sys:   &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: starting %s: %v\n", guardName, args[2], err)
		return 127
	}
	exited := make(chan syscall.WaitStatus, 1)
	go reapUntil(pid, exited)
	ownerGone := make(chan struct{})
	go watchOwner(fd, ownerGone)
	select {
	case status := <-exited:
		killDescendants()
		exitLike(status)
	case sig := <-signals:
		killDescendants()
		dieBy(sig.(syscall.Signal))
	case <-ownerGone:
		killDescendants()
	}
	dieBy(syscall.SIGKILL)
	return 137
}

// reapUntil reaps every child, the command and adopted orphans alike, and
// reports the command's status.
func reapUntil(pid int, exited chan<- syscall.WaitStatus) {
	for {
		var status syscall.WaitStatus
		got, err := syscall.Wait4(-1, &status, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return
		}
		if got == pid {
			exited <- status
			return
		}
	}
}

// watchOwner closes gone when the owner pipe reaches end of file, which
// happens when the owner closes its end or dies for any reason.
func watchOwner(fd int, gone chan<- struct{}) {
	pipe := os.NewFile(uintptr(fd), "owner")
	buffer := make([]byte, 64)
	for {
		if _, err := pipe.Read(buffer); err != nil {
			close(gone)
			return
		}
	}
}

// exitLike ends the guard with the command's exit status, so the owner sees
// the same exit code and, for the common termination signals, the same
// signal.
func exitLike(status syscall.WaitStatus) {
	if status.Exited() {
		os.Exit(status.ExitStatus())
	}
	if status.Signaled() {
		dieBy(status.Signal())
	}
	os.Exit(1)
}

// dieBy ends the guard by the given signal. Signals the Go runtime handles
// itself (SIGSEGV, SIGQUIT and the like) would print a runtime dump instead,
// so those end the guard with the shell convention 128+n.
func dieBy(sig syscall.Signal) {
	switch sig {
	case syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP:
		signal.Reset(sig)
		_ = syscall.Kill(os.Getpid(), sig)
		time.Sleep(time.Second)
	}
	os.Exit(128 + int(sig))
}

// killDescendants SIGKILLs every process in the guard's group and every
// adopted child in its session until none is left or cleanupLimit passes.
// Group members are the command and whatever it started without changing
// group; adopted children are descendants that changed group and then lost
// their parent. Both identities are safe against pid reuse: the group id is
// the guard's own live pid, and a child's pid cannot be reused before the
// guard reaps it.
func killDescendants() {
	self := os.Getpid()
	own, err := readStat(self)
	if err != nil {
		_ = syscall.Kill(-self, syscall.SIGKILL)
		return
	}
	deadline := time.Now().Add(cleanupLimit)
	for {
		reapAvailable()
		victims := descendants(self, own)
		if len(victims) == 0 || time.Now().After(deadline) {
			return
		}
		for _, pid := range victims {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func reapAvailable() {
	for {
		pid, err := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil || pid <= 0 {
			return
		}
	}
}

// descendants lists the live processes killDescendants must kill.
func descendants(self int, own procStat) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		stat, err := readStat(pid)
		if err != nil || stat.state == 'Z' || stat.state == 'X' {
			continue
		}
		if stat.pgrp == own.pgrp || (stat.ppid == self && stat.session == own.session) {
			out = append(out, pid)
		}
	}
	return out
}

type procStat struct {
	state               byte
	ppid, pgrp, session int
}

// readStat parses the fields of /proc/<pid>/stat that follow the command
// name, which is parenthesised and may itself contain spaces or parentheses.
func readStat(pid int) (procStat, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, err
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return procStat{}, errors.New("malformed stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 4 || len(fields[0]) != 1 {
		return procStat{}, errors.New("malformed stat")
	}
	stat := procStat{state: fields[0][0]}
	for i, target := range []*int{&stat.ppid, &stat.pgrp, &stat.session} {
		if *target, err = strconv.Atoi(fields[i+1]); err != nil {
			return procStat{}, err
		}
	}
	return stat, nil
}
