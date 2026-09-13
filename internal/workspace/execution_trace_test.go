package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func traceTestWorkspace(t *testing.T, root, state string) *Workspace {
	t.Helper()
	w, err := Open(OpenOptions{Kind: KindProject, Root: root, StateDir: state, ProviderEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func TestExecutionTracePrivacyCapsAndRetention(t *testing.T) {
	w := traceTestWorkspace(t, t.TempDir(), t.TempDir())
	trace := ExecutionTrace{Revision: "content_a", Policy: ExecutionTracePolicy{Mode: "stops", CaptureValues: true, MaxEvents: 1}}
	id, err := w.BeginExecutionTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	event := ExecutionTraceEvent{Kind: "stopped", Sequence: 1, Frames: []ExecutionTraceFrame{{Path: "main.go", Line: 1}},
		Values: []ExecutionTraceValue{{Name: "password", Value: "must-not-persist"}, {Name: "value", Value: strings.Repeat("v", 2000)}}}
	if err = w.AppendExecutionTrace(event); err != nil {
		t.Fatal(err)
	}
	if err = w.AppendExecutionTrace(event); err != nil {
		t.Fatal(err)
	}
	event.Sequence = 2
	if err = w.AppendExecutionTrace(event); err != nil {
		t.Fatal(err)
	}
	if _, err = w.FinishExecutionTrace("exited"); err != nil {
		t.Fatal(err)
	}
	finished, err := w.ExecutionTrace(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(finished.Events) != 1 || finished.Dropped != 1 || !finished.Coverage.Capped {
		t.Fatalf("%+v", finished)
	}
	if !finished.Events[0].Values[0].Redacted || !finished.Events[0].Values[1].Truncated {
		t.Fatal("value policy was not enforced")
	}
	body, err := os.ReadFile(filepath.Join(w.traceStore().directory, id+".json"))
	if err != nil || strings.Contains(string(body), "must-not-persist") {
		t.Fatal("secret persisted", err)
	}
	copyDigest := finished.Digest
	_ = w.AppendExecutionTrace(ExecutionTraceEvent{Kind: "stopped", Sequence: 3})
	unchanged, _ := w.ExecutionTrace(id)
	if unchanged.Digest != copyDigest {
		t.Fatal("completed trace changed")
	}
	for i := 0; i < MaxExecutionTraces; i++ {
		trace.Policy.CaptureValues = false
		if _, err = w.BeginExecutionTrace(trace); err != nil {
			t.Fatal(err)
		}
		if err = w.AppendExecutionTrace(event); err != nil {
			t.Fatal(err)
		}
		if _, err = w.FinishExecutionTrace("exited"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = w.ExecutionTrace(id); ErrorCode(err) != "trace_unknown" {
		t.Fatal("old trace survived retention", err)
	}
	latest, _ := w.ExecutionTrace("")
	if len(latest.Events[0].Values) > 0 {
		t.Fatal("disabled trace retained values")
	}
	entries, err := os.ReadDir(w.traceStore().directory)
	if err != nil || len(entries) != MaxExecutionTraces {
		t.Fatal("unbounded retention", err)
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Mode().Perm() != 0600 {
			t.Fatal("trace permissions", info.Mode())
		}
	}
}
func TestExecutionTraceRecoveryMarksInterruptedRecording(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	w := traceTestWorkspace(t, root, state)
	id, err := w.BeginExecutionTrace(ExecutionTrace{Revision: "content_a", Policy: ExecutionTracePolicy{Mode: "stops"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.AppendExecutionTrace(ExecutionTraceEvent{Kind: "stopped", Sequence: 1})
	// Reopen with the original durable identity, as the workspace registry does.
	options := OpenOptions{Kind: KindProject, Root: root, StateDir: state, ProviderEpoch: 1}
	recovered, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	recovered.identity.ID = w.Identity().ID
	trace, err := recovered.ExecutionTrace(id)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Finished == nil || trace.Completion != "daemon_restart" || recovered.ActiveExecutionTrace() != "" {
		t.Fatalf("%+v", trace)
	}
}
