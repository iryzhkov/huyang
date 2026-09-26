package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scratch directories for verification commands.
//
// Every command runs with a throwaway HOME (and, without the shared cache, a
// throwaway Go build cache), and every parallel command runs in a full
// restored copy of the tree. These used to be created in the system temp
// directory and removed only by a deferred RemoveAll. The service is
// regularly killed whole by systemd-oomd, and a SIGKILL runs no defer, so the
// directories accumulated in /tmp, which on this kind of host is a RAM-backed
// tmpfs: the leak cost memory, not only disk.
//
// They now live under a scratch root beside the service state, one
// subdirectory per process named owner-<pid>-<start>, where start is the
// process start time the kernel reports. The name is the ownership marker,
// so there is no window in which a directory exists without one. At startup
// the service removes every owner directory whose process is gone; one whose
// process is still running is never touched, which keeps a second service on
// the same state directory, a test process and the in-process direct mode
// safe from each other. The start time is what tells a live owner from a
// recycled PID.
const (
	scratchOwnerPrefix = "owner-"
	// legacyScratchMaxAge is how old a scratch directory left in the system
	// temp directory by an earlier version must be before the sweep removes
	// it. Those directories carry no owner, so age is the only evidence that
	// nothing uses them. Both kinds are created when a command starts and are
	// used only while it runs, and a command cannot outlive the tool call
	// that started it: the call timeout is minutes and a verification
	// command's own timeout is smaller still. A directory created more than a
	// day ago therefore belongs to a command that has finished or was killed.
	// The top directory's modification time is its creation time for this
	// purpose, because its only children are created right after it.
	legacyScratchMaxAge = 24 * time.Hour
)

// legacyScratchPrefixes are the names earlier versions gave their scratch
// directories in the system temp directory.
var legacyScratchPrefixes = []string{"huyang-command-env-", "huyang-verification-command-"}

var commandScratch struct {
	mu    sync.Mutex
	root  string
	owner string
}

// scratchOwnerAlive reports whether the process that owns an owner directory
// is still running. It is a variable so tests can control liveness.
var scratchOwnerAlive = processOwnerAlive

// SetCommandScratchRoot names the directory that holds verification scratch
// directories. The service calls it once at startup with a directory beside
// its state. Without it, scratch directories are created in the system temp
// directory as before, which only library callers and tests rely on.
func SetCommandScratchRoot(path string) {
	commandScratch.mu.Lock()
	defer commandScratch.mu.Unlock()
	commandScratch.root, commandScratch.owner = path, ""
}

// commandScratchDir creates a fresh scratch directory whose name starts with
// pattern (as os.MkdirTemp takes it) inside this process's owner directory.
func commandScratchDir(pattern string) (string, error) {
	owner, err := commandScratchOwner()
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(owner, pattern)
}

// commandScratchOwner returns this process's owner directory under the
// configured root, creating it on first use, or an empty string, meaning the
// system temp directory, when no root is configured.
func commandScratchOwner() (string, error) {
	commandScratch.mu.Lock()
	defer commandScratch.mu.Unlock()
	if commandScratch.root == "" {
		return "", nil
	}
	owner := filepath.Join(commandScratch.root, scratchOwnerName(os.Getpid(), processStartToken(os.Getpid())))
	if commandScratch.owner == owner {
		// Recreated in case something removed it; MkdirAll is cheap when it exists.
		return owner, os.MkdirAll(owner, 0o700)
	}
	if err := os.MkdirAll(owner, 0o700); err != nil {
		return "", fmt.Errorf("create command scratch directory: %w", err)
	}
	commandScratch.owner = owner
	return owner, nil
}

func scratchOwnerName(pid int, start string) string {
	return scratchOwnerPrefix + strconv.Itoa(pid) + "-" + start
}

// parseScratchOwner reads the PID and start token back out of an owner
// directory name; ok is false for any other name.
func parseScratchOwner(name string) (pid int, start string, ok bool) {
	rest, found := strings.CutPrefix(name, scratchOwnerPrefix)
	if !found {
		return 0, "", false
	}
	pidText, start, found := strings.Cut(rest, "-")
	if !found {
		return 0, "", false
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	return pid, start, true
}

// SweepCommandScratch removes the scratch directories no live process owns:
// owner directories under the configured root whose process has exited, and
// directories earlier versions left in the system temp directory once they
// are older than legacyScratchMaxAge. The service calls it at startup. It
// reports how many directories it removed; a directory that cannot be
// removed is reported in the error and does not stop the sweep.
func SweepCommandScratch() (int, error) {
	commandScratch.mu.Lock()
	root := commandScratch.root
	commandScratch.mu.Unlock()
	removed, err := sweepScratchOwners(root)
	legacy, legacyErr := sweepLegacyScratch(os.TempDir(), time.Now())
	return removed + legacy, errors.Join(err, legacyErr)
}

func sweepScratchOwners(root string) (int, error) {
	if root == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var failures []error
	for _, entry := range entries {
		pid, start, ok := parseScratchOwner(entry.Name())
		if !ok || !entry.IsDir() || scratchOwnerAlive(pid, start) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			failures = append(failures, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(failures...)
}

func sweepLegacyScratch(tempDir string, now time.Time) (int, error) {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return 0, err
	}
	removed := 0
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() || !hasLegacyScratchPrefix(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < legacyScratchMaxAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(tempDir, entry.Name())); err != nil {
			failures = append(failures, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(failures...)
}

func hasLegacyScratchPrefix(name string) bool {
	for _, prefix := range legacyScratchPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
