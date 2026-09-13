package workspace

import (
	"strings"
	"testing"
)

func TestMarkdownSectionsSimple(t *testing.T) {
	content := []byte("# Heading\nContent")
	sections := markdownSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Name != "Heading" {
		t.Errorf("expected name 'Heading', got %q", sections[0].Name)
	}
	if sections[0].Kind != "heading" {
		t.Errorf("expected kind 'heading', got %q", sections[0].Kind)
	}
	if sections[0].ByteStart != 0 {
		t.Errorf("expected ByteStart 0, got %d", sections[0].ByteStart)
	}
	if sections[0].ByteEnd != len(content) {
		t.Errorf("expected ByteEnd %d, got %d", len(content), sections[0].ByteEnd)
	}
}

func TestMarkdownSectionsNested(t *testing.T) {
	content := []byte("# Parent\nParent content\n## Child\nChild content\n### Grandchild\nGrandchild content")
	sections := markdownSections(content)
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(sections))
	}
	if sections[0].Name != "Parent" {
		t.Errorf("expected first section 'Parent', got %q", sections[0].Name)
	}
	// Parent is a document title, so Child doesn't include it in path
	if sections[1].Name != "Child" {
		t.Errorf("expected second section 'Child', got %q", sections[1].Name)
	}
	// Grandchild is nested under Child
	if sections[2].Name != "Child/Grandchild" {
		t.Errorf("expected third section 'Child/Grandchild', got %q", sections[2].Name)
	}
}

func TestMarkdownSectionsBoundaries(t *testing.T) {
	content := []byte("# First\nFirst content\n# Second\nSecond content")
	sections := markdownSections(content)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}
	if sections[0].ByteEnd != sections[1].ByteStart {
		t.Errorf("expected first section to end where second begins, got %d and %d", sections[0].ByteEnd, sections[1].ByteStart)
	}
}

func TestMarkdownSectionsDocumentTitle(t *testing.T) {
	content := []byte("# Title\n## Section One\nContent\n## Section Two\nMore")
	sections := markdownSections(content)
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(sections))
	}
	if sections[0].Name != "Title" {
		t.Errorf("expected first section 'Title', got %q", sections[0].Name)
	}
	if sections[1].Name != "Section One" {
		t.Errorf("expected second section 'Section One', got %q", sections[1].Name)
	}
	if sections[2].Name != "Section Two" {
		t.Errorf("expected third section 'Section Two', got %q", sections[2].Name)
	}
}

func TestMarkdownSectionsDocumentNoTitle(t *testing.T) {
	content := []byte("# One\nContent\n# Two\nMore")
	sections := markdownSections(content)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}
	if sections[0].Name != "One" {
		t.Errorf("expected first section 'One', got %q", sections[0].Name)
	}
	if sections[1].Name != "Two" {
		t.Errorf("expected second section 'Two', got %q", sections[1].Name)
	}
}

func TestMarkdownSectionsSetextHeadings(t *testing.T) {
	content := []byte("Setext Level 1\n===============\nContent\n")
	sections := markdownSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section for setext heading, got %d", len(sections))
	}
	if sections[0].Name != "Setext Level 1" {
		t.Errorf("expected 'Setext Level 1', got %q", sections[0].Name)
	}
}

func TestMarkdownSectionsFencedCodeBlock(t *testing.T) {
	content := []byte("# Real Heading\nContent\n\n```\n# Not a heading\nCode content\n```\n\nMore content")
	sections := markdownSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section (code block ignored), got %d", len(sections))
	}
	if sections[0].Name != "Real Heading" {
		t.Errorf("expected 'Real Heading', got %q", sections[0].Name)
	}
}

func TestMarkdownSectionsEmpty(t *testing.T) {
	content := []byte("")
	sections := markdownSections(content)
	if len(sections) != 0 {
		t.Errorf("expected 0 sections for empty document, got %d", len(sections))
	}
}

func TestMarkdownSectionsHeadingOnly(t *testing.T) {
	content := []byte("# Only Heading")
	sections := markdownSections(content)
	if len(sections) != 1 {
		t.Errorf("expected 1 section for heading-only document, got %d", len(sections))
	}
	if sections[0].ByteEnd != len(content) {
		t.Errorf("section should extend to end of document")
	}
}

func TestMarkdownSectionsNoTrailingNewline(t *testing.T) {
	content := []byte("# Heading\nContent with no newline")
	sections := markdownSections(content)
	if len(sections) != 1 {
		t.Errorf("expected 1 section, got %d", len(sections))
	}
	if sections[0].ByteEnd != len(content) {
		t.Errorf("section should extend to end even without trailing newline")
	}
}

