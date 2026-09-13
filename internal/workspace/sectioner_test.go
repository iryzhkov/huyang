package workspace

import (
	"strings"
	"testing"
)

func sectionNames(sections []Section) []string {
	names := make([]string, 0, len(sections))
	for _, section := range sections {
		names = append(names, section.Name+":"+section.Kind)
	}
	return names
}

// Go declarations are sectioned with their documentation comment, methods
// carry their receiver as the parent, and grouped declarations name every
// member.
func TestNativeSectionerGo(t *testing.T) {
	source := "package p\n\n// Max bounds things.\nconst Max = 1\n\nvar (\n\tA = 1\n\tB = 2\n)\n\n// Ledger holds entries.\ntype Ledger struct{}\n\n// Balance sums an account.\nfunc (l *Ledger) Balance() int { return 0 }\n\nfunc helper() {}\n"
	sections, err := NativeSectioner{}.Sections("ledger.go", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sectionNames(sections), ",")
	want := "Max:const_declaration,A:var_declaration,B:var_declaration,Ledger:type_declaration,Ledger/Balance:function_declaration,helper:function_declaration"
	if got != want {
		t.Fatalf("sections = %s", got)
	}
	balance := sections[4]
	if !strings.HasPrefix(source[balance.ByteStart:balance.ByteEnd], "// Balance sums") || !strings.HasSuffix(source[balance.ByteStart:balance.ByteEnd], "return 0 }") {
		t.Fatalf("Balance range = %q", source[balance.ByteStart:balance.ByteEnd])
	}
	if _, err := (NativeSectioner{}).Sections("broken.go", []byte("package p\nfunc {")); err == nil {
		t.Fatal("a Go parse failure was not reported")
	}
}

// Python def and class blocks end where the indentation returns, nested
// methods carry their class as parent, decorators belong to the
// declaration, and trailing blank lines are not part of a block.
func TestNativeSectionerPython(t *testing.T) {
	source := "import os\n\n\ndef top(a):\n    return a\n\n\nclass Ledger:\n    \"\"\"Doc.\"\"\"\n\n    @property\n    def balance(self):\n        # comment\n        return 0\n\n    def apply(self, e):\n        pass\n\n\nasync def later():\n    pass\n"
	sections, err := NativeSectioner{}.Sections("ledger.py", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sectionNames(sections), ",")
	want := "top:function_definition,Ledger:class_definition,Ledger/balance:function_definition,Ledger/apply:function_definition,later:function_definition"
	if got != want {
		t.Fatalf("sections = %s", got)
	}
	top := source[sections[0].ByteStart:sections[0].ByteEnd]
	if top != "def top(a):\n    return a\n" {
		t.Fatalf("top range = %q", top)
	}
	balance := source[sections[2].ByteStart:sections[2].ByteEnd]
	if !strings.HasPrefix(balance, "    @property\n    def balance") || !strings.HasSuffix(balance, "return 0\n") {
		t.Fatalf("balance range = %q", balance)
	}
	class := source[sections[1].ByteStart:sections[1].ByteEnd]
	if !strings.HasSuffix(class, "        pass\n") || strings.Contains(class, "async") {
		t.Fatalf("class range = %q", class)
	}
}

// Other languages have no native sections and no error, so the workspace
// reports them as declaring nothing rather than as parser failures.
func TestNativeSectionerIgnoresOtherLanguages(t *testing.T) {
	sections, err := NativeSectioner{}.Sections("notes.txt", []byte("a line\n"))
	if err != nil || sections != nil {
		t.Fatalf("text sections = %#v, %v", sections, err)
	}
	// Prose and configuration are sectioned, but they are not source: an
	// edit to them still expects no semantic verdict.
	for _, path := range []string{"notes.md", "pyproject.toml"} {
		if IsSemanticSource(path) {
			t.Fatalf("%s is treated as semantic source", path)
		}
	}
}

