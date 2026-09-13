package workspace

import (
	"os"
	"testing"
)

func TestExecutionTracePersistenceFailureDoesNotPublishMemory(t *testing.T) {
	w := traceTestWorkspace(t, t.TempDir(), t.TempDir())
	id, err := w.BeginExecutionTrace(ExecutionTrace{})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := w.ExecutionTrace(id)
	directory := w.traceStore().directory
	if err = os.Rename(directory, directory+".saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(directory, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.AppendExecutionTrace(ExecutionTraceEvent{Kind: "stopped", Sequence: 1}); ErrorCode(err) != "trace_storage_failed" {
		t.Fatal(err)
	}
	if err = w.SetExecutionTraceTargets([]ExecutionTraceTarget{{Path: "main.go", Line: 1}}); ErrorCode(err) != "trace_storage_failed" {
		t.Fatal(err)
	}
	if _, err = w.FinishExecutionTrace("exited"); ErrorCode(err) != "trace_storage_failed" {
		t.Fatal(err)
	}
	after, _ := w.ExecutionTrace(id)
	if executionJSON(before) != executionJSON(after) || w.ActiveExecutionTrace() != id {
		t.Fatal("failed persistence changed published trace")
	}
	if err = os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(directory+".saved", directory); err != nil {
		t.Fatal(err)
	}
	if _, err = w.FinishExecutionTrace("exited"); err != nil {
		t.Fatal(err)
	}
}
func TestExecutionTraceRecoveryRejectsNonDirectory(t *testing.T) {
	path := t.TempDir() + "/file"
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	store := executionTraceStore{directory: path, entries: map[string]*ExecutionTrace{}}
	if err := store.recover(); ErrorCode(err) != "trace_storage_failed" {
		t.Fatalf("invalid trace directory silently accepted: %v", err)
	}
}
