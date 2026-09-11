package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func openProbeProject(t *testing.T, files map[string]string) (*directWorkspaces, string, string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	direct := newDirectWorkspaces(t.TempDir())
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	t.Cleanup(cleanup)
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	return direct, workspaceID, root
}

func applyLiteralProbeEdit(t *testing.T, direct *directWorkspaces, workspaceID, query, replacement, key string) map[string]any {
	t.Helper()
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": query, "mode": "literal",
	})
	hits := searched["data"].(map[string]any)["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("search %q returned %d hits: %#v", query, len(hits), searched)
	}
	return callModern(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": key,
		"operation": map[string]any{
			"kind":    "replace_range",
			"target":  map[string]any{"file_range": hits[0].(map[string]any)["range"]},
			"content": replacement,
		},
	})
}

func TestWorkspaceInspectViewsExposeAndAdvanceCanonicalRevision(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"a.go": "package sample\nvar Before = 1\n",
	})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()

	for _, view := range []string{"status", "overview", "map"} {
		inspected := callModern(t, session, "workspace_inspect", map[string]any{
			"workspace_id": workspaceID, "view": view,
		})
		data := inspected["data"].(map[string]any)
		if data["view"] != view || data["revision"] != "wsrev_1" {
			t.Fatalf("%s inspection omitted its view/revision: %#v", view, inspected)
		}
		_, hasOverview := data["overview"]
		if view == "status" && hasOverview {
			t.Fatalf("status inspection unexpectedly returned repository overview: %#v", inspected)
		}
		if view != "status" && !hasOverview {
			t.Fatalf("%s inspection omitted repository orientation: %#v", view, inspected)
		}
	}

	applied := applyLiteralProbeEdit(t, direct, workspaceID, "Before", "After", "inspect-revision-edit")
	if applied["outcome"] != "provisional" {
		t.Fatalf("edit failed: %#v", applied)
	}
	if !strings.Contains(applied["summary"].(string), "diagnostics are unavailable") {
		t.Fatalf("edit misreported unavailable diagnostics: %#v", applied)
	}
	verification := applied["data"].(map[string]any)["verification"].(map[string]any)
	if verification["confidence"] != "unavailable" {
		t.Fatalf("edit invented diagnostic timeout evidence: %#v", applied)
	}
	inspected := callModern(t, session, "workspace_inspect", map[string]any{
		"workspace_id": workspaceID, "view": "status",
	})
	if revision := inspected["data"].(map[string]any)["revision"]; revision != "wsrev_2" {
		t.Fatalf("inspection revision did not advance after edit: %#v", inspected)
	}
}

func TestReadSymbolLocatorDoesNotSilentlyReturnWholeFileWithoutParser(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		"main.rb": "class Widget\n  def call\n    :ok\n  end\nend\n",
	})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	result := callModern(t, session, "read", map[string]any{
		"workspace_id": workspaceID,
		"target": map[string]any{"symbol_locator": map[string]any{
			"path": filepath.Join(root, "main.rb"), "name_path": "Widget#call",
		}},
		"view": "source",
	})
	if result["outcome"] != "unavailable" || result["code"] != "semantic_provider_unavailable" {
		t.Fatalf("text-only symbol read made an unsupported semantic claim: %#v", result)
	}
	data := result["data"].(map[string]any)
	if _, leaked := data["content"]; leaked {
		t.Fatalf("unresolved symbol locator silently returned file content: %#v", result)
	}
	next := result["next"].([]any)
	if len(next) == 0 || next[0].(map[string]any)["tool"] != "search" || next[0].(map[string]any)["query"] != "Widget#call" {
		t.Fatalf("symbol fallback is not actionable: %#v", result)
	}
}

