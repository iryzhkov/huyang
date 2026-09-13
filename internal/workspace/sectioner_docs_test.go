package workspace

import (
	"slices"
	"testing"
)

func TestMarkdownSections(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantSecs []Section
	}{
		{
			name:     "empty document",
			content:  "",
			wantSecs: []Section{},
		},
		{
			name:    "single heading",
			content: "# Title\n",
			wantSecs: []Section{
				{Name: "Title", Kind: "heading", ByteStart: 0, ByteEnd: 8},
			},
		},
		{
			name:    "single heading no trailing newline",
			content: "# Title",
			wantSecs: []Section{
				{Name: "Title", Kind: "heading", ByteStart: 0, ByteEnd: 7},
			},
		},
		{
			name:    "nested headings",
			content: "# H1\n## H2\n### H3\n## H2b\n# H1b\n",
			wantSecs: []Section{
				{Name: "H1", Kind: "heading", ByteStart: 0, ByteEnd: 25},
				{Name: "H1/H2", Kind: "heading", ByteStart: 5, ByteEnd: 18},
				{Name: "H1/H2/H3", Kind: "heading", ByteStart: 11, ByteEnd: 18},
				{Name: "H1/H2b", Kind: "heading", ByteStart: 18, ByteEnd: 25},
				{Name: "H1b", Kind: "heading", ByteStart: 25, ByteEnd: 31},
			},
		},
		{
			name:    "document title (level 1, all deeper)",
			content: "# Document Title\n## Section One\n### Subsection\n## Section Two\n",
			wantSecs: []Section{
				{Name: "Document Title", Kind: "heading", ByteStart: 0, ByteEnd: 17},
				{Name: "Section One", Kind: "heading", ByteStart: 17, ByteEnd: 37},
				{Name: "Section One/Subsection", Kind: "heading", ByteStart: 37, ByteEnd: 56},
				{Name: "Section Two", Kind: "heading", ByteStart: 56, ByteEnd: 73},
			},
		},
		{
			name:    "heading with trailing hash",
			content: "# Title #\n## Subtitle ##\n",
			wantSecs: []Section{
				{Name: "Title", Kind: "heading", ByteStart: 0, ByteEnd: 10},
				{Name: "Title/Subtitle", Kind: "heading", ByteStart: 10, ByteEnd: 25},
			},
		},
		{
			name:    "setext heading level 1",
			content: "Title\n=====\n## Subsection\n",
			wantSecs: []Section{
				{Name: "Title", Kind: "heading", ByteStart: 0, ByteEnd: 7},
				{Name: "Title/Subsection", Kind: "heading", ByteStart: 7, ByteEnd: 24},
			},
		},
		{
			name:    "setext heading level 2",
			content: "# Title\nSubtitle\n--------\n",
			wantSecs: []Section{
				{Name: "Title", Kind: "heading", ByteStart: 0, ByteEnd: 8},
				{Name: "Title/Subtitle", Kind: "heading", ByteStart: 8, ByteEnd: 25},
			},
		},
		{
			name:    "fenced code block with heading-like content",
			content: "# Real Heading\n```\n# Fake Heading\n```\n",
			wantSecs: []Section{
				{Name: "Real Heading", Kind: "heading", ByteStart: 0, ByteEnd: 39},
			},
		},
		{
			name:    "indented code block not treated as heading",
			content: "# Real Heading\n\n    # Not a Heading\n",
			wantSecs: []Section{
				{Name: "Real Heading", Kind: "heading", ByteStart: 0, ByteEnd: 36},
			},
		},
		{
			name:    "empty heading (no text)",
			content: "# \n## Content\n",
			wantSecs: []Section{
				{Name: "Content", Kind: "heading", ByteStart: 4, ByteEnd: 14},
			},
		},
		{
			name:    "heading with only whitespace",
			content: "#   \n## Section\n",
			wantSecs: []Section{
				{Name: "Section", Kind: "heading", ByteStart: 5, ByteEnd: 15},
			},
		},
		{
			name:    "long heading name truncated at 80 bytes",
			content: "# " + string(make([]byte, 100)) + "\n## Short\n",
			wantSecs: []Section{
				{Name: string(make([]byte, 79)), Kind: "heading", ByteStart: 0, ByteEnd: 103},
				{Name: string(make([]byte, 79)) + "/Short", Kind: "heading", ByteStart: 103, ByteEnd: 110},
			},
		},
		{
			name:    "heading with slash converted to dash",
			content: "# Parent/Child\n## Nested/Path\n",
			wantSecs: []Section{
				{Name: "Parent-Child", Kind: "heading", ByteStart: 0, ByteEnd: 15},
				{Name: "Parent-Child/Nested-Path", Kind: "heading", ByteStart: 15, ByteEnd: 31},
			},
		},
		{
			name:    "consecutive same-level headings",
			content: "# First\n# Second\n# Third\n",
			wantSecs: []Section{
				{Name: "First", Kind: "heading", ByteStart: 0, ByteEnd: 8},
				{Name: "Second", Kind: "heading", ByteStart: 8, ByteEnd: 17},
				{Name: "Third", Kind: "heading", ByteStart: 17, ByteEnd: 25},
			},
		},
		{
			name:    "heading levels jump (h1 to h3)",
			content: "# H1\n### H3\n## H2\n",
			wantSecs: []Section{
				{Name: "H1", Kind: "heading", ByteStart: 0, ByteEnd: 5},
				{Name: "H1/H3", Kind: "heading", ByteStart: 5, ByteEnd: 12},
				{Name: "H2", Kind: "heading", ByteStart: 12, ByteEnd: 18},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markdownSections([]byte(tt.content))
			if !slices.EqualFunc(got, tt.wantSecs, sectionsEqual) {
				t.Errorf("markdownSections() = %v, want %v", got, tt.wantSecs)
			}
		})
	}
}

