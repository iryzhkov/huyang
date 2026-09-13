package handlers

import (
	"path/filepath"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestExecutionMappingRejectsDirtyProviderReferences(t *testing.T) {
	root := t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "main.go")
	sources := []workspacecore.ExecutionSource{{Path: "main.go", Hash: "disk"}}
	batch := executionProviderBatch{
		Sources:    map[string]string{path: "disk"},
		References: []executionProviderReference{{File: path, Target: executionProviderSymbol{File: path}}},
	}
	if !executionBatchMatchesSources(w, sources, batch) {
		t.Fatal("matching reference rejected")
	}
	batch.Sources[path] = "unsaved"
	if executionBatchMatchesSources(w, sources, batch) {
		t.Fatal("dirty reference accepted under disk revision")
	}
	delete(batch.Sources, path)
	if executionBatchMatchesSources(w, sources, batch) {
		t.Fatal("unmapped reference accepted")
	}
}
