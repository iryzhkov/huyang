package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/handlers"
	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func recordTestFinding(t *testing.T, workspace *workspacecore.Workspace, file string, index int) workspacecore.DiagnosticReport {
	t.Helper()
	version := int64(index + 1)
	report, err := workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
		Kind: workspacecore.EvidencePush, ProviderID: "gopls#1", Producer: "gopls",
		Document: file, DocumentRevision: "rev_1", DocumentVersion: &version, ExpectedVersion: &version,
		Complete: true, Selected: true, Findings: []workspacecore.DiagnosticFinding{{
			Range: workspacecore.DiagnosticRange{StartLine: index + 1, EndLine: index + 1}, Severity: 1,
			Message: fmt.Sprintf("undefined: x%d", index),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestDiagnosticUpdatesAreDeliveredOncePerClientAndCapped(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	workspaceID, workspace := openTestProject(t, direct, map[string]string{"main.go": "package main\n"})
	file := filepath.Join(workspace.Identity().Root, "main.go")
	for index := 0; index < handlers.MaxDiagnosticUpdates+5; index++ {
		recordTestFinding(t, workspace, file, index)
	}
	clientA := handlers.WithClientIdentity(context.Background(), "client-a")
	inspect := func(ctx context.Context) map[string]any {
		return direct.call(ctx, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
	}
	// Each recorded finding supersedes the previous one for the same
	// document, so 25 findings leave 25 new and 24 resolved notices.
	total := 2*(handlers.MaxDiagnosticUpdates+5) - 1
	seen := map[string]bool{}
	deliveries := 0
	for {
		reply := inspect(clientA)
		updates, present := reply["diagnostic_updates"].([]workspacecore.DiagnosticNotice)
		if !present {
			break
		}
		deliveries++
		if len(updates) > handlers.MaxDiagnosticUpdates {
			t.Fatalf("delivery %d carried %d notices", deliveries, len(updates))
		}
		for _, notice := range updates {
			if seen[notice.Cursor] {
				t.Fatalf("notice %s was delivered twice", notice.Cursor)
			}
			seen[notice.Cursor] = true
		}
		if truncated := reply["diagnostic_updates_truncated"] == true; truncated != (len(seen) < total) {
			t.Fatalf("delivery %d truncated=%v with %d of %d seen", deliveries, truncated, len(seen), total)
		}
		if deliveries > 10 {
			t.Fatal("delivery never drained")
		}
	}
	if len(seen) != total || deliveries != 3 {
		t.Fatalf("delivered %d notices in %d replies, want %d in 3", len(seen), deliveries, total)
	}
	other := inspect(handlers.WithClientIdentity(context.Background(), "client-b"))
	updates, _ := other["diagnostic_updates"].([]workspacecore.DiagnosticNotice)
	if len(updates) != handlers.MaxDiagnosticUpdates || other["diagnostic_updates_truncated"] != true {
		t.Fatalf("another client saw %d notices", len(updates))
	}
	// Acknowledging through the diagnostics cursor clears the inbox for everyone.
	report := direct.call(clientA, "diagnostics", map[string]any{"workspace_id": workspaceID})
	cursor := report["data"].(map[string]any)["diagnostics"].(map[string]any)["cursor"].(string)
	direct.call(clientA, "diagnostics", map[string]any{"workspace_id": workspaceID, "since": cursor})
	if after := inspect(handlers.WithClientIdentity(context.Background(), "client-c")); after["diagnostic_updates"] != nil {
		t.Fatalf("acknowledged notices were still delivered: %#v", after["diagnostic_updates"])
	}
}

func TestDiagnosticsReportIsCompactWithEvidenceOnlyOnTheEnvelope(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	workspaceID, workspace := openTestProject(t, direct, map[string]string{"main.go": "package main\n"})
	unavailable := direct.call(context.Background(), "diagnostics", map[string]any{"workspace_id": workspaceID})
	report := unavailable["data"].(map[string]any)["diagnostics"].(map[string]any)
	if unavailable["outcome"] != "unavailable" {
		t.Fatalf("outcome = %#v", unavailable)
	}
	if _, present := report["new"]; present {
		t.Fatalf("unavailable report carries bodies: %#v", report)
	}
	if reasons := report["provisional_reasons"].([]string); len(reasons) != 1 || reasons[0] != "no_diagnostic_evidence" {
		t.Fatalf("reasons = %#v", report["provisional_reasons"])
	}
	for _, key := range []string{"evidence_ids", "resolved"} {
		if _, present := report[key]; present {
			t.Fatalf("compact report repeats %s", key)
		}
	}

	file := filepath.Join(workspace.Identity().Root, "main.go")
	recorded := recordTestFinding(t, workspace, file, 0)
	available := direct.call(context.Background(), "diagnostics", map[string]any{"workspace_id": workspaceID})
	report = available["data"].(map[string]any)["diagnostics"].(map[string]any)
	if available["outcome"] != "ok" || report["new_count"] != 1 || len(report["new"].([]map[string]any)) != 1 {
		t.Fatalf("available report = %#v", available)
	}
	item := report["new"].([]map[string]any)[0]
	if item["message"] != "undefined: x0" || item["attribution"] == nil {
		t.Fatalf("compact item = %#v", item)
	}
	ids := available["evidence"].(map[string]any)["ids"].([]string)
	if len(ids) != 1 || ids[0] != recorded.EvidenceIDs[0] {
		t.Fatalf("envelope evidence = %#v, want %v", ids, recorded.EvidenceIDs)
	}
	coverage := report["coverage"].(map[string]any)
	for _, dimension := range coverage {
		if _, present := dimension.(map[string]any)["evidence_ids"]; present {
			t.Fatalf("coverage repeats evidence IDs: %#v", coverage)
		}
	}
	if _, present := report["resolved_ids"]; !present {
		t.Fatalf("compact report lacks resolved_ids: %#v", report)
	}
	full := direct.call(context.Background(), "diagnostics", map[string]any{"workspace_id": workspaceID, "full": true})
	if _, ok := full["data"].(map[string]any)["diagnostics"].(workspacecore.DiagnosticReport); !ok {
		t.Fatalf("full report = %#v", full["data"])
	}
}

func TestSearchHitsCarryAnchorsOnlyOnRequest(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	workspaceID, _ := openTestProject(t, direct, map[string]string{"main.go": "package main\nvar needle = 1\n"})
	compact := direct.call(context.Background(), "search", map[string]any{"workspace_id": workspaceID, "query": "needle"})
	hit := compact["data"].(map[string]any)["hits"].([]map[string]any)[0]
	for _, key := range []string{"range", "match_handle", "byte_start", "byte_end"} {
		if _, present := hit[key]; present {
			t.Fatalf("compact hit carries %s: %#v", key, hit)
		}
	}
	if hit["path"] != "main.go" || hit["line"] != 2 || hit["match"] != "needle" || hit["handle"] == nil {
		t.Fatalf("compact hit = %#v", hit)
	}
	anchored := direct.call(context.Background(), "search", map[string]any{"workspace_id": workspaceID, "query": "needle", "include_ranges": true})
	hit = anchored["data"].(map[string]any)["hits"].([]map[string]any)[0]
	if _, ok := hit["range"].(workspacecore.RangeHandle); !ok {
		t.Fatalf("anchored hit lacks range: %#v", hit)
	}
	set := anchored["data"].(map[string]any)["result_set"].(map[string]any)
	refined := direct.call(context.Background(), "search", map[string]any{
		"workspace_id": workspaceID, "result_set_handle": fmt.Sprint(set["handle"]), "refine": map[string]any{"path": "main"},
	})
	hit = refined["data"].(map[string]any)["hits"].([]map[string]any)[0]
	if _, present := hit["range"]; present {
		t.Fatalf("refined hit carries anchors by default: %#v", hit)
	}
}

func TestWorkspaceOpenOverviewIsCompactByDefault(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"README.md": "# r\n", "pkg/a.go": "package pkg\n", "pkg/sub/b.go": "package sub\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(arguments ...string) {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	run("init", "-q")
	run("add", ".")
	for index := 0; index < 5; index++ {
		run("commit", "-q", "--allow-empty", "-m", fmt.Sprintf("commit %d", index))
	}
	direct := newDirectWorkspaces(t.TempDir())
	opened := direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
	data := opened["data"].(map[string]any)
	overview := data["overview"].(map[string]any)
	if overview["entry_count"] != 3 || overview["top_level_count"] != 2 {
		t.Fatalf("overview = %#v", overview)
	}
	topLevel := overview["top_level"].([]map[string]any)
	if len(topLevel) != 2 || topLevel[0]["path"] != "README.md" || topLevel[1]["path"] != "pkg" ||
		topLevel[1]["kind"] != "directory" || topLevel[1]["files"] != 2 {
		t.Fatalf("top level = %#v", topLevel)
	}
	if _, present := overview["entries"]; present {
		t.Fatal("compact overview still lists entries")
	}
	commits := data["recent_commits"].(map[string]any)["commits"].([]map[string]any)
	if len(commits) != 3 || commits[0]["subject"] != "commit 4" || commits[0]["handle"] == nil {
		t.Fatalf("recent commits = %#v", commits)
	}
	if _, present := commits[0]["author_name"]; present {
		t.Fatalf("compact commit carries author metadata: %#v", commits[0])
	}
	full := direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root, "overview": "full"})
	if orientation, ok := full["data"].(map[string]any)["overview"].(workspacecore.Orientation); !ok || len(orientation.Entries) != 3 {
		t.Fatalf("full overview = %#v", full["data"].(map[string]any)["overview"])
	}
}

type installOptionsProvider struct {
	stubProvider
}

func (p *installOptionsProvider) Call(ctx context.Context, request provider.Request) (provider.Result, error) {
	if request.Operation == "workspace_support" {
		return provider.Result{Value: map[string]any{"languages": []any{
			map[string]any{"filetype": "python", "lsp": "none", "install_options": []any{"pyright", "basedpyright"}},
			map[string]any{"filetype": "rust", "lsp": "none", "install_options": []any{"rust_analyzer"}},
		}}}, nil
	}
	return p.stubProvider.Call(ctx, request)
}

func TestLanguageServerStatusListsInstallOptionsOnce(t *testing.T) {
	backend := &installOptionsProvider{}
	backend.descriptor = provider.Descriptor{ID: "options", Backend: "test", Epoch: 1}
	useFixedProvider(t, backend)
	direct := newDirectWorkspaces(t.TempDir())
	workspaceID, _ := openTestProject(t, direct, map[string]string{"main.py": "x = 1\n"})
	status := direct.call(context.Background(), "language_server_status", map[string]any{"workspace_id": workspaceID})
	data := status["data"].(map[string]any)
	options := data["install_options"].(map[string][]string)
	if len(options["python"]) != 2 || len(options["rust"]) != 1 {
		t.Fatalf("install options = %#v", options)
	}
	for _, raw := range mcpapi.AnySlice(data["language_servers"].(map[string]any)["languages"]) {
		entry := raw.(map[string]any)
		if _, present := entry["install_options"]; present {
			t.Fatalf("language entry repeats install options: %#v", entry)
		}
		if entry["install_options_key"] != entry["filetype"] {
			t.Fatalf("language entry lacks the options key: %#v", entry)
		}
	}
	for _, raw := range status["next"].([]any) {
		action := raw.(map[string]any)
		if _, present := action["server_options"]; present {
			t.Fatalf("next repeats install options: %#v", action)
		}
		if action["install_options_key"] != action["language"] {
			t.Fatalf("next lacks the options key: %#v", action)
		}
	}
}

// useFixedProvider installs a factory returning one fixed provider.
func useFixedProvider(t *testing.T, backend provider.Provider) {
	t.Helper()
	previous := providerpool.DefaultFactory
	providerpool.DefaultFactory = fixedProviderFactory{backend: backend}
	t.Cleanup(func() { providerpool.DefaultFactory = previous })
}

type fixedProviderFactory struct{ backend provider.Provider }

func (f fixedProviderFactory) Open(providerpool.OpenConfig) (provider.Provider, error) {
	return f.backend, nil
}

func TestReadSymbolLocatorFallsBackToProviderDeclarations(t *testing.T) {
	backend := newStubProvider()
	useStubProvider(t, backend)
	direct := newDirectWorkspaces(t.TempDir())
	source := "package model\n\ntype Shipment struct {\n\tID string\n}\n\nfunc other() {}\n"
	workspaceID, _ := openTestProject(t, direct, map[string]string{"model.go": source})
	read := direct.call(context.Background(), "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       map[string]any{"symbol_locator": map[string]any{"path": "model.go", "name_path": "Shipment"}},
	})
	if read["outcome"] != "ok" {
		t.Fatalf("symbol read = %#v", read)
	}
	content := read["data"].(map[string]any)["content"].(string)
	if !strings.HasPrefix(content, "type Shipment struct {") || strings.Contains(content, "func other") {
		t.Fatalf("symbol content = %q", content)
	}
	if coverage := read["data"].(map[string]any)["coverage"].(workspacecore.Coverage); !coverage.Complete {
		t.Fatalf("coverage = %#v", coverage)
	}
	missing := direct.call(context.Background(), "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       map[string]any{"symbol_locator": map[string]any{"path": "model.go", "name_path": "Nowhere"}},
	})
	if missing["outcome"] == "ok" {
		t.Fatalf("unknown symbol read succeeded: %#v", missing)
	}
}

// Structured results bound the workspace map at mcpapi.MaxStructuredEntries and
// replace large plan operation bodies with their size and hash, while every
// follow-up identifier stays present.
func TestStructuredResponsesBoundLargePlansAndWorkspaceMaps(t *testing.T) {
	files := make(map[string]string, mcpapi.MaxStructuredEntries+25)
	for index := 0; index < mcpapi.MaxStructuredEntries+25; index++ {
		files[fmt.Sprintf("pkg/file_%03d.go", index)] = "package pkg\n"
	}
	files["large.txt"] = "seed"
	direct, workspaceID, _ := openProbeProject(t, files)
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()

	inspected := callModern(t, session, "workspace_inspect", map[string]any{
		"workspace_id": workspaceID, "view": "map",
	})
	overview := inspected["data"].(map[string]any)["overview"].(map[string]any)
	if overview["entry_count"] != float64(len(files)) || overview["entries_truncated"] != true {
		t.Fatalf("bounded map metadata = %#v", overview)
	}
	if entries := overview["entries"].([]any); len(entries) != mcpapi.MaxStructuredEntries {
		t.Fatalf("bounded map returned %d entries", len(entries))
	}

	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "seed", "mode": "literal",
		"include_ranges": true,
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