func TestTomlSections(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantSecs []Section
	}{
		{
			name:     "empty document",
			content:  "",
			wantSecs: []Section{},
		},
		{
			name:    "single table",
			content: "[tool]\nkey = \"value\"\n",
			wantSecs: []Section{
				{Name: "tool", Kind: "table", ByteStart: 0, ByteEnd: 21},
			},
		},
		{
			name:    "single table no trailing newline",
			content: "[tool]\nkey = \"value\"",
			wantSecs: []Section{
				{Name: "tool", Kind: "table", ByteStart: 0, ByteEnd: 20},
			},
		},
		{
			name:    "multiple tables",
			content: "[section1]\nkey1 = 1\n[section2]\nkey2 = 2\n",
			wantSecs: []Section{
				{Name: "section1", Kind: "table", ByteStart: 0, ByteEnd: 20},
				{Name: "section2", Kind: "table", ByteStart: 20, ByteEnd: 40},
			},
		},
		{
			name:    "nested tables with dotted keys",
			content: "[tool.ruff]\nline-length = 80\n[tool.pytest]\n",
			wantSecs: []Section{
				{Name: "tool/ruff", Kind: "table", ByteStart: 0, ByteEnd: 30},
				{Name: "tool/pytest", Kind: "table", ByteStart: 30, ByteEnd: 43},
			},
		},
		{
			name:    "table with quoted key",
			content: "[\"quoted.key\"]\nvalue = 1\n[other]\n",
			wantSecs: []Section{
				{Name: "quoted.key", Kind: "table", ByteStart: 0, ByteEnd: 24},
				{Name: "other", Kind: "table", ByteStart: 24, ByteEnd: 36},
			},
		},
		{
			name:    "array of tables",
			content: "[[products]]\nname = \"A\"\n[[products]]\nname = \"B\"\n",
			wantSecs: []Section{
				{Name: "products", Kind: "table_array", ByteStart: 0, ByteEnd: 23},
				{Name: "products", Kind: "table_array", ByteStart: 23, ByteEnd: 47},
			},
		},
		{
			name:    "nested array of tables",
			content: "[[tool.pytest.plugins]]\nname = \"x\"\n",
			wantSecs: []Section{
				{Name: "tool/pytest/plugins", Kind: "table_array", ByteStart: 0, ByteEnd: 36},
			},
		},
		{
			name:    "table header with comment",
			content: "[tool] # comment\nkey = 1\n",
			wantSecs: []Section{
				{Name: "tool", Kind: "table", ByteStart: 0, ByteEnd: 26},
			},
		},
		{
			name:    "multi-line basic string containing brackets",
			content: "[section]\nkey = \"\"\"contains [brackets]\n\"\"\"\n[next]\n",
			wantSecs: []Section{
				{Name: "section", Kind: "table", ByteStart: 0, ByteEnd: 45},
				{Name: "next", Kind: "table", ByteStart: 45, ByteEnd: 53},
			},
		},
		{
			name:    "multi-line literal string containing brackets",
			content: "[section]\nkey = '''contains [brackets]\n'''\n[next]\n",
			wantSecs: []Section{
				{Name: "section", Kind: "table", ByteStart: 0, ByteEnd: 45},
				{Name: "next", Kind: "table", ByteStart: 45, ByteEnd: 53},
			},
		},
		{
			name:    "inline table",
			content: "[section]\ndata = {key = \"value\"}\n[next]\n",
			wantSecs: []Section{
				{Name: "section", Kind: "table", ByteStart: 0, ByteEnd: 33},
				{Name: "next", Kind: "table", ByteStart: 33, ByteEnd: 41},
			},
		},
		{
			name:     "content without table headers",
			content:  "key = \"value\"\nfoo = \"bar\"\n",
			wantSecs: []Section{},
		},
		{
			name:    "invalid table header - unclosed bracket",
			content: "[section\nkey = 1\n[valid]\n",
			wantSecs: []Section{
				{Name: "valid", Kind: "table", ByteStart: 20, ByteEnd: 28},
			},
		},
		{
			name:    "mixed single and multi-line strings",
			content: "[a]\nkey = \"single\"\n[b]\ndata = \"\"\"multi\nline\"\"\"\n[c]\n",
			wantSecs: []Section{
				{Name: "a", Kind: "table", ByteStart: 0, ByteEnd: 20},
				{Name: "b", Kind: "table", ByteStart: 20, ByteEnd: 51},
				{Name: "c", Kind: "table", ByteStart: 51, ByteEnd: 55},
			},
		},
		{
			name:    "array of tables with nested table",
			content: "[[products]]\nname = \"A\"\n[products.variant]\nsize = \"M\"\n",
			wantSecs: []Section{
				{Name: "products", Kind: "table_array", ByteStart: 0, ByteEnd: 23},
				{Name: "products/variant", Kind: "table", ByteStart: 23, ByteEnd: 55},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tomlSections([]byte(tt.content))
			if !slices.EqualFunc(got, tt.wantSecs, sectionsEqual) {
				t.Errorf("tomlSections() = %v, want %v", got, tt.wantSecs)
			}
		})
	}
}

