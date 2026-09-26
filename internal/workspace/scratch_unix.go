//go:build unix

package workspace

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processStartToken is the process start time from /proc/<pid>/stat, in
// clock ticks since boot, or an empty string where /proc does not exist.
// Together with the PID it names one process: a recycled PID has a later
// start time.
func processStartToken(pid int) string {
	content, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// The command name in field 2 is parenthesised and may itself contain
	// spaces and parentheses, so the fields are counted after its last ')'.
	// Field 3 is the first after it and the start time is field 22.
	name := strings.LastIndexByte(string(content), ')')
	if name < 0 {
		return ""
	}
	fields := strings.Fields(string(content[name+1:]))
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}

// processOwnerAlive reports whether the process with this PID and start token
// is running. Whenever the answer is uncertain it says alive, because the
// cost of a wrong "alive" is a directory kept until the next sweep and the
// cost of a wrong "dead" is a directory removed under a running command.
func processOwnerAlive(pid int, start string) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	if start == "" {
		return true
	}
	current := processStartToken(pid)
	return current == "" || current == start
}
