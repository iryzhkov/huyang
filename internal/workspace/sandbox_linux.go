//go:build linux

package workspace

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func sandboxCloneFile(source, target string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer output.Close()
	if err := unix.IoctlFileClone(int(output.Fd()), int(input.Fd())); err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EINVAL) {
			return ErrReflinkUnsupported
		}
		return err
	}
	return nil
}

func sandboxFreeBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

func sandboxSameDevice(left, right string) (bool, error) {
	var a, b unix.Stat_t
	if err := unix.Stat(left, &a); err != nil {
		return false, err
	}
	if err := unix.Stat(right, &b); err != nil {
		return false, err
	}
	return a.Dev == b.Dev, nil
}