func TestCanonicalAffectedVerificationUsesOnlyEditedFileAndReturnsQuickly(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"changed.go":   "package sample\nvar Changed = 1\n",
		"unchanged.go": "package sample\nvar Unchanged = 2\n",
	})
	applied := applyLiteralProbeEdit(t, direct, workspaceID, "Changed = 1", "Changed = 3", "affected-edit")
	revision := applied["data"].(map[string]any)["revision"].(string)
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	started := time.Now()
	verified := callModern(t, session, "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "affected-verify",
		"revision_or_transaction": revision,
		"stages":                  []string{"parser", "diagnostics"}, "test_scope": "affected",
	})
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("verification waited for an unavailable provider: %s (%#v)", elapsed, verified)
	}
	if verified["outcome"] != "partial" {
		t.Fatalf("parser plus unavailable diagnostics should be partial: %#v", verified)
	}
	stages := verified["data"].(map[string]any)["verification"].(map[string]any)["stages"].([]any)
	if len(stages) != 2 {
		t.Fatalf("verification stages = %#v", verified)
	}
	parser := stages[0].(map[string]any)
	scope := parser["scope"].([]any)
	if len(scope) != 1 || scope[0] != "changed.go" {
		t.Fatalf("affected parser scope includes unrelated files: %#v", verified)
	}
	if diagnostics := stages[1].(map[string]any); diagnostics["status"] != "unavailable" {
		t.Fatalf("unavailable diagnostics were not reported immediately: %#v", verified)
	}
}

func TestRevisionDiffCollapsesExactEditRevert(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"note.txt": "alpha beta\n",
	})
	applyLiteralProbeEdit(t, direct, workspaceID, "beta", "gamma", "revert-forward")
	applyLiteralProbeEdit(t, direct, workspaceID, "gamma", "beta", "revert-back")
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	diffed := callModern(t, session, "revision_diff", map[string]any{
		"workspace_id": workspaceID, "from_revision": "wsrev_1", "to_revision_or_current": "current",
	})
	if diffed["outcome"] != "ok" {
		t.Fatalf("revision diff failed: %#v", diffed)
	}
	diffs := diffed["data"].(map[string]any)["diffs"].([]any)
	if len(diffs) != 0 {
		t.Fatalf("exactly reverted path remains in endpoint diff: %#v", diffed)
	}
}

func TestCompactTextDataSummarizesOrientationWithoutDroppingResults(t *testing.T) {
	orientation := workspacecore.Orientation{Entries: []workspacecore.Entry{{Path: "a.go"}, {Path: "b.go"}}}
	data := compactTextData(map[string]any{"overview": orientation, "content": "kept"}).(map[string]any)
	if data["content"] != "kept" {
		t.Fatalf("substantive text data was dropped: %#v", data)
	}
	summary := data["overview"].(map[string]any)
	if summary["entry_count"] != 2 {
		t.Fatalf("orientation summary = %#v", summary)
	}
	if _, present := summary["entries"]; present {
		t.Fatalf("compact text duplicated orientation entries: %#v", summary)
	}
}

func TestStructuredResponsesBoundLargePlansAndWorkspaceMaps(t *testing.T) {
	files := make(map[string]string, maxStructuredEntries+25)
	for index := 0; index < maxStructuredEntries+25; index++ {
		files[fmt.Sprintf("pkg/file_%03d.go", index)] = "package pkg\n"
	}
	files["large.txt"] = "seed"
	direct, workspaceID, _ := openProbeProject(t, files)
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()

	inspected := callModern(t, session, "workspace_inspect", map[string]any{
		"workspace_id": workspaceID, "view": "map",
	})
	overview := inspected["data"].(map[string]any)["overview"].(map[string]any)
	if overview["entry_count"] != float64(len(files)) || overview["entries_truncated"] != true {
		t.Fatalf("bounded map metadata = %#v", overview)
	}
	if entries := overview["entries"].([]any); len(entries) != maxStructuredEntries {
		t.Fatalf("bounded map returned %d entries", len(entries))
	}

	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "seed", "mode": "literal",
	})
	hit := searched["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	large := strings.Repeat("x", 128<<10)
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "large-plan-create", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "large-replace", "kind": "replace_range",
			"target": map[string]any{"file_range": hit["range"]}, "content": large,
		}},
	})
	if created["outcome"] != "ok" {
		t.Fatalf("large plan create failed: %#v", created)
	}
	encoded, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 16<<10 || strings.Contains(string(encoded), large[:1024]) {
		t.Fatalf("large plan response was not bounded: %d bytes", len(encoded))
	}
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	operation := plan["operations"].([]any)[0].(map[string]any)
	if plan["plan_id"] == "" || operation["op_id"] != "large-replace" ||
		operation["content_bytes"] != float64(len(large)) || operation["content_sha256"] == "" {
		t.Fatalf("bounded plan omitted follow-up identifiers: %#v", plan)
	}
	previewed := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "large-plan-preview", "action": "preview",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"],
	})
	previewBytes, err := json.Marshal(previewed)
	if err != nil {
		t.Fatal(err)
	}
	if previewed["outcome"] != "ok" || len(previewBytes) > 16<<10 ||
		strings.Contains(string(previewBytes), large[:1024]) {
		t.Fatalf("large plan preview response was not bounded: %d bytes, %#v", len(previewBytes), previewed)
	}
	preview := previewed["data"].(map[string]any)["plan"].(map[string]any)["preview"].(map[string]any)
	if preview["preview_revision"] == "" || len(preview["diffs"].([]any)) != 1 {
		t.Fatalf("bounded preview omitted revision or diff identity: %#v", previewed)
	}
}

