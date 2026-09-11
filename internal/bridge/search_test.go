package bridge

import "testing"

// A single hit is summarised in the singular, and an invalid regular
// expression names its code and offers a literal retry.
func TestSearchSummarisesSingleHitAndRecoversInvalidRegex(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "only once\n"})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()

	singular := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "once", "mode": "literal",
		"include_ranges": true,
	})
	if singular["summary"] != "1 match" {
		t.Fatalf("singular search summary = %#v", singular)
	}
	invalid := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "(", "mode": "regex",
	})
	if invalid["code"] != "invalid_regex" || len(invalid["next"].([]any)) == 0 {
		t.Fatalf("invalid regex has no recovery: %#v", invalid)
	}
}
