package main

import "testing"

func TestProbeRejectsIntermediateFalseResolutionAndReannouncement(t *testing.T) {
	item := map[string]any{"id": "error", "path": "main.go", "severity": float64(1), "line": float64(2)}
	reply := func(kind string) map[string]any {
		update := map[string]any{}
		for k, v := range item {
			update[k] = v
		}
		update["kind"] = kind
		return map[string]any{"diagnostic_updates": []any{update}}
	}
	seen := map[string]bool{}
	if err := probeIntermediateTransitions(reply("new"), "main.go", "A", 2, seen); err != nil {
		t.Fatal(err)
	}
	if err := probeIntermediateTransitions(reply("resolved"), "main.go", "B", 2, seen); err == nil {
		t.Fatal("intermediate false resolution accepted")
	}
	if err := probeIntermediateTransitions(reply("new"), "main.go", "B", 2, seen); err == nil {
		t.Fatal("intermediate re-announcement accepted")
	}
	if err := probeIntermediateTransitions(reply("resolved"), "main.go", "C", 2, seen); err != nil {
		t.Fatal(err)
	}
	if err := probeIntermediateTransitions(reply("new"), "main.go", "D", 2, map[string]bool{}); err == nil {
		t.Fatal("first touch misclassified as new")
	}
	item["baseline_missing"] = true
	if err := probeIntermediateTransitions(reply("new"), "main.go", "D", 2, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
}
