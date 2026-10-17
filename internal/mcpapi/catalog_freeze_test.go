package mcpapi

// The frozen catalog. Every agent that has spoken to this server has its input
// schemas cached, so a change to them is a change to a contract: it has to be a
// deliberate act with a diff somebody read, not a side effect of rewording a
// description. This test fails on any difference and prints how to adopt it.
//
//	go test ./internal/mcpapi -run TestFrozenCatalog -update
//
// The fixtures are stored indented so the diff is reviewable; the wire bytes
// are what the budget gate measures.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var updateCatalogFixtures = flag.Bool("update", false, "rewrite all profile and contract fixtures from the current catalog")
var catalogFixtureDir = flag.String("catalog-fixture-dir", filepath.Join("..", "..", "docs", "plans", "fixtures", "huyang-v1alpha1"), "fixture directory; use an artifact directory for guarded import")

func catalogFixturePath(profile Profile) string {
	return filepath.Join(*catalogFixtureDir, fmt.Sprintf("catalog-%s.json", profile))
}

// indentedCatalog is the catalog as it goes on the wire, re-indented so a
// change to one description shows as one changed line.
func indentedCatalog(t *testing.T, profile Profile) []byte {
	t.Helper()
	wire, err := CatalogJSON(profile)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	indented, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(indented, '\n')
}

// frozenProfiles includes every frozen profile/alias and the experimental one. The
// experimental catalog is not a contract with anybody yet, which is the reason
// to freeze it rather than a reason not to: it is where the next contract is
// being written, and a change to it should be a diff somebody read.
func frozenProfiles() []Profile {
	return append(append([]Profile(nil), ProfileOrder...), ProfileExperimental)
}

func TestFrozenCatalogIsUnchanged(t *testing.T) {
	if *updateCatalogFixtures {
		if err := os.MkdirAll(*catalogFixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, profile := range frozenProfiles() {
		current := indentedCatalog(t, profile)
		path := catalogFixturePath(profile)
		if *updateCatalogFixtures {
			if err := os.WriteFile(path, current, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		frozen, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v; create it with: go test ./internal/mcpapi -run TestFrozenCatalog -update", path, err)
		}
		if !bytes.Equal(current, frozen) {
			t.Fatalf("the %s catalog no longer matches %s.\n"+
				"An input schema is a contract with every agent that cached it. If the change is\n"+
				"deliberate, adopt it and review the diff:\n"+
				"  go test ./internal/mcpapi -run TestFrozenCatalog -update && git diff %s",
				profile, path, path)
		}
	}
	checkContractFixture(t)
}

// A tool that is still being designed is advertised in the experimental
// catalog only, so the frozen profiles cannot acquire one by accident.
func TestExperimentalToolsStayOutOfTheFrozenProfiles(t *testing.T) {
	for _, profile := range ProfileOrder {
		for _, descriptor := range Catalog(profile) {
			if descriptor.Experimental {
				t.Fatalf("%s advertises the experimental tool %s; add it to the %s profile instead",
					profile, descriptor.Name, ProfileExperimental)
			}
		}
	}
	// The experimental profile is the whole frozen surface plus whatever is
	// being incubated, so an agent can opt into it and lose nothing.
	frozen := CatalogNames(ProfileFull)
	experimental := CatalogNames(ProfileExperimental)
	for _, name := range frozen {
		found := false
		for _, candidate := range experimental {
			found = found || candidate == name
		}
		if !found {
			t.Fatalf("the experimental catalog is missing the frozen tool %s", name)
		}
	}
	if APIVersionExperimental == APIVersion {
		t.Fatal("the experimental API version must differ from the frozen one")
	}
}

// Generate the contract from the same descriptors as tools/list. Historical
// hand-maintained tool shapes drifted after surface cuts; actual per-profile
// schemas are the contract, including lean's complete overrides.
func checkContractFixture(t *testing.T) {
	t.Helper()
	catalogs := map[string][]string{}
	schemas := map[string]map[string]any{}
	for _, profile := range frozenProfiles() {
		catalogs[string(profile)] = CatalogNames(profile)
		shapes := map[string]any{}
		for _, tool := range Catalog(profile) {
			shapes[tool.Name] = tool.InputSchema
		}
		schemas[string(profile)] = shapes
	}
	fixture := map[string]any{
		"fixture_version": "huyang.tools/v1alpha1-m7",
		"catalogs":        catalogs,
		"catalog_rules": map[string]any{
			"default": "full", "selection": "fixed startup argument or fixed HTTP route",
			"profile_is_permission": false, "workspace_health_changes_catalog": false,
			"aliases": map[string]string{"edit": "full (one release)"},
		},
		"input_schemas": schemas,
		"output_schema": OutputEnvelopeSchema(),
	}
	current, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	current = append(current, '\n')
	path := filepath.Join(*catalogFixtureDir, "contract-schema.json")
	if *updateCatalogFixtures {
		if err := os.WriteFile(path, current, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	frozen, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, frozen) {
		t.Fatalf("contract fixture is stale; regenerate all profiles with go test ./internal/mcpapi -run '^TestFrozenCatalogIsUnchanged$' -update")
	}
}