func TestRevisionDiffReturnsKnownSegmentsAfterExternalGap(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		"first.txt": "before\n", "second.txt": "alpha beta\n",
	})
	workspace := direct.get(workspacecore.ID(workspaceID))
	if _, err := workspace.Refresh(filepath.Join(root, "first.txt"), workspacecore.ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "first.txt"), []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Refresh(filepath.Join(root, "first.txt"), workspacecore.ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	inspected := callModern(t, session, "workspace_inspect", map[string]any{"workspace_id": workspaceID})
	if inspected["data"].(map[string]any)["revision"] != "wsrev_2" {
		t.Fatalf("external change was not observed: %#v", inspected)
	}
	applyLiteralProbeEdit(t, direct, workspaceID, "beta", "gamma", "post-gap-edit")
	diffed := callModern(t, session, "revision_diff", map[string]any{
		"workspace_id": workspaceID, "from_revision": "wsrev_1", "to_revision_or_current": "current",
	})
	if diffed["outcome"] != "partial" || diffed["code"] != "diff_evidence_incomplete" {
		t.Fatalf("gap diff outcome = %#v", diffed)
	}
	data := diffed["data"].(map[string]any)
	if len(data["gaps"].([]any)) != 1 || len(data["known_segments"].([]any)) != 1 || len(data["diffs"].([]any)) != 1 {
		t.Fatalf("known diff segments or explicit gaps missing: %#v", diffed)
	}
}

func TestInspectionExplainsUntrustedPipelineAndRecovery(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		".huyang.toml": "version = 1\n[[check]]\nname = \"check\"\ncommand = [\"go\", \"test\", \"./...\"]\n",
		"main.go":      "package sample\n",
	})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	inspected := callModern(t, session, "workspace_inspect", map[string]any{"workspace_id": workspaceID})
	state := inspected["data"].(map[string]any)["pipeline_state"].(map[string]any)
	if state["state"] != "configured_untrusted" || state["configured"] != true || state["trusted"] != false {
		t.Fatalf("pipeline state is ambiguous: %#v", inspected)
	}
	next := inspected["next"].([]any)
	if len(next) != 1 || next[0].(map[string]any)["root"] != root ||
		next[0].(map[string]any)["user_config"] != filepath.Join(configHome, "huyang", "config.toml") {
		t.Fatalf("pipeline trust recovery is not actionable: %#v", inspected)
	}
}

