//go:build linux

package workspace

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames oldPath to newPath and fails with fs.ErrExist when newPath already
// exists. Linux offers this atomically through renameat2(RENAME_NOREPLACE); a filesystem
// that does not implement the flag falls back to the portable link-then-unlink sequence.
func renameNoReplace(oldPath, newPath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) {
		return linkNoReplace(oldPath, newPath)
	}
	return err
}
