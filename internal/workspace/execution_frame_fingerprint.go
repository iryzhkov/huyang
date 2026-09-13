package workspace

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

// Token identity includes the entire declaration, signature and body. It only
// tolerates layout/comments, never an incompatible signature or semantic edit.
func ExecutionFrameFingerprint(source ExecutionSource, frame ExecutionTraceFrame) (string, int) {
	if filepath.Ext(source.Path) != ".go" || strings.Contains(source.Content, "//go:") || strings.Contains(source.Content, "//line ") {
		return "", 0
	}
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, source.Path, source.Content, 0)
	if err != nil {
		return "", 0
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || frame.Name != fn.Name.Name && !strings.HasSuffix(frame.Name, "."+fn.Name.Name) {
			continue
		}
		start, end := set.Position(fn.Pos()), set.Position(fn.End())
		if frame.Line < start.Line || frame.Line > end.Line {
			continue
		}
		return fingerprintFunction(source.Content[start.Offset:end.Offset], frame.Line-start.Line+1)
	}
	return "", 0
}
func fingerprintFunction(content string, line int) (string, int) {
	set := token.NewFileSet()
	file := set.AddFile("function", set.Base(), len(content))
	var lexer scanner.Scanner
	lexer.Init(file, []byte(content), nil, 0)
	parts := []string{}
	offset, index := 0, 0
	for {
		pos, kind, literal := lexer.Scan()
		if kind == token.EOF {
			break
		}
		// Inserted and explicit semicolons have the same lexical meaning.
		if kind == token.SEMICOLON {
			literal = ";"
		}
		index++
		if offset == 0 && file.Position(pos).Line >= line {
			offset = index
		}
		parts = append(parts, kind.String()+":"+strconv.Quote(literal))
	}
	if offset == 0 {
		return "", 0
	}
	return hashBytes([]byte(strings.Join(parts, "\n"))), offset
}
