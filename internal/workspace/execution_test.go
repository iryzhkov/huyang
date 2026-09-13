package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type executionFixtureContributor struct {
	name       string
	contribute func(*ExecutionBuilder)
}

func (c executionFixtureContributor) Name() string    { return c.name }
func (c executionFixtureContributor) Version() string { return "1" }
func (c executionFixtureContributor) ContributeExecution(_ context.Context, _ ExecutionRequest, b *ExecutionBuilder) error {
	c.contribute(b)
	b.Covered(ExecutionCoverage{Complete: true})
	return nil
}
func executionTestEvidence(producer string) []ExecutionEvidence {
	return []ExecutionEvidence{{Revision: "content_a", Producer: ProducerVersion{Name: producer, Version: "1"},
		Classification: "static", Confidence: "exact", SourceHandles: []string{"sym_a"}, EvidenceIDs: []string{"ev_a"},
		Coverage: ExecutionCoverage{Complete: true}}}
}
func executionTestRequest() ExecutionRequest {
	return ExecutionRequest{Key: AnalysisKey{WorkspaceID: "ws_a", Epoch: 1, Revision: "content_a", Profile: "execution/v1", ExecutionDomain: "canonical"}}
}
func executionTestContributor(t *testing.T, name string) ExecutionContributor {
	return executionFixtureContributor{name, func(b *ExecutionBuilder) {
		for _, id := range []string{"a", "b", "c", "isolated"} {
			if err := b.Node(ExecutionNode{ID: id, Kind: "symbol", Path: "main.go", Name: id, Evidence: executionTestEvidence(name)}); err != nil {
				t.Fatal(err)
			}
		}
		for _, pair := range [][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}} {
			if err := b.Edge(ExecutionEdge{From: pair[0], To: pair[1], Kind: "calls", Evidence: executionTestEvidence(name)}); err != nil {
				t.Fatal(err)
			}
		}
	}}
}
func TestExecutionIdentityAndProvenance(t *testing.T) {
	request := executionTestRequest()
	a, err := BuildExecutionSnapshot(context.Background(), request, executionTestContributor(t, "one"), executionTestContributor(t, "two"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildExecutionSnapshot(context.Background(), request, executionTestContributor(t, "two"), executionTestContributor(t, "one"))
	if err != nil {
		t.Fatal(err)
	}
	if executionJSON(a) != executionJSON(b) {
		t.Fatal("contributor order changed snapshot")
	}
	if len(a.Execution.Edges) != 3 || len(a.Execution.Edges[0].Evidence) != 2 {
		t.Fatal("duplicate facts lost provenance")
	}
	for _, change := range []func(*AnalysisKey){
		func(k *AnalysisKey) { k.Epoch++ }, func(k *AnalysisKey) { k.Revision = "content_b" },
		func(k *AnalysisKey) { k.ExecutionDomain = "prepared:plan_a" }, func(k *AnalysisKey) { k.ConfigHash = "other" },
	} {
		key := cloneExecution(a.Key)
		change(&key)
		if key.ID() == a.ID {
			t.Fatal("different inputs share identity")
		}
	}
	key := cloneExecution(a.Key)
	key.ExecutionBudget.Nodes--
	if key.ID() == a.ID {
		t.Fatal("budget missing from identity")
	}
	a.Execution.Nodes[0].Evidence[0].EvidenceIDs[0] = "mutated"
	if b.Execution.Nodes[0].Evidence[0].EvidenceIDs[0] == "mutated" {
		t.Fatal("snapshots share mutable facts")
	}
}
func TestExecutionCycleAndAbsence(t *testing.T) {
	snapshot, err := BuildExecutionSnapshot(context.Background(), executionTestRequest(), executionTestContributor(t, "one"))
	if err != nil {
		t.Fatal(err)
	}
	graph := *snapshot.Execution
	if result := ExecutionGraphPaths(context.Background(), graph, "a", "c", 0, 0); result.Status != ExecutionStaticPossible || len(result.Paths) != 1 {
		t.Fatalf("%+v", result)
	}
	if result := ExecutionGraphPaths(context.Background(), graph, "a", "isolated", 0, 0); result.Status != ExecutionStaticallyUnreachable {
		t.Fatalf("%+v", result)
	}
	if result := ExecutionGraphPaths(context.Background(), graph, "a", "missing", 0, 0); result.Status != ExecutionUnknown {
		t.Fatalf("%+v", result)
	}
	if result := ExecutionGraphPaths(context.Background(), graph, "a", "c", 0, 1); result.Status != ExecutionUnknown || !result.Coverage.Capped {
		t.Fatalf("%+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := ExecutionGraphPaths(ctx, graph, "a", "isolated", 0, 0); result.Status != ExecutionUnknown {
		t.Fatalf("%+v", result)
	}
}
func TestExecutionCapsAndMissingContributor(t *testing.T) {
	for _, budget := range []GraphBudget{{Nodes: 1}, {Edges: 1}} {
		request := executionTestRequest()
		request.Budget = budget
		snapshot, err := BuildExecutionSnapshot(context.Background(), request, executionTestContributor(t, "one"))
		if err != nil {
			t.Fatal(err)
		}
		if !snapshot.Execution.Coverage.Capped || snapshot.Execution.Coverage.Complete {
			t.Fatal("cap called complete")
		}
		if result := ExecutionGraphPaths(context.Background(), *snapshot.Execution, "a", "isolated", 0, 0); result.Status != ExecutionUnknown {
			t.Fatalf("%+v", result)
		}
	}
	snapshot, err := BuildExecutionSnapshot(context.Background(), executionTestRequest())
	if err != nil || snapshot.Execution.Coverage.Complete || len(snapshot.Execution.Coverage.Gaps) == 0 {
		t.Fatalf("%+v %v", snapshot, err)
	}
	request := executionTestRequest()
	request.Budget.Nodes = MaxExecutionNodes + 1
	if _, err := BuildExecutionSnapshot(context.Background(), request); ErrorCode(err) != "graph_budget_invalid" {
		t.Fatal(err)
	}
}
func TestExecutionCacheIsolationAndConcurrentContributions(t *testing.T) {
	b := newExecutionBuilder("content_a", DefaultGraphBudget())
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			if err := b.Node(ExecutionNode{ID: fmt.Sprint(i), Kind: "symbol", Evidence: executionTestEvidence("one")}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	group.Wait()
	canonical := b.finish(executionTestRequest().Key)
	store := newAnalysisStore()
	store.put(canonical)
	canonical.Execution.Nodes[0].ID = "mutated"
	cached, ok := store.get(canonical.ID)
	if !ok || cached.Execution.Nodes[0].ID == "mutated" {
		t.Fatal("cache shares caller-owned graph")
	}
	cached.Execution.Nodes[0].ID = "mutated"
	again, _ := store.get(canonical.ID)
	if again.Execution.Nodes[0].ID == "mutated" {
		t.Fatal("cache returns shared graph")
	}
	for i := 0; i < maxAnalysisSnapshots+1; i++ {
		key := executionTestRequest().Key
		key.ExecutionDomain = fmt.Sprintf("prepared:%d", i)
		store.put(b.finish(key))
	}
	if _, ok := store.get(canonical.ID); ok {
		t.Fatal("snapshot retention exceeded")
	}
}

func TestExecutionIncompleteFactCannotProveAbsence(t *testing.T) {
	contributor := executionFixtureContributor{"one", func(b *ExecutionBuilder) {
		for _, id := range []string{"a", "b"} {
			evidence := executionTestEvidence("one")
			evidence[0].Coverage.Capped = true
			if err := b.Node(ExecutionNode{ID: id, Kind: "symbol", Evidence: evidence}); err != nil {
				t.Fatal(err)
			}
		}
	}}
	snapshot, err := BuildExecutionSnapshot(context.Background(), executionTestRequest(), contributor)
	if err != nil {
		t.Fatal(err)
	}
	result := ExecutionGraphPaths(context.Background(), *snapshot.Execution, "a", "b", 0, 0)
	if result.Status != ExecutionUnknown {
		t.Fatal("overall coverage concealed a capped fact")
	}
}

func TestExecutionConflictsAndByteCap(t *testing.T) {
	b := newExecutionBuilder("content_a", DefaultGraphBudget())
	for _, id := range []string{"a", "b"} {
		if err := b.Node(ExecutionNode{ID: id, Kind: "symbol", Evidence: executionTestEvidence("one")}); err != nil {
			t.Fatal(err)
		}
	}
	for _, condition := range []string{"true", "false"} {
		if err := b.Edge(ExecutionEdge{From: "a", To: "b", Kind: "calls", Condition: condition, Evidence: executionTestEvidence("one")}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := b.finish(executionTestRequest().Key)
	if len(snapshot.Execution.Edges) != 2 {
		t.Fatal("conflicting conditions merged")
	}
	budget := DefaultGraphBudget()
	budget.Bytes = 2 * maxExecutionMetadata
	b = newExecutionBuilder("content_a", budget)
	err := b.Node(ExecutionNode{ID: "large", Kind: "symbol", Name: strings.Repeat("x", budget.Bytes), Evidence: executionTestEvidence("one")})
	if err != nil {
		t.Fatal(err)
	}
	snapshot = b.finish(executionTestRequest().Key)
	encoded, _ := json.Marshal(snapshot.Execution)
	if len(encoded) > budget.Bytes || !snapshot.Execution.Coverage.Capped {
		t.Fatal("encoded byte budget not enforced")
	}
	err = b.Node(ExecutionNode{ID: "bad", Kind: "symbol", Path: "/tmp/sandbox/main.go", Evidence: executionTestEvidence("one")})
	if ErrorCode(err) != "graph_source_invalid" {
		t.Fatal("sandbox path accepted")
	}
}
