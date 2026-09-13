//go:build live

package livetest

// Huyang runs from wherever it was checked out, and unattended work is
// checked out deep: a steward worker's run directory is around two hundred
// characters before the repository's own paths are added. Neovim's
// byte-compile cache names each entry after the full path of the file it
// caches, so the kernel loaded from such a checkout asked for a file name
// past the filesystem's limit, every language-server test in that checkout
// failed at bootstrap, and the gate an unattended run is asked to pass could
// not pass there for reasons that had nothing to do with its change.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheKernelLoadsFromADeepCheckout(t *testing.T) {
	// A path long enough that the escaped cache name exceeds the 255-byte
	// file name limit, built the way a worker's directory is: a few long
	// components rather than one impossible one.
	deep := t.TempDir()
	for range 4 {
		deep = filepath.Join(deep, strings.Repeat("worker-run-attempt", 3))
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(deep, "lua"), os.DirFS(filepath.Join(repositoryRoot(t), "lua"))); err != nil {
		t.Fatalf("copy the kernel into a deep path: %v", err)
	}
	if len(filepath.Join(deep, "lua", "huyang", "rpc.lua")) < 255 {
		t.Fatalf("the probe path is only %d characters; it does not reach the limit it is about",
			len(filepath.Join(deep, "lua", "huyang", "rpc.lua")))
	}

	// The developer's home, so whatever their configuration enables - the
	// byte-compile cache included - is what the embedded instance inherits.
	instance := startWithEnvironment(t, "HUYANG_RUNTIME_PATH="+deep)
	session := instance.connect("experimental")
	root := fixture(t, "go")
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	if outcome(opened) != "ok" {
		t.Fatalf("open from a deep checkout = %#v", opened)
	}
	provider, _ := data(opened)["semantic_provider"].(map[string]any)
	if provider == nil || provider["state"] != "healthy" {
		t.Fatalf("the kernel did not load from a deep checkout: %#v", provider)
	}
}
