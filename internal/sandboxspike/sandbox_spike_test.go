package sandboxspike

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

type strategy struct {
	name string
	args []string
}

var copyStrategies = []strategy{
	{name: "reflink", args: []string{"-a", "--reflink=always", "--sparse=auto"}},
	{name: "safe_copy", args: []string{"-a", "--reflink=never", "--sparse=always"}},
}

type entry struct {
	mode   fs.FileMode
	size   int64
	target string
	hash   [sha256.Size]byte
}

func spikeTempDir(t *testing.T) string {
	t.Helper()
	base := os.Getenv("HUYANG_SANDBOX_SPIKE_DIR")
	if base == "" {
		return t.TempDir()
	}
	root, err := os.MkdirTemp(base, "huyang-s14-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove spike temp directory: %v", err)
		}
	})
	return root
}

func fixture(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "tracked.txt"), []byte("dirty working-tree bytes\n"), 0o644)
	mustWrite(t, filepath.Join(root, "untracked.txt"), []byte("untracked\n"), 0o600)
	mustWrite(t, filepath.Join(root, "ignored", "output.bin"), []byte{0, 1, 2, 3}, 0o640)
	mustWrite(t, filepath.Join(root, "executable.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o751)
	mustWrite(t, filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/fixture\n"), 0o644)
	if err := os.Symlink("../tracked.txt", filepath.Join(root, "ignored", "link")); err != nil {
		t.Fatal(err)
	}
	sparse := filepath.Join(root, "sparse.img")
	file, err := os.OpenFile(sparse, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0x7f}, 16<<20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, content []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func manifest(root string) (map[string]entry, error) {
	result := make(map[string]entry)
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		record := entry{mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			record.size = info.Size()
			record.target, err = os.Readlink(path)
		case info.Mode().IsRegular():
			record.size = info.Size()
			var content []byte
			content, err = os.ReadFile(path)
			record.hash = sha256.Sum256(content)
		case info.IsDir():
		default:
			return fmt.Errorf("unsupported fixture object %s: %s", relative, info.Mode())
		}
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = record
		return nil
	})
	return result, err
}

func copyTree(ctxRoot, destination string, selected strategy) (time.Duration, error) {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return 0, err
	}
	start := time.Now()
	args := append(append([]string{}, selected.args...), ctxRoot+string(filepath.Separator)+".", destination)
	output, err := exec.Command("cp", args...).CombinedOutput()
	if err != nil {
		return time.Since(start), fmt.Errorf("cp %s: %w: %s", selected.name, err, strings.TrimSpace(string(output)))
	}
	return time.Since(start), nil
}

func TestCopyStrategiesPreserveExactFixture(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("S14 host spike targets Linux")
	}
	source := spikeTempDir(t)
	fixture(t, source)
	want, err := manifest(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range copyStrategies {
		t.Run(selected.name, func(t *testing.T) {
			destination := filepath.Join(spikeTempDir(t), "snapshot")
			elapsed, err := copyTree(source, destination, selected)
			if err != nil {
				if selected.name == "reflink" {
					t.Skipf("reflink unavailable on this filesystem: %v", err)
				}
				t.Fatal(err)
			}
			got, err := manifest(destination)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("snapshot manifest differs\n got: %#v\nwant: %#v", got, want)
			}
			sourceInfo, err := os.Stat(filepath.Join(source, "tracked.txt"))
			if err != nil {
				t.Fatal(err)
			}
			copyInfo, err := os.Stat(filepath.Join(destination, "tracked.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(sourceInfo, copyInfo) {
				t.Fatal("snapshot regular file aliases canonical inode")
			}
			if err := os.WriteFile(filepath.Join(destination, "tracked.txt"), []byte("sandbox mutation\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			canonical, err := os.ReadFile(filepath.Join(source, "tracked.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != "dirty working-tree bytes\n" {
				t.Fatalf("sandbox mutation escaped: %q", canonical)
			}
			t.Logf("%s fixture snapshot: %s", selected.name, elapsed)
		})
	}
}

func TestSpecialFilesAreDetectableAndMustBeRejected(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("S14 host spike targets Linux")
	}
	root := spikeTempDir(t)
	fifo := filepath.Join(root, "build.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := manifest(root)
	if err == nil || !strings.Contains(err.Error(), "unsupported fixture object") {
		t.Fatalf("special file was not rejected: %v", err)
	}
}

func TestHostBackendCapabilities(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("S14 host spike targets Linux")
	}
	source := spikeTempDir(t)
	mustWrite(t, filepath.Join(source, "probe"), []byte("probe"), 0o600)
	for _, selected := range copyStrategies {
		destination := filepath.Join(spikeTempDir(t), selected.name)
		elapsed, err := copyTree(source, destination, selected)
		if err != nil {
			t.Logf("%s unavailable: %v", selected.name, err)
			continue
		}
		t.Logf("%s available: %s", selected.name, elapsed)
	}
	if path, err := exec.LookPath("fuse-overlayfs"); err == nil {
		t.Logf("fuse-overlay available at %s", path)
	} else {
		t.Log("fuse-overlay unavailable: executable not found")
	}
	if _, err := os.Stat("/sys/module/overlay"); err == nil {
		t.Log("kernel overlay module present; mount privilege is probed by the S14 host command")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Logf("kernel overlay module status unavailable: %v", err)
	} else {
		t.Log("kernel overlay module absent")
	}
}
