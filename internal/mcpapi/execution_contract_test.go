package mcpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// normalizeExecutionSchema restores the Go types used by the catalog's
// validator after a schema has crossed a JSON boundary.
func normalizeExecutionSchema(schema map[string]any) {
	for key, value := range schema {
		switch child := value.(type) {
		case map[string]any:
			normalizeExecutionSchema(child)
		case float64:
			schema[key] = int(child)
		case []any:
			if key == "required" {
				names := make([]string, len(child))
				for i, name := range child {
					names[i] = name.(string)
				}
				schema[key] = names
			}
		}
	}
}

// The examples are client-visible evidence, not graph implementation tests.
// Rejecting an accidental sixth status keeps uncertainty from being folded
// into a more confident answer by a later adapter.
func TestExecutionEvidenceContract(t *testing.T) {
	base := filepath.Join("..", "..", "tests", "fixtures", "execution", "contracts")
	var schema map[string]any
	var examples []map[string]any
	for name, target := range map[string]any{"evidence.schema.json": &schema, "evidence.json": &examples} {
		content, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(content, target); err != nil {
			t.Fatal(err)
		}
	}
	normalizeExecutionSchema(schema)
	if len(examples) != 5 {
		t.Fatalf("want all five evidence statuses, got %d", len(examples))
	}
	for _, example := range examples {
		if err := ValidateToolArguments(schema, example); err != nil {
			t.Fatal(err)
		}
	}
	examples[0]["status"] = "never_called"
	if err := ValidateToolArguments(schema, examples[0]); err == nil {
		t.Fatal("invented certainty accepted by execution schema")
	}
	examples[0]["status"] = "unknown"
	delete(examples[0], "coverage")
	if err := ValidateToolArguments(schema, examples[0]); err == nil {
		t.Fatal("evidence without coverage accepted")
	}
}
