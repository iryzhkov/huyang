//go:build live

package providerpool

import (
	"context"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestReapRealNeovimAndReopen(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim is not installed")
	}
	runtimePath, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUYANG_RUNTIME_PATH", runtimePath)
	t.Setenv("HUYANG_HEADLESS_INIT", "NONE")
	w, err := workspacecore.New(workspacecore.KindProject, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	p := New(t.TempDir(), ConfiguredFactory{})
	defer p.Close()
	now := time.Now()
	p.now = func() time.Time { return now }
	backend, release, err := p.Canonical(context.Background(), w)
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	before := backend.Descriptor()
	if before.ProcessID <= 0 {
		t.Fatal("provider has no process")
	}
	release()
	now = now.Add(DefaultProviderIdleTimeout)
	if p.ReapIdle() != 1 {
		t.Fatal("idle Neovim was not reaped")
	}
	if err := syscall.Kill(before.ProcessID, 0); err != syscall.ESRCH {
		t.Fatalf("Neovim pid %d still exists: %v", before.ProcessID, err)
	}
	next, releaseNext, err := p.Canonical(context.Background(), w)
	defer releaseNext()
	if err != nil {
		t.Fatal(err)
	}
	after := next.Descriptor()
	if after.ProcessID == before.ProcessID || after.Epoch <= before.Epoch {
		t.Fatalf("replacement did not advance process and epoch: before=%+v after=%+v", before, after)
	}
	if _, err := CallCanonical(context.Background(), "reopened", w, next, "workspace_resync", map[string]any{"root": w.Identity().Root}); err != nil {
		t.Fatalf("replacement cannot serve calls: %v", err)
	}
}