func TestMarkdownSectionsSectionNameCleaning(t *testing.T) {
	content := []byte("#   Multiple   Spaces   \nContent")
	sections := markdownSections(content)
	if len(sections) != 1 {
		t.Errorf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Name != "Multiple Spaces" {
		t.Errorf("expected 'Multiple Spaces', got %q", sections[0].Name)
	}
}

func TestMarkdownSectionsMultipleLevels(t *testing.T) {
	content := []byte("# Top\n## Level 2\nContent 2\n### Level 3\nContent 3\n## Another Level 2\nContent 2b")
	sections := markdownSections(content)
	if len(sections) != 4 {
		t.Fatalf("expected 4 sections, got %d", len(sections))
	}
	if sections[0].Name != "Top" {
		t.Errorf("section 0: expected 'Top', got %q", sections[0].Name)
	}
	// Top is a document title, so Level 2 doesn't include it
	if sections[1].Name != "Level 2" {
		t.Errorf("section 1: expected 'Level 2', got %q", sections[1].Name)
	}
	// Level 3 is nested under Level 2
	if sections[2].Name != "Level 2/Level 3" {
		t.Errorf("section 2: expected 'Level 2/Level 3', got %q", sections[2].Name)
	}
	// Another Level 2 is at same level as first Level 2
	if sections[3].Name != "Another Level 2" {
		t.Errorf("section 3: expected 'Another Level 2', got %q", sections[3].Name)
	}
}

func TestTomlSectionsSimple(t *testing.T) {
	content := []byte("[section]\nkey = \"value\"")
	sections := tomlSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Name != "section" {
		t.Errorf("expected name 'section', got %q", sections[0].Name)
	}
	if sections[0].Kind != "table" {
		t.Errorf("expected kind 'table', got %q", sections[0].Kind)
	}
}

func TestTomlSectionsNested(t *testing.T) {
	content := []byte("[tool]\nname = \"value\"\n[tool.ruff]\nline-length = 100")
	sections := tomlSections(content)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}
	if sections[0].Name != "tool" {
		t.Errorf("expected first 'tool', got %q", sections[0].Name)
	}
	if sections[1].Name != "tool/ruff" {
		t.Errorf("expected second 'tool/ruff', got %q", sections[1].Name)
	}
	if sections[0].ByteEnd != sections[1].ByteStart {
		t.Errorf("first section should end where second begins")
	}
}

func TestTomlSectionsArrayOfTables(t *testing.T) {
	content := []byte("[[products]]\nname = \"Hammer\"\n[[products]]\nname = \"Nail\"")
	sections := tomlSections(content)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}
	if sections[0].Name != "products" {
		t.Errorf("expected first 'products', got %q", sections[0].Name)
	}
	if sections[0].Kind != "table_array" {
		t.Errorf("expected first kind 'table_array', got %q", sections[0].Kind)
	}
	if sections[1].Name != "products" {
		t.Errorf("expected second 'products', got %q", sections[1].Name)
	}
	if sections[1].Kind != "table_array" {
		t.Errorf("expected second kind 'table_array', got %q", sections[1].Kind)
	}
}

func TestTomlSectionsWithComments(t *testing.T) {
	content := []byte("# This is a comment\n[section]\nkey = \"value\"")
	sections := tomlSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Name != "section" {
		t.Errorf("expected 'section', got %q", sections[0].Name)
	}
}

func TestTomlSectionsStringWithBrackets(t *testing.T) {
	content := []byte("key = \"[not a header]\"\n[actual]\nvalue = 1")
	sections := tomlSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Name != "actual" {
		t.Errorf("expected 'actual', got %q", sections[0].Name)
	}
}

func TestTomlSectionsMultilineString(t *testing.T) {
	content := []byte("key = \"\"\"\nThis has [brackets] inside\n\"\"\"\n[section]\nvalue = 1")
	sections := tomlSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Name != "section" {
		t.Errorf("expected 'section', got %q", sections[0].Name)
	}
}

func TestTomlSectionsEmpty(t *testing.T) {
	content := []byte("")
	sections := tomlSections(content)
	if len(sections) != 0 {
		t.Errorf("expected 0 sections for empty document, got %d", len(sections))
	}
}