func TestSearchDiagnosticsAndIdempotencyFailuresAreActionable(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "only once\n"})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()

	singular := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "once", "mode": "literal",
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
	diagnostics := callModern(t, session, "diagnostics", map[string]any{"workspace_id": workspaceID})
	if diagnostics["outcome"] != "unavailable" ||
		strings.Contains(diagnostics["summary"].(string), "retrieved") || len(diagnostics["next"].([]any)) == 0 {
		t.Fatalf("unavailable diagnostics are misleading: %#v", diagnostics)
	}

	hit := singular["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	arguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "replay-key",
		"operation": map[string]any{
			"kind": "replace_range", "target": map[string]any{"file_range": hit["range"]}, "content": "twice",
		},
	}
	first := callModern(t, session, "edit_apply", arguments)
	replayed := callModern(t, session, "edit_apply", arguments)
	replayData := replayed["data"].(map[string]any)
	if first["idempotency"] != "created" || replayed["idempotency"] != "replayed" ||
		replayData["canonical_changed"] != false || replayData["original_canonical_changed"] != true ||
		!strings.Contains(strings.ToLower(replayed["summary"].(string)), "no new mutation") {
		t.Fatalf("idempotent replay wording is ambiguous: %#v", replayed)
	}
	conflictArguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "replay-key",
		"operation": map[string]any{
			"kind": "replace_range", "target": map[string]any{"file_range": hit["range"]}, "content": "different",
		},
	}
	conflict := callModern(t, session, "edit_apply", conflictArguments)
	if conflict["code"] != "idempotency_key_reused" || len(conflict["next"].([]any)) == 0 {
		t.Fatalf("idempotency conflict has no recovery: %#v", conflict)
	}
}

func TestStaleRangeRecoveryNeverSuggestsEmptySearch(t *testing.T) {
	workspace, err := workspacecore.Open(workspacecore.OpenOptions{
		Kind: workspacecore.KindProject, Root: t.TempDir(), StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := modernHandleConflict("req_test", workspace, workspacecore.HandleResolution{
		Status:   workspacecore.ResolutionConflicted,
		Code:     workspacecore.ConflictDocumentChanged,
		Original: workspacecore.SemanticLocator{Path: "note.txt"},
	})
	next := result["next"].([]any)
	if len(next) != 1 || next[0].(map[string]any)["tool"] != "read" {
		t.Fatalf("range conflict suggested a search with no query: %#v", result)
	}
}

func TestDebuggerUnavailableNamesHuyangAndGivesRepairStep(t *testing.T) {
	workspace, err := workspacecore.Open(workspacecore.OpenOptions{
		Kind: workspacecore.KindProject, Root: t.TempDir(), StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := debugUnavailable("req_test", workspace, "debugger_unavailable", fmt.Errorf("agent99 adapter not installed"))
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "agent99") || len(result["next"].([]any)) != 2 ||
		result["data"].(map[string]any)["repair"].(map[string]any)["action"] != "install_or_configure_dap_adapter" {
		t.Fatalf("debugger recovery is obsolete or not actionable: %#v", result)
	}
}

func TestCompactTextEnvelopeBoundsTypedPlanPayload(t *testing.T) {
	large := strings.Repeat("large-marker-", 10000)
	envelope := map[string]any{
		"api_version": "huyang.workspace/v1alpha1",
		"request_id":  "req_test",
		"outcome":     "ok",
		"summary":     "Plan created",
		"data": map[string]any{
			"plan": workspacecore.PlanRecord{
				PlanID: "plan_test",
				Operations: []workspacecore.PlanOperation{{
					OpID:    "large-create",
					Kind:    workspacecore.OperationCreateFile,
					Path:    "large.txt",
					Content: large,
				}},
			},
		},
		"evidence": map[string]any{"ids": []string{}, "truncated": false},
		"warnings": []string{},
		"next":     []any{},
	}
	encoded, err := json.Marshal(compactTextEnvelope(envelope))
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 16<<10 || strings.Contains(string(encoded), large[:1024]) {
		t.Fatalf("compact text envelope retained large plan bytes: %d", len(encoded))
	}
}

func TestCanonicalChangedPathsUsesLatestPlanReceipt(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	workspaceID := workspacecore.ID("ws_receipt_test")
	direct.replays[string(workspaceID)+"\x00change_plan\x00receipt"] = &directReplay{
		complete: true,
		result: map[string]any{
			"data": map[string]any{
				"canonical_changed": true,
				"from_revision":     "wsrev_19",
				"revision":          "wsrev_20",
				"changed_paths":     []any{"pkg/a.py", "tests/test_a.py"},
			},
		},
	}
	got, err := direct.canonicalChangedPaths(workspaceID, 20)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pkg/a.py", "tests/test_a.py"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed paths = %#v, want %#v", got, want)
	}
}

type probeFailedPrepareStager struct {
	rollbacks int
}

func (s *probeFailedPrepareStager) Epoch() uint64 { return 1 }

func (s *probeFailedPrepareStager) Stage(context.Context, workspacecore.PlanStageRequest) error {
	return errors.New("python probe prepare failed")
}

func (s *probeFailedPrepareStager) Commit(context.Context, string) error { return nil }

func (s *probeFailedPrepareStager) Rollback(context.Context, string) error {
	s.rollbacks++
	return nil
}

func TestRevisionDiffIncludesCommittedPlanReceipt(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"sample.py": "value = 1\n"})
	applied := applyLiteralProbeEdit(t, direct, workspaceID, "1", "2", "plan-receipt-source")
	if applied["outcome"] != "ok" && applied["outcome"] != "provisional" {
		t.Fatalf("seed edit failed: %#v", applied)
	}

	direct.replayMu.Lock()
	for key := range direct.replays {
		if strings.HasPrefix(key, workspaceID+"\x00edit_apply\x00") {
			delete(direct.replays, key)
		}
	}
	direct.replays[workspaceID+"\x00change_plan\x00python-plan-apply"] = &directReplay{
		complete: true,
		result: map[string]any{"data": map[string]any{
			"canonical_changed": true,
			"from_revision":     "wsrev_1",
			"revision":          "wsrev_2",
			"plan": workspacecore.PlanRecord{Preparation: &workspacecore.PlanPreparation{
				CommittedDiffs: []workspacecore.ExactDiff{
					{Path: "sample.py", BeforeSHA256: "before-sample", AfterSHA256: "after-sample"},
					{Path: "other.py", BeforeSHA256: "before-other", AfterSHA256: "after-other"},
				},
			}},
		}},
	}
	direct.replayMu.Unlock()

	workspace := direct.get(workspacecore.ID(workspaceID))
	result := direct.revisionDiff("req_plan_receipt", workspace, map[string]any{
		"from_revision": "wsrev_1", "to_revision_or_current": "wsrev_2",
	})
	if result["outcome"] != "ok" {
		t.Fatalf("revision diff outcome = %#v", result)
	}
	diffs := result["data"].(map[string]any)["diffs"].([]any)
	if len(diffs) != 2 {
		t.Fatalf("revision diff returned %d plan diffs: %#v", len(diffs), result)
	}
}

