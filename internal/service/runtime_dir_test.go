package service

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/runtimedir"
)

// useRunUserRoot points the runtimedir /run/user seam at a temporary directory
// and, when private is set, creates this process's private directory in it.
// The directory name is short because a socket path under it must stay below
// the 108-byte Unix socket limit.
func useRunUserRoot(t *testing.T, private bool) string {
	t.Helper()
	root, err := os.MkdirTemp("", "ru")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	previous := runtimedir.RunUserRoot
	runtimedir.RunUserRoot = root
	t.Cleanup(func() { runtimedir.RunUserRoot = previous })
	if private {
		if err := os.Mkdir(filepath.Join(root, strconv.Itoa(os.Getuid())), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func unsetEnvForTest(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

// TestServeAndMCPDefaultSocketsAgree is the property a harness that strips
// XDG_RUNTIME_DIR relies on: the adapter finds the socket the service listens
// on without a pinned path.
func TestServeAndMCPDefaultSocketsAgree(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())
	for _, private := range []bool{true, false} {
		t.Run("run user directory present="+strconv.FormatBool(private), func(t *testing.T) {
			unsetEnvForTest(t, "XDG_RUNTIME_DIR", "HUYANG_SOCKET")
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			root := useRunUserRoot(t, private)
			serve := newServeFlags(&serviceConfig{}, io.Discard).Lookup("socket").DefValue
			profile, socket := "", ""
			mcp := newMCPFlags(&profile, &socket, io.Discard).Lookup("socket").DefValue
			if serve != mcp {
				t.Fatalf("serve default %q differs from mcp default %q", serve, mcp)
			}
			want := filepath.Join(temp, "huyang-"+uid, "huyang", "control.sock")
			if private {
				want = filepath.Join(root, uid, "huyang", "control.sock")
			}
			if serve != want {
				t.Fatalf("default socket = %q, want %q", serve, want)
			}
		})
	}
}

func TestDefaultSocketPrecedence(t *testing.T) {
	useRunUserRoot(t, true)
	t.Setenv("XDG_RUNTIME_DIR", "/xdg/runtime")
	t.Setenv("HUYANG_SOCKET", "/explicit/control.sock")
	if got := defaultHuyangSocket(); got != "/explicit/control.sock" {
		t.Fatalf("HUYANG_SOCKET lost precedence: %q", got)
	}
	unsetEnvForTest(t, "HUYANG_SOCKET")
	if got := defaultHuyangSocket(); got != "/xdg/runtime/huyang/control.sock" {
		t.Fatalf("XDG_RUNTIME_DIR lost precedence: %q", got)
	}
}

func TestMCPStalePinNamesTheUIDMismatch(t *testing.T) {
	unsetEnvForTest(t, "XDG_RUNTIME_DIR", "HUYANG_SOCKET")
	root := useRunUserRoot(t, true)
	uid := os.Getuid()
	other := strconv.Itoa(uid + 1)
	pinned := filepath.Join(root, other, "huyang", "control.sock")
	derived := filepath.Join(root, strconv.Itoa(uid), "huyang", "control.sock")
	err := runHuyang([]string{"mcp", "-socket", pinned, "-profile", "full"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("mcp with a stale pinned socket succeeded")
	}
	for _, want := range []string{
		"socket " + pinned + " is in UID " + other + "'s runtime directory but this process runs as UID " + strconv.Itoa(uid),
		"remove -socket from the registration (the default is " + derived + ")",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}

	t.Setenv("HUYANG_SOCKET", pinned)
	err = runHuyang([]string{"mcp"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "UID "+other+"'s runtime directory") || !strings.Contains(err.Error(), "unset HUYANG_SOCKET") {
		t.Fatalf("stale HUYANG_SOCKET: err = %v, want the UID mismatch and an unset hint", err)
	}
}

func TestMCPMissingSocketOfThisUIDKeepsTheConnectError(t *testing.T) {
	unsetEnvForTest(t, "XDG_RUNTIME_DIR", "HUYANG_SOCKET")
	root := useRunUserRoot(t, true)
	pinned := filepath.Join(root, strconv.Itoa(os.Getuid()), "huyang", "control.sock")
	err := runHuyang([]string{"mcp", "-socket", pinned}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("mcp with a missing socket succeeded")
	}
	want := "connect service at " + pinned + ": dial unix " + pinned + ": connect: no such file or directory"
	if err.Error() != want {
		t.Fatalf("error = %q, want today's %q", err, want)
	}
}