func TestTomlSectionsNoTrailingNewline(t *testing.T) {
	content := []byte("[section]\nkey = \"value\"")
	sections := tomlSections(content)
	if len(sections) != 1 {
		t.Errorf("expected 1 section, got %d", len(sections))
	}
	if sections[0].ByteEnd != len(content) {
		t.Errorf("section should extend to end of document")
	}
}

func TestHasDocumentTitle(t *testing.T) {
	tests := []struct {
		name     string
		headings []documentHeading
		want     bool
	}{
		{
			name:     "no headings",
			headings: []documentHeading{},
			want:     false,
		},
		{
			name: "one heading",
			headings: []documentHeading{
				{level: 1, name: "Title"},
			},
			want: false,
		},
		{
			name: "title above all others",
			headings: []documentHeading{
				{level: 1, name: "Title"},
				{level: 2, name: "Section"},
				{level: 3, name: "Subsection"},
			},
			want: true,
		},
		{
			name: "first has peer at same level",
			headings: []documentHeading{
				{level: 1, name: "First"},
				{level: 1, name: "Second"},
			},
			want: false,
		},
		{
			name: "first deeper than peer",
			headings: []documentHeading{
				{level: 2, name: "First"},
				{level: 1, name: "Second"},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasDocumentTitle(tt.headings)
			if got != tt.want {
				t.Errorf("hasDocumentTitle() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTomlTableHeader(t *testing.T) {
	tests := []struct {
		line      string
		wantName  string
		wantKind  string
		wantValid bool
	}{
		{
			line:      "[simple]",
			wantName:  "simple",
			wantKind:  "table",
			wantValid: true,
		},
		{
			line:      "  [spaced]  ",
			wantName:  "spaced",
			wantKind:  "table",
			wantValid: true,
		},
		{
			line:      "[nested.table]",
			wantName:  "nested/table",
			wantKind:  "table",
			wantValid: true,
		},
		{
			line:      "[[array]]",
			wantName:  "array",
			wantKind:  "table_array",
			wantValid: true,
		},
		{
			line:      "[section] # comment",
			wantName:  "section",
			wantKind:  "table",
			wantValid: true,
		},
		{
			line:      "not a header",
			wantName:  "",
			wantKind:  "",
			wantValid: false,
		},
		{
			line:      "[incomplete",
			wantName:  "",
			wantKind:  "",
			wantValid: false,
		},
		{
			line:      "[has] extra stuff",
			wantName:  "",
			wantKind:  "",
			wantValid: false,
		},
		{
			line:      "[\"\"]",
			wantName:  "",
			wantKind:  "",
			wantValid: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			name, kind, valid := tomlTableHeader(tt.line)
			if valid != tt.wantValid {
				t.Errorf("tomlTableHeader() valid = %v, want %v", valid, tt.wantValid)
			}
			if name != tt.wantName {
				t.Errorf("tomlTableHeader() name = %q, want %q", name, tt.wantName)
			}
			if kind != tt.wantKind {
				t.Errorf("tomlTableHeader() kind = %q, want %q", kind, tt.wantKind)
			}
		})
	}
}

func TestTomlKeyPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantPath string
	}{
		{
			name:     "simple",
			input:    "simple",
			wantPath: "simple",
		},
		{
			name:     "two levels",
			input:    "level1.level2",
			wantPath: "level1/level2",
		},
		{
			name:     "three levels",
			input:    "a.b.c",
			wantPath: "a/b/c",
		},
		{
			name:     "quoted key",
			input:    "\"quoted.key\".other",
			wantPath: "quoted.key/other",
		},
		{
			name:     "single quoted",
			input:    "'single'.key",
			wantPath: "single/key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tomlKeyPath(tt.input)
			if got != tt.wantPath {
				t.Errorf("tomlKeyPath(%q) = %q, want %q", tt.input, got, tt.wantPath)
			}
		})
	}
}

func TestTomlStringState(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		open     string
		wantOpen string
	}{
		{
			name:     "no string",
			line:     "key = 5",
			open:     "",
			wantOpen: "",
		},
		{
			name:     "basic string",
			line:     "key = \"value\"",
			open:     "",
			wantOpen: "",
		},
		{
			name:     "multiline string starts",
			line:     "key = \"\"\"value",
			open:     "",
			wantOpen: "\"\"\"",
		},
		{
			name:     "multiline string ends",
			line:     "continuing text\"\"\"",
			open:     "\"\"\"",
			wantOpen: "",
		},
		{
			name:     "single quotes",
			line:     "key = '''value",
			open:     "",
			wantOpen: "'''",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tomlStringState(tt.line, tt.open)
			if got != tt.wantOpen {
				t.Errorf("tomlStringState(%q, %q) = %q, want %q", tt.line, tt.open, got, tt.wantOpen)
			}
		})
	}
}

