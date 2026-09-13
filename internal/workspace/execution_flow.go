package workspace

import (
	"context"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
)

const MaxExecutionExpandedFunctions = 16
const MaxExecutionConditionBytes = 1024

// ExecutionCondition describes syntax at this revision. Constant is only set
// for an identifier-free constant expression; arbitrary evaluation is unsafe.
type ExecutionCondition struct {
	Text        string   `json:"text"`
	ContentHash string   `json:"content_hash"`
	Variables   []string `json:"variables"`
	Constant    *bool    `json:"constant,omitempty"`
}

type goExecutionFlow struct {
	ctx    context.Context
	source ExecutionSource
	owner  string
	exit   string
	set    *token.FileSet
	graph  ExecutionGraph
	facts  *ExecutionCallFacts
	count  int
	depth  int
}

// AcquireGoExecutionFlow expands only selected declarations. A selected body
// is bounded independently; the call graph remains an interprocedural summary.
func AcquireGoExecutionFlow(ctx context.Context, revision string, sources []ExecutionSource, graph ExecutionGraph, selected []string) ExecutionCallFacts {
	facts := ExecutionCallFacts{Producer: ProducerVersion{Name: "go_cfg", Version: "1"},
		Coverage: ExecutionCoverage{Complete: false, Gaps: []string{"cfg_is_intraprocedural"}, Limits: []string{}}}
	wanted := map[string]bool{}
	for i, id := range selected {
		if i >= MaxExecutionExpandedFunctions {
			facts.Coverage.Capped = true
			facts.Coverage.Limits = append(facts.Coverage.Limits, "expanded_functions")
			break
		}
		wanted[id] = true
	}
	for _, source := range sources {
		if ctx.Err() != nil {
			facts.Coverage.Gaps = append(facts.Coverage.Gaps, "analysis_cancelled")
			break
		}
		if filepath.Ext(source.Path) != ".go" {
			continue
		}
		relevant := false
		for _, node := range graph.Nodes {
			if wanted[node.ID] && node.Path == source.Path {
				relevant = true
				break
			}
		}
		if !relevant {
			continue
		}
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, source.Path, source.Content, 0)
		if err != nil {
			facts.Coverage.Gaps = append(facts.Coverage.Gaps, "cfg_parse_failed")
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			pos := set.Position(fn.Name.Pos())
			id := ExecutionSymbolID(source.Path, pos.Line, pos.Column)
			if !wanted[id] {
				continue
			}
			delete(wanted, id)
			f := goExecutionFlow{ctx: ctx, source: source, owner: id, set: set, graph: graph, facts: &facts}
			f.exit = f.node(fn.End(), "exit", "function exit", nil)
			entry := f.block(fn.Body.List, f.exit)
			f.edge(id, entry, "enters", "")
		}
	}
	for id := range wanted {
		facts.Risks = append(facts.Risks, ExecutionRisk{Kind: "cfg_adapter_unavailable", Node: id, Detail: "No selected Go declaration was expanded"})
	}
	return facts
}
func (f *goExecutionFlow) nodeLimit() int {
	if limit := f.graph.Budget.FunctionNodes; limit > 0 && limit < MaxExecutionFunctionNodes {
		return limit
	}
	return MaxExecutionFunctionNodes
}

func (f *goExecutionFlow) evidence() []ExecutionEvidence {
	return []ExecutionEvidence{{Revision: f.graph.Revision, Producer: f.facts.Producer, Confidence: "parser", Classification: "static",
		SourceHandles: []string{}, EvidenceIDs: []string{}, Coverage: ExecutionCoverage{Complete: false, Gaps: []string{"cfg_is_intraprocedural"}, Limits: []string{}}}}
}
func (f *goExecutionFlow) gap(reason string) {
	f.facts.Coverage.Gaps = uniqueSorted(append(f.facts.Coverage.Gaps, reason))
}
func (f *goExecutionFlow) node(at token.Pos, kind, name string, condition *ExecutionCondition) string {
	if f.ctx.Err() != nil {
		f.gap("analysis_cancelled")
		return ""
	}
	if f.count >= f.nodeLimit() || len(f.facts.Nodes) >= MaxExecutionNodes {
		f.facts.Coverage.Capped = true
		f.facts.Coverage.Limits = uniqueSorted(append(f.facts.Coverage.Limits, "function_nodes"))
		return ""
	}
	f.count++
	pos := f.set.Position(at)
	id := fmt.Sprintf("cfg:%s:%d:%d:%s:%d", f.owner, pos.Line, pos.Column, kind, f.count)
	f.facts.Nodes = append(f.facts.Nodes, ExecutionNode{ID: id, Owner: f.owner, Kind: kind, Name: sanitizeText(name, MaxExecutionConditionBytes),
		Path: f.source.Path, Line: pos.Line, Column: pos.Column, Condition: condition, Evidence: f.evidence()})
	return id
}
func (f *goExecutionFlow) edge(from, to, kind, condition string) {
	if from == "" || to == "" {
		return
	}
	if len(f.facts.Edges) >= MaxExecutionEdges {
		f.facts.Coverage.Capped = true
		f.facts.Coverage.Limits = uniqueSorted(append(f.facts.Coverage.Limits, "edges"))
		return
	}
	edge := ExecutionEdge{From: from, To: to, Kind: kind, Condition: condition, Evidence: f.evidence()}
	for _, node := range f.facts.Nodes {
		if node.ID == from {
			edge.SitePath, edge.SiteLine, edge.SiteColumn = node.Path, node.Line, node.Column
			break
		}
	}
	if edge.SitePath == "" {
		for _, node := range f.graph.Nodes {
			if node.ID == from {
				edge.SitePath, edge.SiteLine, edge.SiteColumn = node.Path, node.Line, node.Column
				break
			}
		}
	}
	f.facts.Edges = append(f.facts.Edges, edge)
}
func (f *goExecutionFlow) text(node ast.Node) string {
	if node == nil {
		return ""
	}
	start, end := f.set.Position(node.Pos()).Offset, f.set.Position(node.End()).Offset
	if start < 0 || end > len(f.source.Content) || end < start {
		return ""
	}
	return f.source.Content[start:end]
}
func (f *goExecutionFlow) condition(expr ast.Expr) *ExecutionCondition {
	if expr == nil {
		return nil
	}
	text := f.text(expr)
	c := &ExecutionCondition{Text: sanitizeText(text, MaxExecutionConditionBytes), ContentHash: hashBytes([]byte(text)), Variables: []string{}}
	if len(text) > MaxExecutionConditionBytes {
		f.gap("condition_text_truncated")
	}
	ast.Inspect(expr, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok && len(c.Variables) < MaxExecutionValues {
			c.Variables = uniqueSorted(append(c.Variables, id.Name))
		}
		return len(c.Variables) < MaxExecutionValues
	})
	if len(c.Variables) >= MaxExecutionValues {
		f.gap("condition_variables_capped")
	}
	// Even true can be shadowed in another file. Only identifier-free
	// constant expressions can be evaluated without package type resolution.
	if len(c.Variables) == 0 && len(text) <= MaxExecutionConditionBytes {
		if value, err := types.Eval(f.set, nil, token.NoPos, text); err == nil && value.Value != nil && value.Value.Kind() == constant.Bool {
			result := constant.BoolVal(value.Value)
			c.Constant = &result
		}
	}
	return c
}