func TestHasDocumentTitle(t *testing.T) {
	tests := []struct {
		name     string
		headings []documentHeading
		want     bool
	}{
		{
			name:     "empty headings",
			headings: []documentHeading{},
			want:     false,
		},
		{
			name: "single heading",
			headings: []documentHeading{
				{level: 1, name: "Title"},
			},
			want: false,
		},
		{
			name: "title at level 1, subsections deeper",
			headings: []documentHeading{
				{level: 1, name: "Title"},
				{level: 2, name: "Section"},
				{level: 3, name: "Subsection"},
			},
			want: true,
		},
		{
			name: "first heading not a title - same level follows",
			headings: []documentHeading{
				{level: 1, name: "First"},
				{level: 1, name: "Second"},
			},
			want: false,
		},
		{
			name: "first heading not a title - shallower level follows",
			headings: []documentHeading{
				{level: 2, name: "First"},
				{level: 1, name: "Higher"},
			},
			want: false,
		},
		{
			name: "title with mixed depths but all deeper",
			headings: []documentHeading{
				{level: 1, name: "Title"},
				{level: 2, name: "A"},
				{level: 2, name: "B"},
				{level: 3, name: "B.1"},
			},
			want: true,
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
		line     string
		wantName string
		wantKind string
		wantOk   bool
	}{
		{
			line:     "[tool]",
			wantName: "tool",
			wantKind: "table",
			wantOk:   true,
		},
		{
			line:     "[tool.ruff]",
			wantName: "tool/ruff",
			wantKind: "table",
			wantOk:   true,
		},
		{
			line:     "[[products]]",
			wantName: "products",
			wantKind: "table_array",
			wantOk:   true,
		},
		{
			line:     "[[tool.pytest.plugins]]",
			wantName: "tool/pytest/plugins",
			wantKind: "table_array",
			wantOk:   true,
		},
		{
			line:     "[section] # comment",
			wantName: "section",
			wantKind: "table",
			wantOk:   true,
		},
		{
			line:     "  [indented]  ",
			wantName: "indented",
			wantKind: "table",
			wantOk:   true,
		},
		{
			line:     "key = \"value\"",
			wantName: "",
			wantKind: "",
			wantOk:   false,
		},
		{
			line:     "[unclosed",
			wantName: "",
			wantKind: "",
			wantOk:   false,
		},
		{
			line:     "[section] extra text",
			wantName: "",
			wantKind: "",
			wantOk:   false,
		},
		{
			line:     "[\"quoted.key\"]",
			wantName: "quoted.key",
			wantKind: "table",
			wantOk:   true,
		},
		{
			line:     "[a.\"b.c\".d]",
			wantName: "a/b.c/d",
			wantKind: "table",
			wantOk:   true,
		},
		{
			line:     "[]",
			wantName: "",
			wantKind: "",
			wantOk:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			gotName, gotKind, gotOk := tomlTableHeader(tt.line)
			if gotName != tt.wantName || gotKind != tt.wantKind || gotOk != tt.wantOk {
				t.Errorf("tomlTableHeader(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.line, gotName, gotKind, gotOk, tt.wantName, tt.wantKind, tt.wantOk)
			}
		})
	}
}

func TestTomlKeyPath(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{
			header: "tool",
			want:   "tool",
		},
		{
			header: "tool.ruff",
			want:   "tool/ruff",
		},
		{
			header: "tool.pytest.ini_options",
			want:   "tool/pytest/ini_options",
		},
		{
			header: "\"quoted.key\"",
			want:   "quoted.key",
		},
		{
			header: "a.\"b.c\".d",
			want:   "a/b.c/d",
		},
		{
			header: "a.'single.quoted'.b",
			want:   "a/single.quoted/b",
		},
		{
			header: "",
			want:   "",
		},
		{
			header: "   ",
			want:   "",
		},
		{
			header: "a..b",
			want:   "",
		},
		{
			header: "\"\"",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			got := tomlKeyPath(tt.header)
			if got != tt.want {
				t.Errorf("tomlKeyPath(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestTomlStringState(t *testing.T) {
	tests := []struct {
		line string
		open string
		want string
	}{
		{
			line: "key = \"value\"",
			open: "",
			want: "",
		},
		{
			line: "key = \"\"\"multiline",
			open: "",
			want: "\"\"\"",
		},
		{
			line: "continuation\"\"\"",
			open: "\"\"\"",
			want: "",
		},
		{
			line: "key = '''multiline",
			open: "",
			want: "'''",
		},
		{
			line: "continuation'''",
			open: "'''",
			want: "",
		},
		{
			line: "[ignored] because # comment starts",
			open: "",
			want: "",
		},
		{
			line: "still in \"\"\"string",
			open: "\"\"\"",
			want: "\"\"\"",
		},
		{
			line: "still in '''string",
			open: "'''",
			want: "'''",
		},
		{
			line: "\"\"\" # this closes even before comment",
			open: "\"\"\"",
			want: "",
		},
		{
			line: "",
			open: "\"\"\"",
			want: "\"\"\"",
		},
		{
			line: "normal string then \"\"\"open",
			open: "",
			want: "\"\"\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.line+"/"+tt.open, func(t *testing.T) {
			got := tomlStringState(tt.line, tt.open)
			if got != tt.want {
				t.Errorf("tomlStringState(%q, %q) = %q, want %q", tt.line, tt.open, got, tt.want)
			}
		})
	}
}

func TestSectionName(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{
			raw:  "Simple Title",
			want: "Simple Title",
		},
		{
			raw:  "  Trimmed  ",
			want: "Trimmed",
		},
		{
			raw:  "Multiple   Spaces",
			want: "Multiple Spaces",
		},
		{
			raw:  "Slash/Separator",
			want: "Slash-Separator",
		},
		{
			raw:  "Multiple/Slashes/Here",
			want: "Multiple-Slashes-Here",
		},
		{
			raw:  "Trailing Hash #",
			want: "Trailing Hash",
		},
		{
			raw:  "Trailing Spaces    #",
			want: "Trailing Spaces",
		},
		{
			raw:  "   ",
			want: "",
		},
		{
			raw:  "",
			want: "",
		},
		{
			raw:  string(make([]byte, 100)),
			want: string(make([]byte, 79)),
		},
		{
			raw:  "Short " + string(make([]byte, 80)),
			want: "Short " + string(make([]byte, 73)),
		},
		{
			raw:  "Title\t\tWith\t\tTabs",
			want: "Title With Tabs",
		},
		{
			raw:  "# Markdown Heading",
			want: "# Markdown Heading",
		},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := sectionName(tt.raw)
			if got != tt.want {
				t.Errorf("sectionName(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLineStart(t *testing.T) {
	tests := []struct {
		content string
		offset  int
		want    int
	}{
		{
			content: "first line\nsecond line",
			offset:  11,
			want:    11,
		},
		{
			content: "first line\nsecond line",
			offset:  15,
			want:    11,
		},
		{
			content: "first line\nsecond line",
			offset:  21,
			want:    11,
		},
		{
			content: "single line",
			offset:  5,
			want:    0,
		},
		{
			content: "line1\nline2\nline3",
			offset:  6,
			want:    6,
		},
		{
			content: "line1\nline2\nline3",
			offset:  8,
			want:    6,
		},
		{
			content: "a\nb\nc",
			offset:  0,
			want:    0,
		},
		{
			content: "a\nb\nc",
			offset:  2,
			want:    2,
		},
		{
			content: "line",
			offset:  100,
			want:    0,
		},
		{
			content: "line\nother",
			offset:  100,
			want:    5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.content+":"+string(rune(tt.offset)), func(t *testing.T) {
			got := lineStart([]byte(tt.content), tt.offset)
			if got != tt.want {
				t.Errorf("lineStart(%q, %d) = %d, want %d", tt.content, tt.offset, got, tt.want)
			}
		})
	}
}

func TestHeadingSections(t *testing.T) {
	tests := []struct {
		name         string
		headings     []documentHeading
		total        int
		wantSections []Section
	}{
		{
			name:         "empty headings",
			headings:     []documentHeading{},
			total:        100,
			wantSections: []Section{},
		},
		{
			name: "single heading",
			headings: []documentHeading{
				{level: 1, start: 0, name: "Title"},
			},
			total: 50,
			wantSections: []Section{
				{Name: "Title", Kind: "heading", ByteStart: 0, ByteEnd: 50},
			},
		},
		{
			name: "nested headings with correct ranges",
			headings: []documentHeading{
				{level: 1, start: 0, name: "H1"},
				{level: 2, start: 10, name: "H2"},
				{level: 3, start: 20, name: "H3"},
				{level: 2, start: 30, name: "H2b"},
				{level: 1, start: 40, name: "H1b"},
			},
			total: 50,
			wantSections: []Section{
				{Name: "H1", Kind: "heading", ByteStart: 0, ByteEnd: 10},
				{Name: "H1/H2", Kind: "heading", ByteStart: 10, ByteEnd: 20},
				{Name: "H1/H2/H3", Kind: "heading", ByteStart: 20, ByteEnd: 30},
				{Name: "H1/H2b", Kind: "heading", ByteStart: 30, ByteEnd: 40},
				{Name: "H1b", Kind: "heading", ByteStart: 40, ByteEnd: 50},
			},
		},
		{
			name: "document title without prefix for children",
			headings: []documentHeading{
				{level: 1, start: 0, name: "Title"},
				{level: 2, start: 10, name: "Section"},
				{level: 3, start: 20, name: "Subsection"},
			},
			total: 30,
			wantSections: []Section{
				{Name: "Title", Kind: "heading", ByteStart: 0, ByteEnd: 10},
				{Name: "Section", Kind: "heading", ByteStart: 10, ByteEnd: 20},
				{Name: "Section/Subsection", Kind: "heading", ByteStart: 20, ByteEnd: 30},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := headingSections(tt.headings, tt.total)
			if !slices.EqualFunc(got, tt.wantSections, sectionsEqual) {
				t.Errorf("headingSections() = %v, want %v", got, tt.wantSections)
			}
		})
	}
}

func sectionsEqual(a, b Section) bool {
	return a.Name == b.Name && a.Kind == b.Kind && a.ByteStart == b.ByteStart && a.ByteEnd == b.ByteEnd
}