// A Markdown document is sectioned by heading, nested by level, so a long
// document can be outlined and read one part at a time.
func TestNativeSectionerSectionsMarkdownByHeading(t *testing.T) {
	source := "# Title\n\nintro\n\n## Rules of thumb\n\nfirst\n\n### Read / write\n\nnested\n\n## Out of scope\n\nlast\n"
	sections, err := NativeSectioner{}.Sections("plan.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Title", "Title/Rules of thumb", "Title/Rules of thumb/Read - write", "Title/Out of scope"}
	if len(sections) != len(want) {
		t.Fatalf("markdown sections = %#v", sections)
	}
	for index, name := range want {
		if sections[index].Name != name || sections[index].Kind != "heading" {
			t.Fatalf("section %d = %#v, want %q", index, sections[index], name)
		}
	}
	// A section starts at its own markers and runs to the next heading of
	// the same or a higher level, so it contains its subsections.
	rules := source[sections[1].ByteStart:sections[1].ByteEnd]
	if !strings.HasPrefix(rules, "## Rules of thumb\n") || !strings.HasSuffix(rules, "nested\n\n") || !strings.Contains(rules, "### Read") {
		t.Fatalf("rules range = %q", rules)
	}
	if last := source[sections[3].ByteStart:sections[3].ByteEnd]; !strings.HasSuffix(last, "last\n") {
		t.Fatalf("last range = %q", last)
	}
	// A heading inside a fenced code block is code, not a heading, which is
	// why this goes through the same parser the parser stage runs.
	fenced, err := NativeSectioner{}.Sections("fenced.md", []byte("# Real\n\n```sh\n# not a heading\n```\n"))
	if err != nil || len(fenced) != 1 || fenced[0].Name != "Real" {
		t.Fatalf("fenced sections = %#v, %v", fenced, err)
	}
}

// A TOML document is sectioned by table, each one ending where the next
// header begins, with the dotted name in the name-path shape.
func TestNativeSectionerSectionsTOMLByTable(t *testing.T) {
	source := "root = 1\n\n[tool.ruff]\nline-length = 100\n\n[[feed.entries]] # first\nname = \"a\"\n\n[notes]\nbody = \"\"\"\n[not.a.table]\n\"\"\"\n"
	sections, err := NativeSectioner{}.Sections("config.toml", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, kind string }{
		{"tool/ruff", "table"}, {"feed/entries", "table_array"}, {"notes", "table"},
	}
	if len(sections) != len(want) {
		t.Fatalf("toml sections = %#v", sections)
	}
	for index, expected := range want {
		if sections[index].Name != expected.name || sections[index].Kind != expected.kind {
			t.Fatalf("section %d = %#v, want %q %q", index, sections[index], expected.name, expected.kind)
		}
	}
	if ruff := source[sections[0].ByteStart:sections[0].ByteEnd]; ruff != "[tool.ruff]\nline-length = 100\n\n" {
		t.Fatalf("ruff range = %q", ruff)
	}
	// The bracketed line inside the multi-line string is not a header.
	if notes := source[sections[2].ByteStart:sections[2].ByteEnd]; !strings.Contains(notes, "[not.a.table]") {
		t.Fatalf("notes range = %q", notes)
	}
	// A quoted key keeps its dots, and a line that is not a header is left
	// alone.
	quoted, err := NativeSectioner{}.Sections("quoted.toml", []byte("[tool.\"my.tool\"]\nvalue = [1, 2]\n"))
	if err != nil || len(quoted) != 1 || quoted[0].Name != "tool/my.tool" {
		t.Fatalf("quoted sections = %#v, %v", quoted, err)
	}
}

// Symbol reads through the workspace resolve Go and Python declarations
// without a provider once the native sectioner is configured.
func TestFindSymbolsUsesNativeSectioner(t *testing.T) {
	workspace := literalWorkspace(t, map[string]string{
		"ledger.go": "package p\n\n// Balance sums.\nfunc Balance() int { return 0 }\n",
		"ledger.py": "def balance():\n    return 0\n",
	})
	workspace.sectioner = NativeSectioner{}
	records, coverage, err := workspace.FindSymbols("Balance")
	if err != nil || !coverage.Complete || len(records) != 1 || records[0].Locator.Path != "ledger.go" {
		t.Fatalf("Go symbol = %#v coverage=%#v err=%v", records, coverage, err)
	}
	records, _, err = workspace.FindSymbols("balance")
	if err != nil || len(records) != 1 || records[0].Locator.Path != "ledger.py" {
		t.Fatalf("Python symbol = %#v err=%v", records, err)
	}
}
