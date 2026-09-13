package workspace

// The TypeScript side of the exported surface.
//
// There is no TypeScript parser in this process, so this reads the export
// forms directly: declarations, re-exports through a barrel, and the star
// re-export. It is deliberately literal about what it understands. An export
// line it cannot classify makes the whole file uncovered, because a scanner
// that quietly skips a form it does not know would report the names it did
// understand as the complete surface - and the next comparison would call
// every name it missed "removed", or worse, call a removal compatible.

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// A declaration exported where it is declared.
	typescriptDeclaration = regexp.MustCompile(`^export\s+(?:declare\s+)?(?:default\s+)?(?:abstract\s+)?(async\s+function|function|class|interface|type|enum|const|let|var|namespace)\s+([A-Za-z_$][\w$]*)`)
	// A barrel: export { a, b as c } from "./x", or the same without a source.
	typescriptNamedExport = regexp.MustCompile(`^export\s+(?:type\s+)?\{([^}]*)\}\s*(?:from\s*['"]([^'"]+)['"])?`)
	// export * from "./x", with or without a namespace name.
	typescriptStarExport = regexp.MustCompile(`^export\s+\*\s*(?:as\s+([A-Za-z_$][\w$]*)\s*)?from\s*['"]([^'"]+)['"]`)
	// export default <expression>, which is one promise under one name.
	typescriptDefaultExport = regexp.MustCompile(`^export\s+default\b`)
)

// typescriptAPISurface reads the exported names of one TypeScript or
// JavaScript file, and the shape of each as far as the source states it.
func typescriptAPISurface(surface APISurface, content []byte) APISurface {
	lines := strings.Split(string(content), "\n")
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(line, "export") {
			continue
		}
		symbols, understood := typescriptExports(line, lines, index)
		if !understood {
			surface.Symbols = map[string]APISymbol{}
			surface.Reason = fmt.Sprintf("line %d is an export form this adapter does not read: %s", index+1, line)
			return surface
		}
		for _, symbol := range symbols {
			surface.Symbols[symbol.Name] = symbol
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

// typescriptExports reads one export line. The second result is false when the
// line is an export this adapter does not understand, which is the answer that
// keeps the file honest rather than half-read.
func typescriptExports(line string, lines []string, index int) ([]APISymbol, bool) {
	if match := typescriptStarExport.FindStringSubmatch(line); match != nil {
		name := "*"
		if match[1] != "" {
			name = match[1]
		}
		return []APISymbol{{Name: name, Kind: "reexport", Signature: "* from " + match[2]}}, true
	}
	if match := typescriptNamedExport.FindStringSubmatch(line); match != nil {
		return typescriptNamedExports(match), true
	}
	if match := typescriptDeclaration.FindStringSubmatch(line); match != nil {
		return []APISymbol{typescriptDeclarationSymbol(match, line, lines, index)}, true
	}
	if typescriptDefaultExport.MatchString(line) {
		return []APISymbol{{Name: "default", Kind: "default", Signature: typescriptSignature(line)}}, true
	}
	return nil, false
}

// typescriptNamedExports records one entry per name a barrel passes through,
// under the name importers use, with the module it comes from. Changing where
// a name comes from is a change to the promise even when the name is the same.
func typescriptNamedExports(match []string) []APISymbol {
	source := match[2]
	var symbols []APISymbol
	for _, entry := range strings.Split(match[1], ",") {
		entry = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(entry), "type "))
		if entry == "" {
			continue
		}
		original, exported := entry, entry
		if before, after, found := strings.Cut(entry, " as "); found {
			original, exported = strings.TrimSpace(before), strings.TrimSpace(after)
		}
		signature := original
		if source != "" {
			signature = original + " from " + source
		}
		symbols = append(symbols, APISymbol{Name: exported, Kind: "reexport", Signature: signature})
	}
	return symbols
}

// typescriptDeclarationSymbol records a declaration by its own line, and an
// interface or class by the member lines inside its braces: a method added to
// an interface is work for every implementor.
func typescriptDeclarationSymbol(match []string, line string, lines []string, index int) APISymbol {
	keyword, name := strings.TrimPrefix(match[1], "async "), match[2]
	symbol := APISymbol{Name: name, Kind: keyword, Signature: typescriptSignature(line)}
	if keyword != "interface" && keyword != "class" {
		return symbol
	}
	symbol.Members = typescriptMembers(lines, index)
	return symbol
}

// typescriptMembers are the lines inside a braced declaration, normalised and
// without comments. Brace counting is enough here: it is the block the
// declaration opened, read to the line that closes it.
func typescriptMembers(lines []string, index int) []string {
	depth, started := 0, false
	var members []string
	for cursor := index; cursor < len(lines); cursor++ {
		line := strings.TrimSpace(lines[cursor])
		opened, closed := strings.Count(line, "{"), strings.Count(line, "}")
		if started && depth == 1 && line != "" && !strings.HasPrefix(line, "//") &&
			!strings.HasPrefix(line, "/*") && !strings.HasPrefix(line, "*") && line != "}" {
			members = append(members, strings.Join(strings.Fields(strings.TrimSuffix(line, ";")), " "))
		}
		depth += opened - closed
		if opened > 0 {
			started = true
		}
		if started && depth <= 0 {
			break
		}
	}
	return members
}

// typescriptSignature is the declaration line without its body or trailing
// punctuation, with whitespace normalised so reformatting is not a change.
func typescriptSignature(line string) string {
	if index := strings.Index(line, "{"); index >= 0 {
		line = line[:index]
	}
	line = strings.TrimRight(strings.TrimSpace(line), ";,")
	return strings.Join(strings.Fields(line), " ")
}
