package workspace

// The surface other code is written against.
//
// An exported declaration is a promise: somebody outside this file compiled
// against its name, its signature and, for a type, its member set. Changing
// one of those breaks code this workspace may not even contain, which is
// exactly the kind of consequence a diff does not show and a test suite
// inside one repository does not catch.
//
// So a file's exported surface is read from the bytes, twice - as the
// workspace holds it and as a plan would leave it - and compared. Two
// adapters read it: Go through the language's own parser, and TypeScript
// through a scanner over its export forms.
//
// The scanner is the honest part of this. It knows the export forms the
// fixtures and ordinary code use, and when it meets an export line it cannot
// classify it says the file is not covered rather than reporting the names it
// did understand as the whole surface. A surface read by nobody is unknown; a
// surface read wrongly would be a false accusation or, worse, a false
// all-clear.

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

// apiSurfaceSymbolLimit bounds the exported declarations read from one file.
// A file past it is reported as not covered rather than truncated, because a
// truncated surface makes everything after the cut look removed.
const apiSurfaceSymbolLimit = 512

// APISymbol is one exported declaration as callers depend on it.
type APISymbol struct {
	Name string `json:"name"`
	// Kind is what the declaration is: func, method, type, interface, const,
	// var, or reexport for a name a barrel passes through.
	Kind      string `json:"kind"`
	Signature string `json:"signature,omitempty"`
	// Members is the method set of an interface or the exported field set of
	// a struct: adding to one of those is a change implementors must follow.
	Members []string `json:"members,omitempty"`
}

// APISurface is everything one file promises, and whether anybody could read
// it.
type APISurface struct {
	Path    string               `json:"path"`
	Symbols map[string]APISymbol `json:"symbols,omitempty"`
	// Covered says an adapter read this file and understood all of it.
	Covered bool   `json:"covered"`
	Reason  string `json:"reason,omitempty"`
	// Exists distinguishes a file with no exports from one that is not there,
	// which is the difference between "promises nothing" and "promised
	// everything it used to and does not any more".
	Exists bool `json:"exists"`
}

// APIChange is one difference between two surfaces of the same file.
type APIChange struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// Kind is added, removed, signature_changed or members_changed.
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// Breaking reports whether this change can break code written against the
// surface. Adding a declaration cannot; removing one, changing what it takes
// or returns, and changing a type's member set all can.
func (c APIChange) Breaking() bool { return c.Kind != "added" }

// ReadAPISurface reads one file's exported surface with the adapter for its
// language. A language no adapter covers is reported as such.
func ReadAPISurface(path string, content []byte, exists bool) APISurface {
	surface := APISurface{Path: path, Symbols: map[string]APISymbol{}, Exists: exists}
	if !exists {
		// A file that is not there has no surface, and that is a fact rather
		// than a gap: everything it used to export is gone.
		surface.Covered = apiAdapterFor(path) != ""
		if !surface.Covered {
			surface.Reason = "no API adapter reads " + filepath.Ext(path)
		}
		return surface
	}
	switch apiAdapterFor(path) {
	case "go":
		return goAPISurface(surface, content)
	case "typescript":
		return typescriptAPISurface(surface, content)
	}
	surface.Reason = "no API adapter reads " + filepath.Ext(path)
	return surface
}

func apiAdapterFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".mjs", ".jsx":
		return "typescript"
	}
	return ""
}

// CompareAPISurfaces lists what changed between two surfaces of one file.
func CompareAPISurfaces(before, after APISurface) []APIChange {
	var changes []APIChange
	for name, symbol := range before.Symbols {
		current, present := after.Symbols[name]
		if !present {
			changes = append(changes, APIChange{
				Path: before.Path, Name: name, Kind: "removed",
				Detail: fmt.Sprintf("%s %s is no longer exported", symbol.Kind, name),
			})
			continue
		}
		if current.Signature != symbol.Signature {
			changes = append(changes, APIChange{
				Path: before.Path, Name: name, Kind: "signature_changed",
				Detail: fmt.Sprintf("%s %s was %q and is %q", symbol.Kind, name, symbol.Signature, current.Signature),
			})
		}
		if added, removed := memberDelta(symbol.Members, current.Members); len(added)+len(removed) > 0 {
			changes = append(changes, APIChange{
				Path: before.Path, Name: name, Kind: "members_changed",
				Detail: fmt.Sprintf("%s %s gained [%s] and lost [%s]", symbol.Kind, name,
					strings.Join(added, ", "), strings.Join(removed, ", ")),
			})
		}
	}
	for name, symbol := range after.Symbols {
		if _, present := before.Symbols[name]; !present {
			changes = append(changes, APIChange{
				Path: after.Path, Name: name, Kind: "added",
				Detail: fmt.Sprintf("%s %s is now exported", symbol.Kind, name),
			})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Name != changes[j].Name {
			return changes[i].Name < changes[j].Name
		}
		return changes[i].Kind < changes[j].Kind
	})
	return changes
}

