// Package runtimedir finds the per-user runtime directory that Huyang's
// control socket and the embedded provider's root locks live in.
//
// A harness may start the MCP adapter without XDG_RUNTIME_DIR, while the
// systemd user service that listens on the socket has it. Both must still
// arrive at the same directory, so the lookup does not stop at the
// environment: when XDG_RUNTIME_DIR is missing it checks the directory
// systemd-logind creates for the user, /run/user/<uid>, and only uses it when
// it is plainly that user's private directory.
package runtimedir

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RunUserRoot is the parent of the per-user runtime directories
// systemd-logind creates. It is a variable so tests can point it at a
// temporary directory instead of the host's /run/user.
var RunUserRoot = "/run/user"

// Base returns the runtime directory for uid: $XDG_RUNTIME_DIR when it is set
// to an absolute path, else RunUserRoot/<uid> when CheckPrivate accepts it for
// uid. It returns "" when neither applies, and the caller falls back to a
// private directory of its own under the temporary directory.
func Base(uid int) string {
	if value := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	candidate := filepath.Join(RunUserRoot, strconv.Itoa(uid))
	if CheckPrivate(candidate, uid) != nil {
		return ""
	}
	return candidate
}

// CheckPrivate returns nil when path is a directory, not a symbolic link,
// owned by uid and writable by no one but its owner. Otherwise the error says
// which of those failed.
func CheckPrivate(path string, uid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link, not a private directory", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	owner, known := ownerUID(info)
	if !known {
		return fmt.Errorf("%s: the owner of a directory cannot be read on this platform", path)
	}
	if owner != uid {
		return fmt.Errorf("%s is owned by UID %d, not %d", path, owner, uid)
	}
	if mode := info.Mode().Perm(); mode&0o022 != 0 {
		return fmt.Errorf("%s is group- or world-writable (mode %04o)", path, mode)
	}
	return nil
}

// EnsurePrivate creates path with mode 0700 when it does not exist, then
// checks it with CheckPrivate, so a directory someone else created first is
// refused rather than used.
func EnsurePrivate(path string, uid int) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return CheckPrivate(path, uid)
}

// RunUserOwner reports the UID whose runtime directory under RunUserRoot
// holds path, for example 1000 for /run/user/1000/huyang/control.sock. It
// returns false for a relative path and for anything outside RunUserRoot.
func RunUserOwner(path string) (int, bool) {
	if !filepath.IsAbs(path) {
		return 0, false
	}
	relative, err := filepath.Rel(filepath.Clean(RunUserRoot), filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return 0, false
	}
	first, _, _ := strings.Cut(relative, string(filepath.Separator))
	uid, err := strconv.Atoi(first)
	if err != nil || uid < 0 || strconv.Itoa(uid) != first {
		return 0, false
	}
	return uid, true
}
