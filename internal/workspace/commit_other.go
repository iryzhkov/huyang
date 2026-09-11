//go:build !linux

package workspace

// renameNoReplace renames oldPath to newPath without replacing an existing newPath. Without
// renameat2 the portable link-then-unlink sequence provides the same refusal.
func renameNoReplace(oldPath, newPath string) error {
	return linkNoReplace(oldPath, newPath)
}
