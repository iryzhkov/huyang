//go:build linux

package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/iryzhkov/huyang/internal/runtimedir"
)

type rootLock struct {
	file *os.File
	path string
}

// lockUID is the UID whose lock directory this process uses. It is a variable
// so tests can simulate another user.
var lockUID = os.Getuid

// rootLockDir is agent99-huyang in the user's runtime directory (see
// runtimedir.Base), else agent99-huyang-<uid> under the temporary directory:
// a shared name there would let the first user on a host own every other
// user's locks.
func rootLockDir(uid int) string {
	if base := runtimedir.Base(uid); base != "" {
		return filepath.Join(base, "agent99-huyang")
	}
	return filepath.Join(os.TempDir(), "agent99-huyang-"+strconv.Itoa(uid))
}

func rootLockPath(root string) (string, error) {
	uid := lockUID()
	dir := rootLockDir(uid)
	if err := runtimedir.EnsurePrivate(dir, uid); err != nil {
		return "", fmt.Errorf("embedded provider lock directory: %w", err)
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, "embed-"+hex.EncodeToString(sum[:10])+".lock"), nil
}

func acquireRootLock(root string) (*rootLock, error) {
	path, err := rootLockPath(root)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("workspace is already locked by another embedded provider")
		}
		return nil, err
	}
	if err := file.Truncate(0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	if _, err := file.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	_ = file.Sync()
	return &rootLock{file: file, path: path}, nil
}

func (l *rootLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
}
