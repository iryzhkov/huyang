//go:build live

package livetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect speaks to the daemon the way a client does: through the adapter
// subprocess, over its stdio, with the official SDK. Nothing in the test
// reaches into the service's memory.
func (l *live) connect(profile string) *mcp.ClientSession {
	l.t.Helper()
	command := exec.Command(l.binary, "mcp", "--socket", l.socketPath, "--profile", profile)
	command.Env = l.environment
	command.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-livetest", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		l.t.Fatalf("connect through the adapter: %v", err)
	}
	l.t.Cleanup(func() { _ = session.Close() })
	return session
}

// call runs one tool and returns its envelope. A transport error fails the
// test; an application refusal is a result like any other, because refusing
// well is behaviour these tests are here to check.
func call(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("%s returned no content", name)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s returned %T, want text", name, result.Content[0])
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text.Text), &envelope); err != nil {
		t.Fatalf("%s returned content that is not an envelope: %v\n%s", name, err, text.Text)
	}
	return envelope
}

// callRefused runs a tool that the server is expected to reject outright, and
// returns the refusal. A schema violation is refused by the transport rather
// than answered with an envelope, which is the point when the question is
// whether an argument is advertised at all.
func callRefused(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) error {
	t.Helper()
	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err == nil {
		t.Fatalf("%s accepted arguments it does not advertise", name)
	}
	return err
}

// sessionHandle is the client session type, named once so helpers can take
// it without every test file importing the SDK.
type sessionHandle = *mcp.ClientSession

// renderJSON is a reply as the client received it, for assertions about what
// a reply must never contain.
func renderJSON(t *testing.T, envelope map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// outcome is the envelope's outcome, or the empty string when there is none.
func outcome(envelope map[string]any) string {
	text, _ := envelope["outcome"].(string)
	return text
}

func summary(envelope map[string]any) string {
	text, _ := envelope["summary"].(string)
	return text
}

func data(envelope map[string]any) map[string]any {
	value, _ := envelope["data"].(map[string]any)
	return value
}

// workspaceIdentity is the envelope's workspace block, which every reply
// carries beside its data rather than inside it.
func workspaceIdentity(t *testing.T, envelope map[string]any) string {
	t.Helper()
	identity, _ := envelope["workspace"].(map[string]any)
	id, _ := identity["id"].(string)
	if id == "" {
		t.Fatalf("reply carries no workspace id: %#v", envelope)
	}
	return id
}

// workspaceRevision is the canonical revision the envelope's workspace block
// reports, which is how a test sees whether a refused call moved anything.
func workspaceRevision(envelope map[string]any) string {
	identity, _ := envelope["workspace"].(map[string]any)
	revision, _ := identity["revision"].(string)
	return revision
}

// fixture copies one of tests/fixtures into a disposable directory, so a live
// run never edits the repository it is run from.
func fixture(t *testing.T, language string) string {
	t.Helper()
	source := filepath.Join(repositoryRoot(t), "tests", "fixtures", language)
	destination := filepath.Join(t.TempDir(), language)
	if err := os.CopyFS(destination, os.DirFS(source)); err != nil {
		t.Fatalf("copy the %s fixture: %v", language, err)
	}
	return destination
}

// repositoryRoot is the checkout this test was built from.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("no go.mod above the working directory")
		}
		directory = parent
	}
}

// shortSocketPath keeps the socket inside the runtime directory, because a
// Unix socket path is bounded at about 100 bytes and a test temporary
// directory name is most of that on its own.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	directory, err := os.MkdirTemp(base, "huyang-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, "control.sock")
}

// syncBuffer collects a subprocess's output from whichever goroutine writes it.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// hashTree records the sha256 of every file under root, so a test can state
// that canonical bytes did not move.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hashes[relative] = fmt.Sprintf("%x", sha256.Sum256(content))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hashes
}

// spoolLines is every friction event this installation recorded.
func (l *live) spoolLines(t *testing.T) []string {
	t.Helper()
	directory := filepath.Join(l.spoolDir, "huyang")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var lines []string
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
	}
	return lines
}
