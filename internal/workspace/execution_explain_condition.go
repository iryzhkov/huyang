package workspace

import (
	"context"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
)

func explainConditionSamples(ctx context.Context, node ExecutionNode, trace ExecutionTrace) []ExecutionConditionExplanation {
	result := []ExecutionConditionExplanation{}
	for _, event := range trace.Events {
		if len(result) >= MaxExecutionExplanations || ctx.Err() != nil {
			break
		}
		if event.Kind != "stopped" || len(event.Frames) == 0 {
			continue
		}
		frame := event.Frames[0]
		if frame.Path != node.Path || frame.Line != node.Line || frame.Mapping != "launch_snapshot" || frame.SourceHash == "" || trace.SourceHashes[frame.Path] != frame.SourceHash {
			continue
		}
		sample := ExecutionConditionExplanation{Node: node.ID, Expression: node.Condition.Text, Sequence: event.Sequence, Thread: event.Thread,
			Status: "unknown", Reason: "Condition evaluation is unavailable or operands are missing, redacted, ambiguous or truncated.", Operands: []ExecutionTraceValue{}, CandidateEdges: []string{}}
		wanted := map[string]bool{}
		for _, name := range node.Condition.Variables {
			wanted[name] = true
		}
		for _, value := range event.Values {
			if wanted[value.Name] {
				sample.Operands = append(sample.Operands, value)
			}
		}
		if filepath.Ext(node.Path) == ".go" {
			if value, ok := evaluateTraceCondition(node.Condition.Text, sample.Operands); ok {
				sample.Value = &value
				sample.Status = "inferred"
				sample.Reason = "Pure scalar condition evaluated against this stopped operand snapshot; candidate edge only, not proof of subsequent execution."
			}
		}
		result = append(result, sample)
	}
	return result
}
func evaluateTraceCondition(expression string, values []ExecutionTraceValue) (bool, bool) {
	if len(expression) > MaxExecutionConditionBytes {
		return false, false
	}
	parsed, err := parser.ParseExpr(expression)
	if err != nil {
		return false, false
	}
	known := map[string]constant.Value{}
	for _, value := range values {
		if _, exists := known[value.Name]; exists {
			known[value.Name] = constant.MakeUnknown()
			continue
		}
		known[value.Name] = traceScalar(value)
	}
	result := traceExpression(parsed, known, 0)
	if result.Kind() != constant.Bool {
		return false, false
	}
	return constant.BoolVal(result), true
}
func traceScalar(value ExecutionTraceValue) constant.Value {
	if value.Redacted || value.Truncated {
		return constant.MakeUnknown()
	}
	switch value.Type {
	case "bool":
		if value.Value == "true" {
			return constant.MakeBool(true)
		}
		if value.Value == "false" {
			return constant.MakeBool(false)
		}
	case "int", "int8", "int16", "int32", "int64":
		if number, err := strconv.ParseInt(value.Value, 10, 64); err == nil {
			return constant.MakeInt64(number)
		}
	case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
		if number, err := strconv.ParseUint(value.Value, 10, 64); err == nil {
			return constant.MakeUint64(number)
		}
	}
	return constant.MakeUnknown()
}
func traceExpression(expression ast.Expr, known map[string]constant.Value, depth int) constant.Value {
	if depth > MaxExecutionDepth {
		return constant.MakeUnknown()
	}
	switch node := expression.(type) {
	case *ast.ParenExpr:
		return traceExpression(node.X, known, depth+1)
	case *ast.Ident:
		if value := known[node.Name]; value != nil {
			return value
		}
	case *ast.BasicLit:
		if node.Kind == token.INT {
			return constant.MakeFromLiteral(node.Value, token.INT, 0)
		}
	case *ast.UnaryExpr:
		value := traceExpression(node.X, known, depth+1)
		if node.Op == token.NOT && value.Kind() == constant.Bool {
			return constant.MakeBool(!constant.BoolVal(value))
		}
		if node.Op == token.SUB && value.Kind() == constant.Int {
			return constant.UnaryOp(token.SUB, value, 0)
		}
	case *ast.BinaryExpr:
		left, right := traceExpression(node.X, known, depth+1), traceExpression(node.Y, known, depth+1)
		return traceComparison(node.Op, left, right)
	}
	return constant.MakeUnknown()
}
func traceComparison(op token.Token, left, right constant.Value) constant.Value {
	if left.Kind() != right.Kind() || left.Kind() == constant.Unknown {
		return constant.MakeUnknown()
	}
	if left.Kind() == constant.Bool {
		switch op {
		case token.LAND:
			return constant.MakeBool(constant.BoolVal(left) && constant.BoolVal(right))
		case token.LOR:
			return constant.MakeBool(constant.BoolVal(left) || constant.BoolVal(right))
		case token.EQL, token.NEQ:
			return constant.MakeBool(constant.Compare(left, op, right))
		}
	}
	if left.Kind() == constant.Int {
		switch op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return constant.MakeBool(constant.Compare(left, op, right))
		}
	}
	return constant.MakeUnknown()
}
