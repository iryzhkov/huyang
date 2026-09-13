package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func closedGoFixture(t *testing.T, content string) (ExecutionRequest, []ExecutionSource) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := New(KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	sources, revision, _, err := w.ExecutionSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := executionTestRequest()
	request.Root = root
	request.Key.Revision = revision
	return request, sources
}
func TestClosedGoProofRequiresCompleteInventoryAndGrammar(t *testing.T) {
	request, sources := closedGoFixture(t, "package main\nfunc main(){source()}\nfunc source(){}\nfunc target(){}\n")
	graph, err := ClosedGoExecution(context.Background(), request, sources, ".")
	if err != nil {
		t.Fatal(err)
	}
	from, to := "", ""
	for _, node := range graph.Execution.Nodes {
		if node.Name == "source" {
			from = node.ID
		}
		if node.Name == "target" {
			to = node.ID
		}
	}
	paths := ExecutionGraphPaths(context.Background(), *graph.Execution, from, to, 0, 0)
	if paths.Status != ExecutionStaticallyUnreachable {
		t.Fatalf("%+v", paths)
	}
	if err := os.WriteFile(filepath.Join(request.Root, "ignored.go"), []byte("package main\nfunc hidden(){}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ClosedGoExecution(context.Background(), request, sources, "."); ErrorCode(err) != "execution_proof_unavailable" {
		t.Fatal("omitted Go source did not refuse proof", err)
	}
	for _, content := range []string{
		"package main\nimport \"fmt\"\nfunc main(){fmt.Println(1)}",
		"package main\nfunc main(){f:=target;f()}\nfunc target(){}",
		"package main\nfunc main(){go target()}\nfunc target(){}",
		"package main\nfunc target()\nfunc main(){}",
	} {
		request, sources := closedGoFixture(t, content)
		if _, err := ClosedGoExecution(context.Background(), request, sources, "."); ErrorCode(err) != "execution_proof_unavailable" {
			t.Fatalf("unsafe grammar accepted: %s error=%v", content, err)
		}
	}
}
func TestClosedGoDepthCapCannotProveAbsence(t *testing.T) {
	request, sources := closedGoFixture(t, "package main\nfunc main(){a()}\nfunc a(){b()}\nfunc b(){}\nfunc target(){}")
	snapshot, err := ClosedGoExecution(context.Background(), request, sources, ".")
	if err != nil {
		t.Fatal(err)
	}
	from, to := "", ""
	for _, node := range snapshot.Execution.Nodes {
		if node.Name == "main" {
			from = node.ID
		}
		if node.Name == "target" {
			to = node.ID
		}
	}
	paths := ExecutionGraphPaths(context.Background(), *snapshot.Execution, from, to, 16, 1)
	if paths.Status != ExecutionUnknown || !paths.Coverage.Capped {
		t.Fatalf("%+v", paths)
	}
}
func TestPreparedExecutionHandlesNeverRelocateIntoCanonicalSource(t *testing.T) {
	request, sources := closedGoFixture(t, "package main\nfunc main(){}\n")
	w, err := New(KindProject, request.Root, 1)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := w.PreparedExecutionSourceHandle(sources[0], "prep_1234567890123456", 2, 6)
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.PreparedExecutionSourceHandle(sources[0], "prep_1234567890123456", 2, 6)
	if err != nil || handle != again {
		t.Fatal("prepared handles unstable")
	}
	if _, err := w.ResolveHandle(HandleID(handle)); ErrorCode(err) != "handle_revision_mismatch" {
		t.Fatal("prepared handle resolved canonically", err)
	}
}
