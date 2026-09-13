//go:build live

package livetest

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestExecutionSelectedFlowMatrix(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	for _, language := range []string{"go", "typescript", "python", "lua"} {
		t.Run(language, func(t *testing.T) {
			root := fixture(t, filepath.Join("execution", language))
			before := hashTree(t, root)
			initial := call(t, session, "execution_graph", map[string]any{"root": root})
			base := data(initial)["snapshot"].(map[string]any)
			graph := base["execution"].(map[string]any)
			selected := ""
			name := "run"
			if language == "go" {
				name = "guarded"
			}
			for _, raw := range graph["nodes"].([]any) {
				node := raw.(map[string]any)
				if node["kind"] == "symbol" && node["name"] == name {
					selected = node["id"].(string)
					break
				}
			}
			if selected == "" {
				t.Fatalf("missing %s: %v", name, graph)
			}
			result := call(t, session, "execution_graph", map[string]any{"root": root, "expand_functions": []string{selected}})
			if outcome(result) != "partial" {
				t.Fatalf("%v", result)
			}
			expanded := data(result)["snapshot"].(map[string]any)
			if expanded["id"] == base["id"] {
				t.Fatal("selection absent from snapshot identity")
			}
			flow := expanded["execution"].(map[string]any)
			for _, gap := range flow["coverage"].(map[string]any)["gaps"].([]any) {
				if gap == "node_conflict" {
					t.Fatal("flow node identity conflict")
				}
			}
			count, conditions, handles := 0, 0, 0
			for _, raw := range flow["nodes"].([]any) {
				node := raw.(map[string]any)
				owner, _ := node["owner"].(string)
				if owner == "" {
					continue
				}
				if owner != selected {
					t.Fatal("unselected function expanded")
				}
				count++
				if node["condition"] != nil {
					conditions++
				}
				for _, rawFact := range node["evidence"].([]any) {
					fact := rawFact.(map[string]any)
					refs, _ := fact["source_handles"].([]any)
					handles += len(refs)
				}
			}
			t.Logf("%s selected=%s nodes=%d conditions=%d coverage=%v", language, selected, count, conditions, flow["coverage"])
			if count == 0 || conditions == 0 || handles == 0 {
				t.Fatal("selected flow lacks condition/source evidence")
			}
			if !reflect.DeepEqual(before, hashTree(t, root)) {
				t.Fatal("flow changed canonical fixture")
			}
		})
	}
}
