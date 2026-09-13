package workspace

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

// ExecutionCallFacts is an adapter's detached result. It contains no provider
// objects and can be contributed to the same snapshot as any other adapter.
type ExecutionCallFacts struct {
	Producer ProducerVersion
	Nodes    []ExecutionNode
	Edges    []ExecutionEdge
	Risks    []ExecutionRisk
	Coverage ExecutionCoverage
}

func (c ExecutionCallFacts) Name() string    { return c.Producer.Name }
func (c ExecutionCallFacts) Version() string { return c.Producer.Version }
func (c ExecutionCallFacts) ContributeExecution(ctx context.Context, _ ExecutionRequest, b *ExecutionBuilder) error {
	for _, node := range c.Nodes {
		if ctx.Err() != nil {
			return Coded("analysis_cancelled", ctx.Err())
		}
		if err := b.Node(node); err != nil {
			return err
		}
	}
	for _, edge := range c.Edges {
		if ctx.Err() != nil {
			return Coded("analysis_cancelled", ctx.Err())
		}
		if err := b.Edge(edge); err != nil {
			return err
		}
	}
	for _, risk := range c.Risks {
		b.Risk(risk)
	}
	b.Covered(c.Coverage)
	return nil
}

func ExecutionSymbolID(path string, line, column int) string {
	return fmt.Sprintf("function:%s:%d:%d", path, line, column)
}

type goExecutionFunction struct {
	source ExecutionSource
	decl   *ast.FuncDecl
	node   ExecutionNode
}
type goExecutionAcquisition struct {
	ctx       context.Context
	revision  string
	budget    GraphBudget
	facts     ExecutionCallFacts
	fileset   *token.FileSet
	functions []goExecutionFunction
	byName    map[string][]ExecutionNode
}

// AcquireGoExecutionCalls is the provider-disabled Go fallback. A syntax
// reader can name calls and candidate declarations, but cannot resolve a
// shadowed function value or prove a complete interface implementation set.
func AcquireGoExecutionCalls(ctx context.Context, revision string, sources []ExecutionSource, budget GraphBudget) ExecutionCallFacts {
	if budget.Nodes == 0 {
		budget = DefaultGraphBudget()
	}
	a := goExecutionAcquisition{ctx: ctx, revision: revision, budget: budget, fileset: token.NewFileSet(), byName: map[string][]ExecutionNode{},
		facts: ExecutionCallFacts{Producer: ProducerVersion{Name: "go_parser_calls", Version: "1"}, Coverage: ExecutionCoverage{
			Complete: false, Limits: []string{}, Gaps: []string{"parser_call_resolution_is_conservative"}}}}
	for _, source := range sources {
		if ctx.Err() != nil {
			a.gap("analysis_cancelled")
			break
		}
		if filepath.Ext(source.Path) != ".go" {
			continue
		}
		file, err := parser.ParseFile(a.fileset, source.Path, source.Content, 0)
		if err != nil {
			a.gap("go_parse_failed")
			continue
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			a.function(source, function)
		}
	}
	for _, function := range a.functions {
		a.calls(function)
	}
	sort.Slice(a.facts.Nodes, func(i, j int) bool { return a.facts.Nodes[i].ID < a.facts.Nodes[j].ID })
	return a.facts
}
func (a *goExecutionAcquisition) gap(reason string) {
	a.facts.Coverage.Gaps = uniqueSorted(append(a.facts.Coverage.Gaps, reason))
}
func (a *goExecutionAcquisition) cap(limit string) {
	a.facts.Coverage.Capped = true
	a.facts.Coverage.Limits = uniqueSorted(append(a.facts.Coverage.Limits, limit))
}
func (a *goExecutionAcquisition) evidence() []ExecutionEvidence {
	return []ExecutionEvidence{{Revision: a.revision, Producer: a.facts.Producer, Classification: "static", Confidence: "parser",
		SourceHandles: []string{}, EvidenceIDs: []string{}, Coverage: ExecutionCoverage{Complete: false, Limits: []string{}, Gaps: []string{"syntax_only"}}}}
}
func (a *goExecutionAcquisition) function(source ExecutionSource, function *ast.FuncDecl) {
	if len(a.facts.Nodes) >= a.budget.Nodes {
		a.cap("nodes")
		return
	}
	pos := a.fileset.Position(function.Name.Pos())
	node := ExecutionNode{ID: ExecutionSymbolID(source.Path, pos.Line, pos.Column), Kind: "symbol", Path: source.Path, Name: function.Name.Name, Line: pos.Line, Column: pos.Column, Evidence: a.evidence()}
	a.facts.Nodes = append(a.facts.Nodes, node)
	a.byName[node.Name] = append(a.byName[node.Name], node)
	a.functions = append(a.functions, goExecutionFunction{source, function, node})
}
func (a *goExecutionAcquisition) calls(function goExecutionFunction) {
	ast.Inspect(function.decl.Body, func(node ast.Node) bool {
		if a.ctx.Err() != nil {
			a.gap("analysis_cancelled")
			return false
		}
		if len(a.facts.Edges) >= a.budget.Edges {
			a.cap("edges")
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		pos := a.fileset.Position(call.Pos())
		candidates, name := a.targets(function, call)
		if len(candidates) == 0 {
			candidates = []ExecutionNode{a.unresolved(function, pos, name)}
		}
		for _, target := range candidates {
			if len(a.facts.Edges) >= a.budget.Edges {
				a.cap("edges")
				break
			}
			a.facts.Edges = append(a.facts.Edges, ExecutionEdge{From: function.node.ID, To: target.ID, Kind: "calls",
				SitePath: function.source.Path, SiteLine: pos.Line, SiteColumn: pos.Column, Evidence: a.evidence()})
		}
		return true
	})
}
func (a *goExecutionAcquisition) targets(function goExecutionFunction, call *ast.CallExpr) ([]ExecutionNode, string) {
	name := ""
	switch target := call.Fun.(type) {
	case *ast.Ident:
		if target.Obj != nil && target.Obj.Kind != ast.Fun {
			return nil, "function_value:" + target.Name
		}
		name = target.Name
	case *ast.SelectorExpr:
		name = target.Sel.Name
	default:
		return nil, "function_value_or_callback"
	}
	candidates := []ExecutionNode{}
	for _, candidate := range a.byName[name] {
		if filepath.Dir(candidate.Path) == filepath.Dir(function.source.Path) {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, name
}
func (a *goExecutionAcquisition) unresolved(function goExecutionFunction, pos token.Position, name string) ExecutionNode {
	id := fmt.Sprintf("unresolved:%s:%d:%d", function.source.Path, pos.Line, pos.Column)
	node := ExecutionNode{ID: id, Kind: "unresolved", Path: function.source.Path, Name: strings.TrimSpace(name), Line: pos.Line, Column: pos.Column, Evidence: a.evidence()}
	if len(a.facts.Nodes) >= a.budget.Nodes {
		a.cap("nodes")
		return node
	}
	a.facts.Nodes = append(a.facts.Nodes, node)
	if len(a.facts.Risks) < maxExecutionGaps {
		a.facts.Risks = append(a.facts.Risks, ExecutionRisk{Kind: "dynamic_or_external_target", Node: id, Detail: "Syntax alone cannot resolve " + sanitizeText(name, 80)})
	}
	return node
}
