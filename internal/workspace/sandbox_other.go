//go:build !linux

package workspace

import (
	"io/fs"
)

func sandboxCloneFile(string, string, fs.FileMode) error { return ErrReflinkUnsupported }
func sandboxFreeBytes(string) (uint64, error)            { return ^uint64(0), nil }
func sandboxSameDevice(string, string) (bool, error)     { return false, nil }