func memberDelta(before, after []string) (added, removed []string) {
	present := map[string]bool{}
	for _, member := range after {
		present[member] = true
	}
	for _, member := range before {
		if !present[member] {
			removed = append(removed, member)
		}
		delete(present, member)
	}
	for member := range present {
		added = append(added, member)
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// goAPISurface reads the exported surface with the language's own parser, so
// what it reports is what a compiler would see.
func goAPISurface(surface APISurface, content []byte) APISurface {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, surface.Path, content, parser.SkipObjectResolution)
	if err != nil {
		surface.Reason = "this file does not parse: " + err.Error()
		return surface
	}
	for _, declaration := range file.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if symbol, ok := goFunctionSymbol(fileSet, typed); ok {
				surface.Symbols[symbol.Name] = symbol
			}
		case *ast.GenDecl:
			for _, symbol := range goDeclarationSymbols(fileSet, typed) {
				surface.Symbols[symbol.Name] = symbol
			}
		}
		if len(surface.Symbols) > apiSurfaceSymbolLimit {
			surface.Symbols = map[string]APISymbol{}
			surface.Reason = fmt.Sprintf("more than %d exported declarations", apiSurfaceSymbolLimit)
			return surface
		}
	}
	surface.Covered = true
	return surface
}

// goFunctionSymbol names a function by itself and a method by its receiver, so
// two types with the same method name stay two promises.
func goFunctionSymbol(fileSet *token.FileSet, declaration *ast.FuncDecl) (APISymbol, bool) {
	name := declaration.Name.Name
	if !ast.IsExported(name) {
		return APISymbol{}, false
	}
	kind := "func"
	if declaration.Recv != nil && len(declaration.Recv.List) == 1 {
		receiver := strings.TrimPrefix(goNodeText(fileSet, declaration.Recv.List[0].Type), "*")
		if !ast.IsExported(receiver) {
			// A method on an unexported type is not part of the surface.
			return APISymbol{}, false
		}
		kind, name = "method", receiver+"."+name
	}
	return APISymbol{Name: name, Kind: kind, Signature: goNodeText(fileSet, declaration.Type)}, true
}

func goDeclarationSymbols(fileSet *token.FileSet, declaration *ast.GenDecl) []APISymbol {
	var symbols []APISymbol
	for _, spec := range declaration.Specs {
		switch typed := spec.(type) {
		case *ast.TypeSpec:
			if !ast.IsExported(typed.Name.Name) {
				continue
			}
			symbols = append(symbols, goTypeSymbol(fileSet, typed))
		case *ast.ValueSpec:
			kind := "var"
			if declaration.Tok == token.CONST {
				kind = "const"
			}
			for _, name := range typed.Names {
				if !ast.IsExported(name.Name) {
					continue
				}
				symbols = append(symbols, APISymbol{
					Name: name.Name, Kind: kind, Signature: goNodeText(fileSet, typed.Type),
				})
			}
		}
	}
	return symbols
}

// goTypeSymbol records an interface by its method set and a struct by its
// exported fields, because both are what an implementor or a caller writes
// against.
func goTypeSymbol(fileSet *token.FileSet, spec *ast.TypeSpec) APISymbol {
	symbol := APISymbol{Name: spec.Name.Name, Kind: "type"}
	switch underlying := spec.Type.(type) {
	case *ast.InterfaceType:
		symbol.Kind = "interface"
		for _, method := range underlying.Methods.List {
			symbol.Members = append(symbol.Members, goFieldMembers(fileSet, method)...)
		}
	case *ast.StructType:
		symbol.Kind = "struct"
		for _, field := range underlying.Fields.List {
			for _, member := range goFieldMembers(fileSet, field) {
				if name, _, _ := strings.Cut(member, " "); ast.IsExported(name) {
					symbol.Members = append(symbol.Members, member)
				}
			}
		}
	default:
		symbol.Signature = goNodeText(fileSet, spec.Type)
	}
	sort.Strings(symbol.Members)
	return symbol
}

// goFieldMembers is one field or method line as "name type", or the embedded
// type on its own when it has no name.
func goFieldMembers(fileSet *token.FileSet, field *ast.Field) []string {
	typeText := goNodeText(fileSet, field.Type)
	if len(field.Names) == 0 {
		return []string{typeText}
	}
	members := make([]string, 0, len(field.Names))
	for _, name := range field.Names {
		members = append(members, name.Name+" "+typeText)
	}
	return members
}

func goNodeText(fileSet *token.FileSet, node ast.Node) string {
	if node == nil {
		return ""
	}
	var out bytes.Buffer
	if err := (&printer.Config{Mode: printer.RawFormat}).Fprint(&out, fileSet, node); err != nil {
		return ""
	}
	return strings.Join(strings.Fields(out.String()), " ")
}
