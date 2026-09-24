package mcpapi

import (
	"reflect"
	"testing"
)

// A receipt replayed from disk carries its warnings as []any. Adding an
// alias warning to it used to read only []string and so dropped them.
func TestWarningsSurviveBeingAddedToAReplayedEnvelope(t *testing.T) {
	replayed := map[string]any{"warnings": []any{"from the receipt"}}
	PrependWarnings(replayed, "alias rewritten")
	if got := Warnings(replayed); !reflect.DeepEqual(got, []string{"alias rewritten", "from the receipt"}) {
		t.Fatalf("prepended warnings = %#v", got)
	}
	built := map[string]any{"warnings": []string{"first"}}
	AddWarnings(built, "", "second")
	if got := Warnings(built); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("appended warnings = %#v", got)
	}
	untouched := map[string]any{"warnings": []any{"kept"}}
	AddWarnings(untouched, "")
	if _, same := untouched["warnings"].([]any); !same {
		t.Fatalf("adding no warning rewrote the list: %#v", untouched)
	}
}
