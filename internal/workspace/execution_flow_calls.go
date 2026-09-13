package workspace

import (
	"go/ast"
	"go/token"
	"sort"
)

func (f *goExecutionFlow) receives(node ast.Node, next string) string {
	visits := 0
	ast.Inspect(node, func(n ast.Node) bool {
		visits++
		if visits > MaxExecutionNodes || f.ctx.Err() != nil {
			f.gap("receive_scan_incomplete")
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if unary, ok := n.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
			boundary := f.node(unary.Pos(), "async_boundary", "channel receive", nil)
			f.edge(boundary, next, "receives", "")
			next = boundary
		}
		return true
	})
	return next
}

// Calls remain candidate edges, including unresolved callback targets. The
// local continuation never enters another function's return path.
func (f *goExecutionFlow) calls(node ast.Node, next, kind string) string {
	if node == nil {
		return next
	}
	sites := []*ast.CallExpr{}
	ast.Inspect(node, func(n ast.Node) bool {
		if f.ctx.Err() != nil || len(sites) >= MaxExecutionFunctionNodes {
			f.gap("call_slice_capped")
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			f.gap("callback_body_unexpanded")
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			sites = append(sites, call)
		}
		if unary, ok := n.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
			f.gap("channel_happens_before_unknown")
		}
		return true
	})
	sort.Slice(sites, func(i, j int) bool { return sites[i].Pos() > sites[j].Pos() })
	if len(sites) > 1 {
		f.gap("expression_evaluation_order_approximate")
	}
	for _, call := range sites {
		callID := f.node(call.Pos(), "call_site", f.text(call.Fun), nil)
		pos := f.set.Position(call.Pos())
		found := false
		for _, edge := range f.graph.Edges {
			if edge.From == f.owner && edge.SitePath == f.source.Path && edge.SiteLine == pos.Line && edge.SiteColumn == pos.Column {
				targetKind := kind
				if call != node {
					targetKind = "calls"
				}
				if edge.Kind == "dynamic_dispatch" && targetKind == "calls" {
					targetKind = "dynamic_dispatch"
				}
				f.edge(callID, edge.To, targetKind, "")
				found = true
			}
		}
		if !found {
			f.gap("call_site_target_unresolved")
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "panic" && ident.Obj == nil {
			f.edge(callID, f.exit, "panics", "")
			f.gap("panic_recovery_unexpanded")
		} else {
			f.edge(callID, next, "continues", "")
		}
		next = callID
	}
	if send, ok := node.(*ast.SendStmt); ok {
		boundary := f.node(send.Pos(), "async_boundary", "channel send", nil)
		f.edge(boundary, next, "sends", "")
		f.gap("channel_happens_before_unknown")
		next = boundary
	}
	return f.receives(node, next)
}
