package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

func TestErrClassFoldsOccurrencesTogether(t *testing.T) {
	stale := "Error: lines 11-11 of /tmp/a99t/T.qml do not hold the expected text, so the " +
		"numbers are probably from before an earlier edit shifted them.\n" +
		"expected: \"function openWallpaperPicker() { }\""
	got := errClass(stale)
	want := "lines <n>-<n> of <path> do not hold the expected text, so the numbers are " +
		"probably from before an earlier edit shifted them."
	if got != want {
		t.Errorf("stale-offset class:\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(got, "openWallpaperPicker") {
		t.Error("the class carries source code out of the workspace")
	}

	// The same failure on another file and another line is the same class,
	// which is the whole point: classes are what get counted.
	other := "Error: lines 402-419 of /home/igor/src/other/main.go do not hold the expected " +
		"text, so the numbers are probably from before an earlier edit shifted them."
	if errClass(other) != got {
		t.Errorf("same failure on another file classed differently:\n %s\n %s", errClass(other), got)
	}

	// A relative path folds the same way an absolute one does, so a failure
	// does not split into one cluster per file.
	relative := errClass("Error: the match text is nowhere in agent99/bridge/mcp.go")
	if relative != "the match text is nowhere in <path>" {
		t.Errorf("relative path not folded: %q", relative)
	}
	bare := errClass("Error: the match text is nowhere in mcp.go")
	if bare != "the match text is nowhere in <path>" {
		t.Errorf("bare filename not folded: %q", bare)
	}

	// A message with nothing to redact survives intact.
	plain := "Error: no files to search: pass file, files, or glob"
	if errClass(plain) != "no files to search: pass file, files, or glob" {
		t.Errorf("plain message altered: %s", errClass(plain))
	}
}

func TestArgKeysAreSortedNamesOnly(t *testing.T) {
	keys := argKeys(map[string]any{
		"text":    "func main() { secret() }",
		"file":    "/home/igor/src/x.go",
		"name_p1": 3,
	})
	want := []string{"file", "name_p1", "text"}
	if len(keys) != len(want) {
		t.Fatalf("got %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("got %v, want %v", keys, want)
		}
	}
	if argKeys(nil) != nil {
		t.Error("no arguments should spool no keys")
	}
}

func TestFrictionOutcomeClassification(t *testing.T) {
	tests := []struct {
		name    string
		outcome string
		isError bool
		wantOK  bool
	}{
		{name: "modern ok", outcome: "ok", wantOK: true},
		{name: "modern provisional", outcome: "provisional", wantOK: true},
		{name: "modern partial", outcome: "partial", wantOK: false},
		{name: "modern unavailable", outcome: "unavailable", wantOK: false},
		{name: "modern failed", outcome: "failed", wantOK: false},
		{name: "modern conflict", outcome: "conflict", wantOK: false},
		{name: "no outcome success", wantOK: true},
		{name: "no outcome error", isError: true, wantOK: false},
		{name: "unknown success follows error bit", outcome: "future", wantOK: true},
		{name: "unknown error follows error bit", outcome: "future", isError: true, wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := frictionOutcomeOK(test.outcome, test.isError); got != test.wantOK {
				t.Fatalf("frictionOutcomeOK(%q, %t) = %t, want %t", test.outcome, test.isError, got, test.wantOK)
			}
		})
	}
}

