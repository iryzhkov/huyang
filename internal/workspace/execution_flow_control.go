package workspace

import "go/ast"

func (f *goExecutionFlow) block(statements []ast.Stmt, next string) string {
	f.depth++
	defer func() { f.depth-- }()
	if f.depth > MaxExecutionDepth {
		f.gap("cfg_depth_exceeded")
		f.facts.Coverage.Capped = true
		f.facts.Coverage.Limits = uniqueSorted(append(f.facts.Coverage.Limits, "depth"))
		return ""
	}
	for i := len(statements) - 1; i >= 0; i-- {
		if f.count >= f.nodeLimit() || f.ctx.Err() != nil {
			f.node(statements[i].Pos(), "capped", "", nil)
			return ""
		}
		next = f.statement(statements[i], next)
	}
	return next
}
func (f *goExecutionFlow) statement(statement ast.Stmt, next string) string {
	switch s := statement.(type) {
	case *ast.BlockStmt:
		return f.block(s.List, next)
	case *ast.IfStmt:
		branch := f.node(s.Cond.Pos(), "condition", f.text(s.Cond), f.condition(s.Cond))
		yes := f.block(s.Body.List, next)
		no := next
		if s.Else != nil {
			no = f.block([]ast.Stmt{s.Else}, next)
		}
		f.edge(branch, yes, "branch_true", branch)
		f.edge(branch, no, "branch_false", branch)
		entry := f.calls(s.Cond, branch, "calls")
		if s.Init != nil {
			entry = f.statement(s.Init, entry)
		}
		return entry
	case *ast.ForStmt:
		branch := f.node(s.Pos(), "condition", "loop", f.condition(s.Cond))
		check := f.calls(s.Cond, branch, "calls")
		back := check
		if s.Post != nil {
			back = f.statement(s.Post, check)
		}
		f.edge(branch, f.block(s.Body.List, back), "branch_true", branch)
		if s.Cond != nil {
			f.edge(branch, next, "branch_false", branch)
		}
		entry := check
		if s.Init != nil {
			entry = f.statement(s.Init, entry)
		}
		return entry
	case *ast.RangeStmt:
		branch := f.node(s.Pos(), "condition", "range iteration", f.condition(s.X))
		f.edge(branch, f.block(s.Body.List, branch), "branch_true", branch)
		f.edge(branch, next, "branch_false", branch)
		f.gap("range_iteration_runtime_dependent")
		return f.calls(s.X, branch, "calls")
	case *ast.ReturnStmt:
		node := f.node(s.Pos(), "exit", "return", nil)
		f.edge(node, f.exit, "returns", "")
		return f.calls(s, node, "calls")
	case *ast.GoStmt:
		node := f.node(s.Pos(), "async_boundary", "goroutine spawn", nil)
		f.edge(node, next, "continues", "")
		f.gap("async_schedule_unknown")
		return f.calls(s.Call, node, "spawns")
	case *ast.DeferStmt:
		node := f.node(s.Pos(), "async_boundary", "deferred invocation", nil)
		f.edge(node, next, "continues", "")
		f.gap("defer_exit_order_not_expanded")
		return f.calls(s.Call, node, "schedules")
	case *ast.SwitchStmt:
		return f.choices(s, s.Body.List, next)
	case *ast.TypeSwitchStmt:
		return f.choices(s, s.Body.List, next)
	case *ast.SelectStmt:
		return f.choices(s, s.Body.List, next)
	case *ast.BranchStmt:
		f.gap("jump_target_unresolved")
		return f.node(s.Pos(), "unresolved", s.Tok.String()+" target", nil)
	case *ast.LabeledStmt:
		f.gap("label_control_flow_incomplete")
		return f.statement(s.Stmt, next)
	}
	node := f.node(statement.Pos(), "basic_block", "statement", nil)
	f.edge(node, next, "continues", "")
	return f.calls(statement, node, "calls")
}
func (f *goExecutionFlow) choices(statement ast.Stmt, clauses []ast.Stmt, next string) string {
	branch := f.node(statement.Pos(), "condition", "switch/select alternatives", nil)
	f.gap("case_predicates_and_fallthrough_incomplete")
	hasDefault := false
	for _, clause := range clauses {
		var body []ast.Stmt
		switch c := clause.(type) {
		case *ast.CaseClause:
			body = c.Body
			hasDefault = hasDefault || len(c.List) == 0
		case *ast.CommClause:
			body = c.Body
			hasDefault = hasDefault || c.Comm == nil
		}
		choice := f.node(clause.Pos(), "condition", "case alternative", nil)
		f.edge(branch, choice, "branch_true", choice)
		f.edge(choice, f.block(body, next), "branch_true", choice)
	}
	if _, selectStmt := statement.(*ast.SelectStmt); !hasDefault && !selectStmt {
		f.edge(branch, next, "branch_false", branch)
	}
	return branch
}
