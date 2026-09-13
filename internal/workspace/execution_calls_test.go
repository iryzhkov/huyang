package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGoExecutionRetainsRecursionAndMethodCandidates(t *testing.T) {
	source := ExecutionSource{Path: "main.go", Content: `package example
type First struct{}
type Second struct{}
func(First)Work(){}
func(Second)Work(){}
func recursive(n int){if n>0{recursive(n-1)}}
func indirect(f func()){f()}
func caller(w interface{Work()}){w.Work(); recursive(2)}
`}
	facts := AcquireGoExecutionCalls(context.Background(), "content_a", []ExecutionSource{source}, DefaultGraphBudget())
	snapshot, err := BuildExecutionSnapshot(context.Background(), executionTestRequest(), facts)
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]ExecutionNode{}
	for _, node := range snapshot.Execution.Nodes {
		nodes[node.ID] = node
	}
	recursion, alternatives, unresolved := false, 0, false
	for _, edge := range snapshot.Execution.Edges {
		from, to := nodes[edge.From], nodes[edge.To]
		if from.Name == "recursive" && from.ID == to.ID {
			recursion = true
		}
		if from.Name == "caller" && to.Name == "Work" {
			alternatives++
		}
		if from.Name == "indirect" && to.Kind == "unresolved" {
			unresolved = true
		}
	}
	if !recursion || alternatives != 2 || !unresolved {
		t.Fatalf("recursion=%v alternatives=%d unresolved=%v", recursion, alternatives, unresolved)
	}
	if snapshot.Execution.Coverage.Complete {
		t.Fatal("parser claimed complete call resolution")
	}
	again := AcquireGoExecutionCalls(context.Background(), "content_a", []ExecutionSource{source}, DefaultGraphBudget())
	if executionJSON(facts) != executionJSON(again) {
		t.Fatal("repeated parser acquisition changed facts")
	}
}
func TestExecutionSourceHandlesAreStableAndRefuseChangedBytes(t *testing.T) {
	root := t.TempDir()
	content := []byte("package example\nfunc target() {}\n")
	if err := os.WriteFile(filepath.Join(root, "main.go"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := New(KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	source := ExecutionSource{Path: "main.go", Content: string(content), Hash: hashBytes(content)}
	first, err := w.ExecutionSourceHandle(source, 2, 6)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.ExecutionSourceHandle(source, 2, 6)
	if err != nil || first != second {
		t.Fatalf("first=%s second=%s error=%v", first, second, err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ExecutionSourceHandle(source, 2, 6); ErrorCode(err) != "graph_source_changed" {
		t.Fatal(err)
	}
}
func TestDependenciesDoNotBecomeExecutionPaths(t *testing.T) {
	b := newExecutionBuilder("content_a", DefaultGraphBudget())
	for _, id := range []string{"a", "b"} {
		if err := b.Node(ExecutionNode{ID: id, Kind: "symbol", Evidence: executionTestEvidence("one")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Edge(ExecutionEdge{From: "a", To: "b", Kind: "data_dependency", Evidence: executionTestEvidence("one")}); err != nil {
		t.Fatal(err)
	}
	graph := b.finish(executionTestRequest().Key).Execution
	result := ExecutionGraphPaths(context.Background(), *graph, "a", "b", 0, 0)
	if result.Status == ExecutionStaticPossible {
		t.Fatal("a dependency was presented as execution")
	}
}
