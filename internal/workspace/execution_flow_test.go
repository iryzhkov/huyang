package workspace

import (
	"context"
	"strings"
	"testing"
)

func executionFlowFixture(t *testing.T, content, name string) (ExecutionCallFacts, ExecutionGraph) {
	t.Helper()
	sources := []ExecutionSource{{Path: "main.go", Content: content}}
	calls := AcquireGoExecutionCalls(context.Background(), "content_a", sources, DefaultGraphBudget())
	request := executionTestRequest()
	base, err := BuildExecutionSnapshot(context.Background(), request, calls)
	if err != nil {
		t.Fatal(err)
	}
	selected := ""
	for _, node := range base.Execution.Nodes {
		if node.Name == name && node.Kind == "symbol" {
			selected = node.ID
		}
	}
	if selected == "" {
		t.Fatal("fixture selection missing")
	}
	flow := AcquireGoExecutionFlow(context.Background(), "content_a", sources, *base.Execution, []string{selected})
	snapshot, err := BuildExecutionSnapshot(context.Background(), request, calls, flow)
	if err != nil {
		t.Fatal(err)
	}
	return flow, *snapshot.Execution
}
func TestExecutionFlowEarlyReturnSeparatesBranches(t *testing.T) {
	flow, graph := executionFlowFixture(t, `package sample
func target(){}
func selected(n int){ if n<0 { return }; target() }
func unrelated(){ target() }
`, "selected")
	var branch ExecutionNode
	target := ""
	for _, node := range graph.Nodes {
		if node.Condition != nil && node.Condition.Text == "n<0" {
			branch = node
		}
		if node.Kind == "symbol" && node.Name == "target" {
			target = node.ID
		}
		if node.Owner != "" && strings.Contains(node.Owner, "function:main.go:4:") {
			t.Fatal("expanded unrelated function")
		}
	}
	if branch.ID == "" || len(branch.Condition.Variables) != 1 || branch.Condition.Variables[0] != "n" {
		t.Fatalf("%+v", branch)
	}
	for _, edge := range flow.Edges {
		if edge.From != branch.ID {
			continue
		}
		paths := ExecutionGraphPaths(context.Background(), graph, edge.To, target, 0, 0)
		if edge.Kind == "branch_true" && len(paths.Paths) > 0 {
			t.Fatal("early return reaches later call")
		}
		if edge.Kind == "branch_false" && len(paths.Paths) == 0 {
			t.Fatal("fallthrough lost later call")
		}
	}
	if flow.Coverage.Complete {
		t.Fatal("CFG promoted to complete program proof")
	}
}
func TestExecutionFlowCapsAndAsyncRemainExplicit(t *testing.T) {
	flow, graph := executionFlowFixture(t, `package sample
func target(){}
func selected(ch chan int){ go target(); defer target(); ch<-1; value:=<-ch; _=value; if 1<2 { target() }; for { target() } }
`, "selected")
	kinds := map[string]bool{}
	constantFound := false
	for _, edge := range flow.Edges {
		kinds[edge.Kind] = true
	}
	for _, node := range graph.Nodes {
		if node.Condition != nil && node.Condition.Constant != nil && *node.Condition.Constant {
			constantFound = true
		}
	}
	if !kinds["spawns"] || !kinds["schedules"] || !kinds["sends"] || !kinds["receives"] || !constantFound {
		t.Fatalf("kinds=%v constant=%v", kinds, constantFound)
	}
	flow, _ = executionFlowFixture(t, "package sample\nfunc target(){}\nfunc selected(){"+strings.Repeat("target();", 400)+"}", "selected")
	if !flow.Coverage.Capped || len(flow.Nodes) > MaxExecutionFunctionNodes {
		t.Fatalf("%+v nodes=%d", flow.Coverage, len(flow.Nodes))
	}
}
func TestExecutionFlowDoesNotTreatShadowableTrueAsConstant(t *testing.T) {
	flow, _ := executionFlowFixture(t, "package sample\nfunc selected(){if true {return}}", "selected")
	for _, node := range flow.Nodes {
		if node.Condition != nil && node.Condition.Constant != nil {
			t.Fatal("unresolved predeclared identifier evaluated")
		}
	}
}