func TestSectionName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantName string
	}{
		{
			name:     "simple",
			input:    "Simple",
			wantName: "Simple",
		},
		{
			name:     "with leading spaces",
			input:    "   Leading",
			wantName: "Leading",
		},
		{
			name:     "with trailing spaces",
			input:    "Trailing   ",
			wantName: "Trailing",
		},

		{
			name:     "multiple spaces collapsed",
			input:    "Multiple   Spaces   Here",
			wantName: "Multiple Spaces Here",
		},
		{
			name:     "slash replaced",
			input:    "Parent/Child",
			wantName: "Parent-Child",
		},
		{
			name:     "long name truncated",
			input:    strings.Repeat("A", 100),
			wantName: strings.Repeat("A", 80),
		},
		{
			name:     "empty after cleaning",
			input:    "   ",
			wantName: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sectionName(tt.input)
			if got != tt.wantName {
				t.Errorf("sectionName(%q) = %q, want %q", tt.input, got, tt.wantName)
			}
		})
	}
}

func TestLineStart(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		offset   int
		wantLine int
	}{
		{
			name:     "at start",
			content:  "First line\nSecond line",
			offset:   0,
			wantLine: 0,
		},
		{
			name:     "in middle of line",
			content:  "First line\nSecond line",
			offset:   5,
			wantLine: 0,
		},
		{
			name:     "at newline",
			content:  "First line\nSecond line",
			offset:   11,
			wantLine: 11,
		},
		{
			name:     "in second line",
			content:  "First line\nSecond line",
			offset:   15,
			wantLine: 11,
		},
		{
			name:     "offset beyond content",
			content:  "Short",
			offset:   100,
			wantLine: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lineStart([]byte(tt.content), tt.offset)
			if got != tt.wantLine {
				t.Errorf("lineStart(%q, %d) = %d, want %d", tt.content, tt.offset, got, tt.wantLine)
			}
		})
	}
}

func TestMarkdownComplexNesting(t *testing.T) {
	content := []byte("# Main\n## Section A\nContent A\n### Subsection A1\nContent A1\n### Subsection A2\nContent A2\n## Section B\nContent B")
	sections := markdownSections(content)
	if len(sections) != 5 {
		t.Fatalf("expected 5 sections, got %d", len(sections))
	}
	// Main is a document title (above all others), so children don't include it in their paths
	expected := []string{
		"Main",
		"Section A",
		"Section A/Subsection A1",
		"Section A/Subsection A2",
		"Section B",
	}
	for i, exp := range expected {
		if i >= len(sections) {
			break
		}
		if sections[i].Name != exp {
			t.Errorf("section %d: expected %q, got %q", i, exp, sections[i].Name)
		}
	}
}

func TestTomlComplexNesting(t *testing.T) {
	content := []byte("[tool]\nversion = \"1.0\"\n[tool.python]\nversion = \"3.9\"\n[tool.python.settings]\ndebug = true\n[build]\nscript = \"build.py\"\n")
	sections := tomlSections(content)
	if len(sections) < 3 {
		t.Fatalf("expected at least 3 sections, got %d", len(sections))
	}
	if sections[0].Name != "tool" {
		t.Errorf("section 0: expected 'tool', got %q", sections[0].Name)
	}
}

func TestMarkdownSectionsByteBoundaries(t *testing.T) {
	content := []byte("# First\n123456\n# Second\nABCDEF")
	sections := markdownSections(content)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}
	for i, section := range sections {
		if section.ByteStart < 0 || section.ByteEnd > len(content) || section.ByteStart > section.ByteEnd {
			t.Errorf("section %d has invalid byte range: %d-%d (content length %d)", i, section.ByteStart, section.ByteEnd, len(content))
		}
	}
}

func TestTomlSectionsByteBoundaries(t *testing.T) {
	content := []byte("[first]\nkey = 1\n[second]\nkey = 2")
	sections := tomlSections(content)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}
	for i, section := range sections {
		if section.ByteStart < 0 || section.ByteEnd > len(content) || section.ByteStart > section.ByteEnd {
			t.Errorf("section %d has invalid byte range: %d-%d (content length %d)", i, section.ByteStart, section.ByteEnd, len(content))
		}
	}
}
