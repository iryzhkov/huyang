package service

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// huyang trust adds, lists and removes roots through the policy file, and
// refuses malformed invocations without touching it.
func TestTrustSubcommandRoundTrip(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := runHuyang([]string{"trust", "--config", config, root}, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "trusted ") || !strings.Contains(stdout.String(), config) {
		t.Fatalf("trust output = %q", stdout.String())
	}
	stdout.Reset()
	if err := runHuyang([]string{"trust", "--config", config, "--list"}, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	listed := strings.TrimSpace(stdout.String())
	if listed == "" {
		t.Fatalf("list output = %q", stdout.String())
	}
	stdout.Reset()
	if err := runHuyang([]string{"trust", "--config", config, "--remove", listed}, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runHuyang([]string{"trust", "--config", config, "--list"}, nil, &stdout, &stderr); err != nil || strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("list after remove = %q, %v", stdout.String(), err)
	}
	if err := runHuyang([]string{"trust", "--config", config}, nil, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing root error = %v", err)
	}
}