func TestLogFrictionWritesOneEvent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", dir)
	t.Setenv("HUYANG_FRICTION", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-under-test")

	failed := textResult("Error: no files to search: pass file, files, or glob", true)
	logFriction("grep", "/home/igor/src/agent99", map[string]any{"pattern": "TODO"}, failed,
		time.Now().Add(-15*time.Millisecond))

	spool := filepath.Join(dir, frictionSource)
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one spool file, got %v (err %v)", entries, err)
	}
	f, err := os.Open(filepath.Join(spool, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("spool file is empty")
	}
	var ev frictionEvent
	if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
		t.Fatalf("spool line is not JSON: %v", err)
	}
	if ev.Tool != "grep" || ev.OK {
		t.Errorf("wrong tool or outcome: %+v", ev)
	}
	if ev.Root != "agent99" {
		t.Errorf("root should be the last element only, got %q", ev.Root)
	}
	if ev.Err != "no files to search: pass file, files, or glob" {
		t.Errorf("wrong error class: %q", ev.Err)
	}
	if len(ev.Args) != 1 || ev.Args[0] != "pattern" {
		t.Errorf("wrong argument keys: %v", ev.Args)
	}
	if ev.Ms < 10 {
		t.Errorf("duration not measured: %d", ev.Ms)
	}
	if scanner.Scan() {
		t.Error("one call must spool exactly one line")
	}
}

func TestLogFrictionRecordsModernOutcomeAndPerCallClient(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", dir)
	t.Setenv("HUYANG_FRICTION", "1")

	result := textResult("Semantic provider unavailable", false)
	result["outcome"] = "unavailable"
	result["client"] = "codex-mcp-client/1"
	logFriction("navigate", "", map[string]any{"relation": "definition"}, result, time.Now())

	entries, err := os.ReadDir(filepath.Join(dir, frictionSource))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one spool file, got %v (err %v)", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, frictionSource, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var event frictionEvent
	if err := json.Unmarshal(bytes.TrimSpace(data), &event); err != nil {
		t.Fatalf("spool line is not JSON: %v", err)
	}
	if event.Outcome != "unavailable" || event.OK {
		t.Fatalf("modern outcome was not classified as friction: %+v", event)
	}
	if event.Client != "codex-mcp-client/1" {
		t.Fatalf("per-call client = %q, want codex-mcp-client/1", event.Client)
	}
	if event.Err != "Semantic provider unavailable" {
		t.Fatalf("error class = %q, want modern result summary", event.Err)
	}
}

func TestFrictionCanBeTurnedOff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", dir)
	t.Setenv("HUYANG_FRICTION", "0")

	logFriction("grep", "", map[string]any{}, textResult("ok", false), time.Now())

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("HUYANG_FRICTION=0 still wrote %v", entries)
	}
}

func TestFrictionIsOffUntilAskedFor(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", dir)
	t.Setenv("HUYANG_FRICTION", "")
	t.Setenv("TOOLFEEDBACK", "")

	if frictionEnabled() {
		t.Fatal("spooling must be off for anyone who did not ask for it")
	}
	logFriction("grep", "", map[string]any{}, textResult("ok", false), time.Now())
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("wrote %v without being enabled", entries)
	}

	// The marker file is the deliberate act that turns it on.
	if err := os.WriteFile(filepath.Join(dir, "enabled"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !frictionEnabled() {
		t.Error("the marker file did not enable spooling")
	}
	logFriction("grep", "", map[string]any{}, textResult("ok", false), time.Now())
	if entries, _ := os.ReadDir(filepath.Join(dir, frictionSource)); len(entries) != 1 {
		t.Errorf("expected one spool file once enabled, got %d", len(entries))
	}
}

func TestModernToolCallsWriteFrictionEvents(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", dir)
	t.Setenv("HUYANG_FRICTION", "1")

	session, cleanup := connectOfficialClient(t, mcpapi.ProfileOrient, newDirectWorkspaces(t.TempDir()))
	defer cleanup()
	callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})

	entries, err := os.ReadDir(filepath.Join(dir, frictionSource))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one modern MCP spool file, got %v (err %v)", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, frictionSource, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var event frictionEvent
	if err := json.Unmarshal(bytes.TrimSpace(data), &event); err != nil {
		t.Fatalf("modern MCP spool line is not JSON: %v", err)
	}
	if event.Tool != "workspace_open" || !event.OK || event.Root != filepath.Base(root) {
		t.Fatalf("wrong modern MCP friction event: %+v", event)
	}
}

// textResult builds the content/isError shape that logFriction inspects.
func textResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}