func TestDiscardFailedPrepareWithoutRemainingSandbox(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"sample.py": "value = 1\n"})
	workspace := direct.get(workspacecore.ID(workspaceID))
	target, err := workspace.NewRange("sample.py", 8, 9)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := workspace.CreatePlan([]workspacecore.PlanOperation{{
		OpID: "replace-value", Kind: workspacecore.OperationReplaceRange,
		Target: &workspacecore.PlanTarget{FileRange: &target}, Content: "2",
	}})
	if err != nil {
		t.Fatal(err)
	}
	stager := &probeFailedPrepareStager{}
	if _, err := workspace.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager); err == nil {
		t.Fatal("prepare unexpectedly succeeded")
	}
	if stager.rollbacks != 1 {
		t.Fatalf("prepare rollback count = %d, want 1", stager.rollbacks)
	}

	failed, err := workspace.InspectPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	result := direct.changePlan(context.Background(), "req_discard_failed_prepare", workspace, map[string]any{
		"action": "discard", "plan_id": plan.PlanID, "plan_revision": float64(failed.PlanRevision),
	})
	if result["outcome"] != "ok" {
		t.Fatalf("discard failed after prepare cleanup: %#v", result)
	}
	transaction := result["transaction"].(map[string]any)
	if transaction["state"] != workspacecore.PlanDiscarded {
		t.Fatalf("discard state = %#v, want %s", transaction["state"], workspacecore.PlanDiscarded)
	}
}
