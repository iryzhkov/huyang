package runtimedir

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// useRunUserRoot points the /run/user seam at a fresh temporary directory for
// the duration of one test, so no test depends on the worker's real /run/user.
func useRunUserRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	previous := RunUserRoot
	RunUserRoot = root
	t.Cleanup(func() { RunUserRoot = previous })
	return root
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

func mkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestBase(t *testing.T) {
	uid := os.Getuid()
	other := uid + 1
	cases := []struct {
		name  string
		xdg   *string
		uid   int
		setup func(t *testing.T, root string)
		want  func(root string) string
	}{
		{
			name: "absolute XDG_RUNTIME_DIR wins over a valid /run/user directory",
			xdg:  stringPointer("/custom/runtime"), uid: uid,
			setup: func(t *testing.T, root string) { mkdirMode(t, filepath.Join(root, strconv.Itoa(uid)), 0o700) },
			want:  func(string) string { return "/custom/runtime" },
		},
		{
			name: "XDG_RUNTIME_DIR unset uses a valid /run/user/<uid>", uid: uid,
			setup: func(t *testing.T, root string) { mkdirMode(t, filepath.Join(root, strconv.Itoa(uid)), 0o700) },
			want:  func(root string) string { return filepath.Join(root, strconv.Itoa(uid)) },
		},
		{
			name: "empty XDG_RUNTIME_DIR uses a valid /run/user/<uid>", xdg: stringPointer(""), uid: uid,
			setup: func(t *testing.T, root string) { mkdirMode(t, filepath.Join(root, strconv.Itoa(uid)), 0o700) },
			want:  func(root string) string { return filepath.Join(root, strconv.Itoa(uid)) },
		},
		{
			name: "relative XDG_RUNTIME_DIR is ignored", xdg: stringPointer("relative/runtime"), uid: uid,
			setup: func(t *testing.T, root string) { mkdirMode(t, filepath.Join(root, strconv.Itoa(uid)), 0o700) },
			want:  func(root string) string { return filepath.Join(root, strconv.Itoa(uid)) },
		},
		{
			name: "relative XDG_RUNTIME_DIR and no /run/user directory", xdg: stringPointer("relative/runtime"), uid: uid,
			setup: func(*testing.T, string) {},
			want:  func(string) string { return "" },
		},
		{
			name: "missing /run/user/<uid>", uid: uid,
			setup: func(*testing.T, string) {},
			want:  func(string) string { return "" },
		},
		{
			name: "/run/user/<uid> is a symbolic link to a private directory", uid: uid,
			setup: func(t *testing.T, root string) {
				target := filepath.Join(t.TempDir(), "real")
				mkdirMode(t, target, 0o700)
				if err := os.Symlink(target, filepath.Join(root, strconv.Itoa(uid))); err != nil {
					t.Fatal(err)
				}
			},
			want: func(string) string { return "" },
		},
		{
			name: "/run/user/<uid> is a regular file", uid: uid,
			setup: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, strconv.Itoa(uid)), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: func(string) string { return "" },
		},
		{
			// The directory belongs to this process's user, so asking for
			// another UID simulates a directory that UID does not own.
			name: "/run/user/<uid> owned by another UID", uid: other,
			setup: func(t *testing.T, root string) { mkdirMode(t, filepath.Join(root, strconv.Itoa(other)), 0o700) },
			want:  func(string) string { return "" },
		},
		{
			name: "group-writable /run/user/<uid>", uid: uid,
			setup: func(t *testing.T, root string) { mkdirMode(t, filepath.Join(root, strconv.Itoa(uid)), 0o770) },
			want:  func(string) string { return "" },
		},
		{
			name: "world-writable /run/user/<uid>", uid: uid,
			setup: func(t *testing.T, root string) { mkdirMode(t, filepath.Join(root, strconv.Itoa(uid)), 0o702) },
			want:  func(string) string { return "" },
		},
		{
			name: "group-readable but not writable /run/user/<uid>", uid: uid,
			setup: func(t *testing.T, root string) { mkdirMode(t, filepath.Join(root, strconv.Itoa(uid)), 0o750) },
			want:  func(root string) string { return filepath.Join(root, strconv.Itoa(uid)) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := useRunUserRoot(t)
			if tc.xdg == nil {
				unsetEnv(t, "XDG_RUNTIME_DIR")
			} else {
				t.Setenv("XDG_RUNTIME_DIR", *tc.xdg)
			}
			tc.setup(t, root)
			if got, want := Base(tc.uid), tc.want(root); got != want {
				t.Fatalf("Base(%d) = %q, want %q", tc.uid, got, want)
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

func TestCheckPrivateExplainsEveryRefusal(t *testing.T) {
	uid := os.Getuid()
	base := t.TempDir()
	private := filepath.Join(base, "private")
	mkdirMode(t, private, 0o700)
	groupWritable := filepath.Join(base, "group")
	mkdirMode(t, groupWritable, 0o770)
	link := filepath.Join(base, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivate(private, uid); err != nil {
		t.Fatalf("private directory refused: %v", err)
	}
	cases := []struct {
		path string
		uid  int
		want string
	}{
		{private, uid + 1, "is owned by UID " + strconv.Itoa(uid) + ", not " + strconv.Itoa(uid+1)},
		{groupWritable, uid, "is group- or world-writable"},
		{link, uid, "is a symbolic link"},
		{file, uid, "is not a directory"},
		{filepath.Join(base, "missing"), uid, "no such file or directory"},
	}
	for _, tc := range cases {
		err := CheckPrivate(tc.path, tc.uid)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.path) {
			t.Errorf("CheckPrivate(%s, %d) = %v, want an error naming the path and %q", tc.path, tc.uid, err, tc.want)
		}
	}
}

func TestEnsurePrivate(t *testing.T) {
	uid := os.Getuid()
	base := t.TempDir()
	created := filepath.Join(base, "created")
	if err := EnsurePrivate(created, uid); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(created)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("EnsurePrivate created %v (err %v), want a 0700 directory", info, err)
	}
	if err := EnsurePrivate(created, uid); err != nil {
		t.Fatalf("existing private directory refused: %v", err)
	}
	if err := EnsurePrivate(created, uid+1); err == nil || !strings.Contains(err.Error(), "owned by UID") {
		t.Fatalf("directory owned by another UID: err = %v, want an ownership refusal", err)
	}
	shared := filepath.Join(base, "shared")
	mkdirMode(t, shared, 0o777)
	if err := EnsurePrivate(shared, uid); err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("world-writable directory: err = %v, want a mode refusal", err)
	}
}

func TestRunUserOwner(t *testing.T) {
	previous := RunUserRoot
	RunUserRoot = "/run/user"
	t.Cleanup(func() { RunUserRoot = previous })
	cases := []struct {
		path string
		uid  int
		ok   bool
	}{
		{"/run/user/1000/huyang/control.sock", 1000, true},
		{"/run/user/1001", 1001, true},
		{"/run/user/1000/../1001/huyang/control.sock", 1001, true},
		{"/run/user", 0, false},
		{"/run/user/abc/huyang/control.sock", 0, false},
		{"/run/user/-1/huyang/control.sock", 0, false},
		{"/run/user/../etc/passwd", 0, false},
		{"/run/userx/1000/control.sock", 0, false},
		{"run/user/1000/control.sock", 0, false},
		{"/tmp/huyang-1000/huyang/control.sock", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		uid, ok := RunUserOwner(tc.path)
		if uid != tc.uid || ok != tc.ok {
			t.Errorf("RunUserOwner(%q) = %d, %v; want %d, %v", tc.path, uid, ok, tc.uid, tc.ok)
		}
	}
}
