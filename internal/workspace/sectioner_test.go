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
	sections, err := NativeSectioner{}.Sections("notes.md", []byte("# heading\n"))
	if err != nil || sections != nil {
		t.Fatalf("markdown sections = %#v, %v", sections, err)
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
