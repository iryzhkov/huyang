package workspace

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// NativeSectioner sections Go and Python documents without a semantic
// provider: Go through the standard parser, Python through its indentation.
// Other extensions have no sections, which FindSymbols and Outline treat as
// "nothing declared here" rather than as a parser failure.
type NativeSectioner struct{}

// Sections returns the top-level and nested declarations of a document with
// the byte range each one covers, including its leading documentation
// comment. Names of nested declarations use the Parent/Name path shape.
func (NativeSectioner) Sections(path string, content []byte) ([]Section, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return goSections(path, content)
	case ".py", ".pyi":
		return pythonSections(content), nil
	}
	if otherSourceExtensions[strings.ToLower(filepath.Ext(path))] {
		return nil, ErrNoNativeParser
	}
	return nil, nil
}

// ErrNoNativeParser marks a source language the native sectioner does not
// understand, so symbol coverage is reported as incomplete for it and the
// semantic provider is consulted.
var ErrNoNativeParser = errors.New("parser_unavailable: no native parser for this language")

// otherSourceExtensions are source languages that declare symbols the
// native sectioner cannot see. Data and documentation formats are absent on
// purpose: they declare nothing, so they do not make coverage incomplete.
var otherSourceExtensions = map[string]bool{
	".c": true, ".cc": true, ".cpp": true, ".cs": true, ".h": true, ".hpp": true, ".java": true, ".js": true,
	".jsx": true, ".kt": true, ".lua": true, ".m": true, ".mjs": true, ".php": true, ".rb": true, ".rs": true,
	".scala": true, ".sh": true, ".swift": true, ".ts": true, ".tsx": true, ".zig": true,
}

// SupportsExtension reports whether the native sectioner understands ext.
func (NativeSectioner) SupportsExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".go", ".py", ".pyi":
		return true
	}
	return false
}

func goSections(path string, content []byte) ([]Section, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, content, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var sections []Section
	for _, decl := range file.Decls {
		switch typed := decl.(type) {
		case *ast.FuncDecl:
			name := typed.Name.Name
			if typed.Recv != nil && len(typed.Recv.List) > 0 {
				name = receiverName(typed.Recv.List[0].Type) + "/" + name
			}
			sections = append(sections, goSection(fset, name, "function_declaration", typed.Doc, typed))
		case *ast.GenDecl:
			sections = append(sections, goGenDeclSections(fset, typed)...)
		}
	}
	return sections, nil
}

// goGenDeclSections names every specification of a var, const or type
// declaration; a grouped declaration yields one section per name, each
// covering the whole group so an edit of the group stays whole.
func goGenDeclSections(fset *token.FileSet, decl *ast.GenDecl) []Section {
	kind := map[token.Token]string{token.TYPE: "type_declaration", token.VAR: "var_declaration", token.CONST: "const_declaration"}[decl.Tok]
	if kind == "" {
		return nil
	}
	var sections []Section
	for _, spec := range decl.Specs {
		switch typed := spec.(type) {
		case *ast.TypeSpec:
			doc := decl.Doc
			if len(decl.Specs) > 1 && typed.Doc != nil {
				doc = typed.Doc
			}
			sections = append(sections, goSection(fset, typed.Name.Name, kind, doc, decl))
		case *ast.ValueSpec:
			for _, name := range typed.Names {
				sections = append(sections, goSection(fset, name.Name, kind, decl.Doc, decl))
			}
		}
	}
	return sections
}

func goSection(fset *token.FileSet, name, kind string, doc *ast.CommentGroup, node ast.Node) Section {
	start := node.Pos()
	if doc != nil {
		start = doc.Pos()
	}
	return Section{Name: name, Kind: kind, ByteStart: fset.Position(start).Offset, ByteEnd: fset.Position(node.End()).Offset}
}

func receiverName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.StarExpr:
		return receiverName(typed.X)
	case *ast.Ident:
		return typed.Name
	case *ast.IndexExpr:
		return receiverName(typed.X)
	case *ast.IndexListExpr:
		return receiverName(typed.X)
	}
	return "?"
}

// pythonSections finds def and class statements at any indentation. A
// block ends where the next line with indentation at or below the
// declaration's own begins, skipping blank lines and comments; decorators
// directly above a declaration belong to it.
func pythonSections(content []byte) []Section {
	lines := strings.SplitAfter(string(content), "\n")
	offsets := make([]int, len(lines)+1)
	for index, line := range lines {
		offsets[index+1] = offsets[index] + len(line)
	}
	type open struct {
		section Section
		indent  int
		index   int
	}
	var sections []Section
	var stack []open
	closeAbove := func(indent int, until int) {
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			top.section.ByteEnd = offsets[lastContentLine(lines, top.index, until)+1]
			sections[top.index] = top.section
		}
	}
	for index, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.TrimSpace(trimmed) == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(trimmed)
		closeAbove(indent, index)
		name, kind := pythonDeclaration(trimmed)
		if name == "" {
			continue
		}
		start := index
		for start > 0 && strings.HasPrefix(strings.TrimLeft(lines[start-1], " \t"), "@") {
			start--
		}
		for _, parent := range stack {
			_ = parent
		}
		path := name
		if len(stack) > 0 {
			path = stack[len(stack)-1].section.Name + "/" + name
		}
		section := Section{Name: path, Kind: kind, ByteStart: offsets[start], ByteEnd: offsets[index+1]}
		sections = append(sections, section)
		stack = append(stack, open{section: section, indent: indent, index: len(sections) - 1})
	}
	closeAbove(-1, len(lines))
	return sections
}

// lastContentLine returns the index of the last non-blank, non-comment line
// before until, at or after from.
func lastContentLine(lines []string, from, until int) int {
	last := from
	for index := from; index < until && index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != "" && !strings.HasPrefix(strings.TrimLeft(lines[index], " \t"), "#") {
			last = index
		}
	}
	return last
}

func pythonDeclaration(trimmed string) (string, string) {
	for _, prefix := range []string{"def ", "async def ", "class "} {
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		rest := strings.TrimPrefix(trimmed, prefix)
		end := strings.IndexAny(rest, "(:")
		if end < 0 {
			end = len(strings.TrimRight(rest, " \t\r\n"))
		}
		name := strings.TrimSpace(rest[:end])
		if name == "" {
			return "", ""
		}
		if prefix == "class " {
			return name, "class_definition"
		}
		return name, "function_definition"
	}
	return "", ""
}
