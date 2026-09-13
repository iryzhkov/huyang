package workspace

// Sections for the two formats an agent edits constantly and could not
// outline: Markdown prose and TOML configuration. They declare nothing a
// language server would diagnose, so these sections are navigation rather
// than symbols - they make a long document readable one part at a time, and
// addressable by name - and the extensions stay out of IsSemanticSource, so
// an edit to them still expects no semantic verdict.
//
// Both are native, without a parser install or a running provider, because
// these are exactly the files found in repositories that have no language
// server at all. Markdown goes through goldmark, which the parser stage
// already runs over every document, so fenced code and setext headings are
// handled by the same reader that validates them.

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// maxSectionNameBytes bounds a section name taken from a document, because a
// heading is a sentence often enough to be worth clipping.
const maxSectionNameBytes = 80

type documentHeading struct {
	level int
	start int
	name  string
	path  string
}

// markdownSections names every heading, nested by level, each covering the
// heading line through to the next heading at the same or a higher level. A
// section therefore contains its subsections, the way a Go type contains its
// methods.
func markdownSections(content []byte) []Section {
	document := goldmark.DefaultParser().Parse(text.NewReader(content))
	var headings []documentHeading
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		heading, ok := node.(*ast.Heading)
		if !ok || !entering || heading.Lines().Len() == 0 {
			return ast.WalkContinue, nil
		}
		segment := heading.Lines().At(0)
		if name := sectionName(string(content[segment.Start:segment.Stop])); name != "" {
			headings = append(headings, documentHeading{
				level: heading.Level, start: lineStart(content, segment.Start), name: name,
			})
		}
		return ast.WalkSkipChildren, nil
	})
	return headingSections(headings, len(content))
}

// headingSections turns the headings of a document into sections: the name
// path of each one is its chain of ancestors, and its range ends where the
// next heading of the same or a higher level begins.
func headingSections(headings []documentHeading, total int) []Section {
	sections := make([]Section, 0, len(headings))
	var ancestors []documentHeading
	for index, heading := range headings {
		for len(ancestors) > 0 && ancestors[len(ancestors)-1].level >= heading.level {
			ancestors = ancestors[:len(ancestors)-1]
		}
		heading.path = heading.name
		if len(ancestors) > 0 {
			heading.path = ancestors[len(ancestors)-1].path + "/" + heading.name
		}
		ancestors = append(ancestors, heading)
		end := total
		for _, later := range headings[index+1:] {
			if later.level <= heading.level {
				end = later.start
				break
			}
		}
		sections = append(sections, Section{Name: heading.path, Kind: "heading", ByteStart: heading.start, ByteEnd: end})
	}
	return sections
}

// tomlSections names every table, from its header line to the next header.
// Tables do not nest in the file - [tool] ends where [tool.ruff] begins - so
// each section is the block a reader would edit, and the dotted name becomes
// the name path shape every other locator uses.
func tomlSections(content []byte) []Section {
	lines := strings.SplitAfter(string(content), "\n")
	offsets := make([]int, len(lines)+1)
	for index, line := range lines {
		offsets[index+1] = offsets[index] + len(line)
	}
	var sections []Section
	open := ""
	for index, line := range lines {
		if open == "" {
			if name, kind, ok := tomlTableHeader(line); ok {
				if count := len(sections); count > 0 {
					sections[count-1].ByteEnd = offsets[index]
				}
				sections = append(sections, Section{Name: name, Kind: kind, ByteStart: offsets[index], ByteEnd: len(content)})
			}
		}
		open = tomlStringState(line, open)
	}
	return sections
}

// tomlTableHeader reads a table or array-of-tables header. Anything after
// the closing bracket other than a comment means the line is not a header.
func tomlTableHeader(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	kind, closing, from := "table", "]", 1
	if strings.HasPrefix(trimmed, "[[") {
		kind, closing, from = "table_array", "]]", 2
	} else if !strings.HasPrefix(trimmed, "[") {
		return "", "", false
	}
	end := strings.Index(trimmed, closing)
	if end < from {
		return "", "", false
	}
	if rest := strings.TrimSpace(trimmed[end+len(closing):]); rest != "" && !strings.HasPrefix(rest, "#") {
		return "", "", false
	}
	name := tomlKeyPath(trimmed[from:end])
	if name == "" {
		return "", "", false
	}
	return name, kind, true
}

// tomlKeyPath turns a dotted table name into the Parent/Name path shape,
// splitting on the dots that are not inside a quoted key.
func tomlKeyPath(header string) string {
	var parts []string
	var current strings.Builder
	quote := byte(0)
	for index := 0; index < len(header); index++ {
		character := header[index]
		switch {
		case quote != 0:
			if character == quote {
				quote = 0
				continue
			}
			current.WriteByte(character)
		case character == '"' || character == '\'':
			quote = character
		case character == '.':
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteByte(character)
		}
	}
	parts = append(parts, current.String())
	for index := range parts {
		if parts[index] = sectionName(parts[index]); parts[index] == "" {
			return ""
		}
	}
	return strings.Join(parts, "/")
}

// tomlStringState advances the multi-line string state across one line, so a
// bracketed line inside a string is not read as a table header. open is the
// delimiter still waiting to be closed, empty outside a string.
func tomlStringState(line, open string) string {
	for index := 0; index < len(line); {
		if open != "" {
			next := strings.Index(line[index:], open)
			if next < 0 {
				return open
			}
			index, open = index+next+len(open), ""
			continue
		}
		switch {
		case strings.HasPrefix(line[index:], `"""`):
			index, open = index+3, `"""`
		case strings.HasPrefix(line[index:], `'''`):
			index, open = index+3, `'''`
		case line[index] == '#':
			return ""
		default:
			index++
		}
	}
	return open
}

// sectionName is the name a document gives a section, reduced to something a
// locator can carry: one line, no name-path separator inside a segment, and
// bounded.
func sectionName(raw string) string {
	name := strings.Join(strings.Fields(strings.TrimRight(strings.TrimSpace(raw), " \t#")), " ")
	name = strings.ReplaceAll(name, "/", "-")
	if len(name) > maxSectionNameBytes {
		name = strings.TrimSpace(name[:maxSectionNameBytes])
	}
	return name
}

// lineStart is the offset of the first byte of the line offset falls on, so
// a heading section begins at its own markers rather than at its text.
func lineStart(content []byte, offset int) int {
	if offset > len(content) {
		offset = len(content)
	}
	for offset > 0 && content[offset-1] != '\n' {
		offset--
	}
	return offset
}
