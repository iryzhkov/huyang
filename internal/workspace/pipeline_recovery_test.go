package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckWriteErrorNamesCommandAndRestoresEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	root := t.TempDir()
	policy := trustedPolicy(t, root)
	command := CommandPolicy{Command: []string{"sh", "-c", "mkdir .venv"}}
	_, _, err := commandStage(context.Background(), root, "rev", "check", "check", command, policy, false)
	if err == nil {
		t.Fatal("accepted environment write")
	}
	for _, want := range []string{"undeclared_tool_write", ".venv", "verification stage check", "mkdir"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".venv")); !os.IsNotExist(err) {
		t.Fatalf("environment write survived: %v", err)
	}
}
