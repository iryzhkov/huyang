package workspace

import "go/ast"

func closedGoBody(body *ast.BlockStmt, functions map[string]*ast.FuncDecl, depth int) bool {
	if depth > MaxExecutionDepth || len(body.List) > MaxExecutionFunctionNodes {
		return false
	}
	for _, statement := range body.List {
		switch s := statement.(type) {
		case *ast.EmptyStmt:
		case *ast.ReturnStmt:
			if len(s.Results) > 0 {
				return false
			}
		case *ast.BlockStmt:
			if !closedGoBody(s, functions, depth+1) {
				return false
			}
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || len(call.Args) > 0 {
				return false
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || functions[id.Name] == nil {
				return false
			}
		case *ast.IfStmt:
			if s.Init != nil || !closedGoCondition(s.Cond, functions) || !closedGoBody(s.Body, functions, depth+1) {
				return false
			}
			if s.Else != nil && !closedGoBody(&ast.BlockStmt{List: []ast.Stmt{s.Else}}, functions, depth+1) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func closedGoCondition(expr ast.Expr, functions map[string]*ast.FuncDecl) bool {
	// No identifiers other than unshadowed built-in booleans, no calls and no
	// conversions. Both branches remain in the overapproximation.
	switch e := expr.(type) {
	case *ast.Ident:
		return (e.Name == "true" || e.Name == "false") && functions[e.Name] == nil
	case *ast.ParenExpr:
		return closedGoCondition(e.X, functions)
	default:
		return false
	}
}
