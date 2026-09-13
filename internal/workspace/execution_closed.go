package workspace

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// ClosedGoExecution certifies only an import-free package of nullary functions
// composed of direct calls, constant branches and returns. Its deliberately
// small grammar excludes values, callbacks, methods, channels and implicit
// initialization. A refusal is a coverage gap, never a negative path answer.
func ClosedGoExecution(ctx context.Context, request ExecutionRequest, sources []ExecutionSource, directory string) (AnalysisSnapshot, error) {
	set := token.NewFileSet()
	functions := map[string]*ast.FuncDecl{}
	nodes := map[string]ExecutionNode{}
	known := map[string]bool{}
	facts := ExecutionCallFacts{Producer: ProducerVersion{Name: "closed_go_calls", Version: "1"},
		Coverage: ExecutionCoverage{Complete: true, Gaps: []string{}, Limits: []string{}}}
	evidence := func() []ExecutionEvidence {
		return []ExecutionEvidence{{Revision: request.Key.Revision, Producer: facts.Producer,
			Confidence: "native", Classification: "static", SourceHandles: []string{}, EvidenceIDs: []string{}, Coverage: facts.Coverage}}
	}
	packageName := ""
	for _, source := range sources {
		if ctx.Err() != nil {
			return AnalysisSnapshot{}, Coded("analysis_cancelled", ctx.Err())
		}
		if filepath.Dir(source.Path) != directory || filepath.Ext(source.Path) != ".go" {
			continue
		}
		known[filepath.Base(source.Path)] = true
		if strings.Contains(source.Content, "//go:") || strings.Contains(source.Content, "//line ") || strings.Contains(source.Content, "/*line ") {
			return AnalysisSnapshot{}, closedGoUnavailable()
		}
		file, err := parser.ParseFile(set, source.Path, source.Content, 0)
		if err != nil || len(file.Imports) > 0 {
			return AnalysisSnapshot{}, closedGoUnavailable()
		}
		if packageName != "" && packageName != file.Name.Name {
			return AnalysisSnapshot{}, closedGoUnavailable()
		}
		packageName = file.Name.Name
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !closedGoSignature(fn) || functions[fn.Name.Name] != nil || len(nodes) >= MaxExecutionNodes {
				return AnalysisSnapshot{}, closedGoUnavailable()
			}
			functions[fn.Name.Name] = fn
			pos := set.Position(fn.Name.Pos())
			node := ExecutionNode{ID: ExecutionSymbolID(source.Path, pos.Line, pos.Column), Kind: "symbol", Name: fn.Name.Name, Path: source.Path, Line: pos.Line, Column: pos.Column, Evidence: evidence()}
			nodes[node.Name] = node
			facts.Nodes = append(facts.Nodes, node)
		}
	}
	if len(functions) == 0 || !closedGoInventory(request.Root, directory, known) {
		return AnalysisSnapshot{}, closedGoUnavailable()
	}
	for name, fn := range functions {
		if ctx.Err() != nil {
			return AnalysisSnapshot{}, Coded("analysis_cancelled", ctx.Err())
		}
		if !closedGoBody(fn.Body, functions, 0) {
			return AnalysisSnapshot{}, closedGoUnavailable()
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && len(facts.Edges) < MaxExecutionEdges {
				target := nodes[call.Fun.(*ast.Ident).Name]
				pos := set.Position(call.Pos())
				facts.Edges = append(facts.Edges, ExecutionEdge{From: nodes[name].ID, To: target.ID, Kind: "calls", SitePath: nodes[name].Path, SiteLine: pos.Line, SiteColumn: pos.Column, Evidence: evidence()})
			}
			return true
		})
		if len(facts.Edges) >= MaxExecutionEdges {
			return AnalysisSnapshot{}, closedGoUnavailable()
		}
	}
	request.Key.Profile += "/closed_go/" + hashBytes([]byte(directory))[:16]
	return BuildExecutionSnapshot(ctx, request, facts)
}
func ClosedGoInventoryComplete(root string, sources []ExecutionSource, directory string) bool {
	known := map[string]bool{}
	for _, source := range sources {
		if filepath.Dir(source.Path) == directory && filepath.Ext(source.Path) == ".go" {
			known[filepath.Base(source.Path)] = true
		}
	}
	return closedGoInventory(root, directory, known)
}
func ClosedGoMainTarget(sources []ExecutionSource, target ExecutionNode) bool {
	if ast.IsExported(target.Name) || target.Name == "main" || target.Name == "init" {
		return false
	}
	for _, source := range sources {
		if source.Path == target.Path {
			file, err := parser.ParseFile(token.NewFileSet(), source.Path, source.Content, parser.PackageClauseOnly)
			return err == nil && file.Name.Name == "main"
		}
	}
	return false
}
func closedGoUnavailable() error {
	return Codedf("execution_proof_unavailable", "package is outside the closed Go proof grammar or inventory")
}
func closedGoSignature(fn *ast.FuncDecl) bool {
	return fn.Recv == nil && fn.Body != nil && fn.Type.TypeParams == nil &&
		len(fn.Type.Params.List) == 0 && (fn.Type.Results == nil || len(fn.Type.Results.List) == 0)
}
func closedGoInventory(root, directory string, known map[string]bool) bool {
	if filepath.IsAbs(directory) || strings.HasPrefix(filepath.Clean(directory), "..") {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(root, directory))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch filepath.Ext(entry.Name()) {
		case ".go":
			if !known[entry.Name()] || entry.Type()&os.ModeSymlink != 0 {
				return false
			}
		case ".s", ".S", ".syso", ".c", ".cc", ".cpp", ".h":
			return false
		}
	}
	return true
}
