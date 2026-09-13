package workspace

import (
	"strings"
	"testing"
)

func surfaceOf(t *testing.T, path, content string) APISurface {
	t.Helper()
	surface := ReadAPISurface(path, []byte(content), true)
	if !surface.Covered {
		t.Fatalf("%s was not covered: %s", path, surface.Reason)
	}
	return surface
}

func changeKinds(changes []APIChange) map[string]string {
	kinds := map[string]string{}
	for _, change := range changes {
		kinds[change.Name] = change.Kind
	}
	return kinds
}

// Removing an exported declaration breaks code this repository does not
// contain, which is the whole reason to look.
func TestRemovingAnExportedGoDeclarationIsBreaking(t *testing.T) {
	before := surfaceOf(t, "ledger.go", "package p\n\nfunc Total() int { return 7 }\n\nfunc Unused() int { return 0 }\n")
	after := surfaceOf(t, "ledger.go", "package p\n\nfunc Total() int { return 7 }\n")
	changes := CompareAPISurfaces(before, after)
	if kinds := changeKinds(changes); kinds["Unused"] != "removed" {
		t.Fatalf("changes = %#v", changes)
	}
	for _, change := range changes {
		if change.Name == "Unused" && !change.Breaking() {
			t.Fatal("a removed export was not breaking")
		}
	}
}

// A method belongs to its receiver, and an unexported receiver is not part of
// the surface at all.
func TestGoMethodsBelongToTheirReceiver(t *testing.T) {
	surface := surfaceOf(t, "store.go", "package p\n\ntype Ledger struct{}\n\nfunc (Ledger) Balance() int { return 1 }\n\ntype hidden struct{}\n\nfunc (hidden) Balance() int { return 2 }\n")
	if _, found := surface.Symbols["Ledger.Balance"]; !found {
		t.Fatalf("symbols = %#v", surface.Symbols)
	}
	if _, found := surface.Symbols["hidden.Balance"]; found {
		t.Fatal("a method on an unexported type reached the surface")
	}
}

// A signature change and an added interface method are both work for somebody
// else, and both are reported.
func TestGoSignatureAndInterfaceChangesAreReported(t *testing.T) {
	before := surfaceOf(t, "store.go", "package p\n\ntype Store interface{ Balance() int }\n\nfunc Sum(store Store) int { return store.Balance() }\n")
	after := surfaceOf(t, "store.go", "package p\n\ntype Store interface {\n\tBalance() int\n\tCurrency() string\n}\n\nfunc Sum(store Store) (int, error) { return store.Balance(), nil }\n")
	kinds := changeKinds(CompareAPISurfaces(before, after))
	if kinds["Sum"] != "signature_changed" || kinds["Store"] != "members_changed" {
		t.Fatalf("kinds = %#v", kinds)
	}
}

// Adding a declaration is not a break, and neither is reformatting one.
func TestAddedAndReformattedGoDeclarationsAreNotBreaking(t *testing.T) {
	before := surfaceOf(t, "ledger.go", "package p\n\nfunc Total() int { return 7 }\n")
	after := surfaceOf(t, "ledger.go", "package p\n\nfunc Total() int {\n\treturn 7\n}\n\nfunc Extra() int { return 1 }\n")
	for _, change := range CompareAPISurfaces(before, after) {
		if change.Breaking() {
			t.Fatalf("unexpected breaking change: %#v", change)
		}
	}
}

// A barrel is a promise about where a name comes from, so moving one behind it
// is a change even when the name does not move.
func TestTypeScriptBarrelChangesAreReported(t *testing.T) {
	before := surfaceOf(t, "index.ts", "export { total, unused } from \"./ledger.js\";\nexport type { Store } from \"./store.js\";\n")
	after := surfaceOf(t, "index.ts", "export { total } from \"./ledger.js\";\nexport type { Store } from \"./types.js\";\n")
	kinds := changeKinds(CompareAPISurfaces(before, after))
	if kinds["unused"] != "removed" || kinds["Store"] != "signature_changed" {
		t.Fatalf("kinds = %#v", kinds)
	}
}

func TestTypeScriptInterfaceMembersAreRead(t *testing.T) {
	before := surfaceOf(t, "store.ts", "export interface Store {\n  balance(): number;\n}\n\nexport function sum(store: Store): number {\n  return store.balance();\n}\n")
	after := surfaceOf(t, "store.ts", "export interface Store {\n  balance(): number;\n  currency(): string;\n}\n\nexport function sum(store: Store): number {\n  return store.balance();\n}\n")
	kinds := changeKinds(CompareAPISurfaces(before, after))
	if kinds["Store"] != "members_changed" {
		t.Fatalf("kinds = %#v", kinds)
	}
	if len(kinds) != 1 {
		t.Fatalf("an unchanged function was reported: %#v", kinds)
	}
}

// The scanner says so when it meets an export it does not understand, because
// reporting the names it did read as the whole surface would turn everything
// it missed into a removal - or a removal into a clean bill of health.
func TestAnUnreadableExportFormMakesTheFileUncovered(t *testing.T) {
	surface := ReadAPISurface("odd.ts", []byte("export function total(): number { return 7; }\nexport = someLegacyThing;\n"), true)
	if surface.Covered {
		t.Fatal("an export form the adapter does not read was treated as understood")
	}
	if !strings.Contains(surface.Reason, "export form") {
		t.Fatalf("reason = %q", surface.Reason)
	}
	if len(surface.Symbols) != 0 {
		t.Fatalf("a half-read surface was returned: %#v", surface.Symbols)
	}
}

func TestAFileThatDoesNotParseIsNotCovered(t *testing.T) {
	surface := ReadAPISurface("broken.go", []byte("package p\n\nfunc Total() int { return\n"), true)
	if surface.Covered || !strings.Contains(surface.Reason, "does not parse") {
		t.Fatalf("surface = %#v", surface)
	}
}

// A language nobody wrote an adapter for is a gap that says its own name.
func TestAnUnknownLanguageIsNotCovered(t *testing.T) {
	surface := ReadAPISurface("config.yaml", []byte("name: value\n"), true)
	if surface.Covered || !strings.Contains(surface.Reason, "no API adapter") {
		t.Fatalf("surface = %#v", surface)
	}
}

// A file the plan deletes has no surface, and that is knowledge rather than a
// gap: everything it exported is gone.
func TestADeletedFileRemovesEverythingItExported(t *testing.T) {
	before := surfaceOf(t, "ledger.go", "package p\n\nfunc Total() int { return 7 }\n")
	after := ReadAPISurface("ledger.go", nil, false)
	if !after.Covered {
		t.Fatalf("a deleted Go file was reported as unreadable: %s", after.Reason)
	}
	if kinds := changeKinds(CompareAPISurfaces(before, after)); kinds["Total"] != "removed" {
		t.Fatalf("kinds = %#v", kinds)
	}
}
