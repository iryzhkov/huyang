package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	workspacecore "agent99/internal/workspace"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkoukk/tiktoken-go"
)

func TestModernRegistryMatchesFrozenProfiles(t *testing.T) {
	if err := validateModernRegistry(); err != nil {
		t.Fatal(err)
	}
	fixtureBytes, err := os.ReadFile("../../docs/plans/fixtures/huyang-v1alpha1/contract-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Catalogs map[string][]string `json:"catalogs"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, profile := range modernProfileOrder {
		got := modernCatalogNames(profile)
		want := fixture.Catalogs[string(profile)]
		if !slices.Equal(got, want) {
			t.Fatalf("%s catalog = %v, want %v", profile, got, want)
		}
		first, err := catalogJSON(profile)
		if err != nil {
			t.Fatal(err)
		}
		second, err := catalogJSON(profile)
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(second) {
			t.Fatalf("%s catalog generation is nondeterministic", profile)
		}
	}
}

func TestRequestedMCPProfileIsExplicit(t *testing.T) {
	tests := []struct {
		arguments []string
		want      mcpProfile
		wantError bool
	}{
		{nil, profileLegacy, false},
		{[]string{"--profile", "full"}, profileFull, false},
		{[]string{"--profile", "orient"}, profileOrient, false},
		{[]string{"--profile", "edit"}, profileEdit, false},
		{[]string{"--profile", "debug"}, profileDebug, false},
		{[]string{"--profile", "future"}, "", true},
		{[]string{"--profile"}, "", true},
	}
	for _, test := range tests {
		got, err := requestedMCPProfile(test.arguments)
		if (err != nil) != test.wantError || got != test.want {
			t.Fatalf("requestedMCPProfile(%v) = %q, %v; want %q, error=%v", test.arguments, got, err, test.want, test.wantError)
		}
	}
}

func connectOfficialClient(t *testing.T, profile mcpProfile, direct *directWorkspaces) (*mcp.ClientSession, func()) {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := newSDKServer(profile, direct)
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "huyang-test", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	}
	return clientSession, cleanup
}

func listedToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	var names []string
	for descriptor, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if descriptor.OutputSchema == nil {
			t.Fatalf("%s has no output schema", descriptor.Name)
		}
		if descriptor.Annotations == nil {
			t.Fatalf("%s has no annotations", descriptor.Name)
		}
		names = append(names, descriptor.Name)
	}
	return names
}

func TestOfficialClientNegotiatesModernProtocolAndCatalogs(t *testing.T) {
	for _, profile := range modernProfileOrder {
		t.Run(string(profile), func(t *testing.T) {
			stateDir := t.TempDir()
			session, cleanup := connectOfficialClient(t, profile, newDirectWorkspaces(stateDir))
			defer cleanup()
			if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
				t.Fatalf("protocol = %q, want 2026-07-28", got)
			}
			got, want := listedToolNames(t, session), modernCatalogNames(profile)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("listed tools = %v, want sorted %v", got, want)
			}
		})
	}
}

func TestDirectModeDoesNotAdvertiseTasksWithoutCapability(t *testing.T) {
	session, cleanup := connectOfficialClient(t, profileFull, newDirectWorkspaces(t.TempDir()))
	defer cleanup()
	encoded, err := json.Marshal(session.InitializeResult().Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "task") {
		t.Fatalf("direct mode advertised unsupported Tasks capability: %s", encoded)
	}
}

func structuredMap(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func callModern(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return structuredMap(t, result)
}

func TestOfficialClientExercisesNativeDirectWorkspace(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "note.txt")
	if err := os.WriteFile(file, []byte("alpha beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, cleanup := connectOfficialClient(t, profileEdit, newDirectWorkspaces(t.TempDir()))
	defer cleanup()

	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "documents", "files": []string{file}})
	workspace := opened["workspace"].(map[string]any)
	workspaceID := workspace["id"].(string)
	if opened["outcome"] != "ok" || workspaceID == "" {
		t.Fatalf("open result = %#v", opened)
	}

	searched := callModern(t, session, "search", map[string]any{"workspace_id": workspaceID, "query": "beta", "mode": "literal"})
	searchData := searched["data"].(map[string]any)
	hits := searchData["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("search result = %#v", searched)
	}
	hit := hits[0].(map[string]any)
	target := map[string]any{"file_range": hit["range"]}
	preview := callModern(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "preview-1", "preview_only": true,
		"operation": map[string]any{"kind": "replace_range", "target": target, "content": "gamma"},
	})
	if preview["outcome"] != "ok" || preview["idempotency"] != "created" {
		t.Fatalf("preview = %#v", preview)
	}
	before, _ := os.ReadFile(file)
	if string(before) != "alpha beta\n" {
		t.Fatalf("preview mutated file: %q", before)
	}
	applyArguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "apply-1",
		"operation": map[string]any{"kind": "replace_range", "target": target, "content": "gamma"},
	}
	applied := callModern(t, session, "edit_apply", applyArguments)
	if applied["outcome"] != "provisional" || applied["idempotency"] != "created" {
		t.Fatalf("apply = %#v", applied)
	}
	replayed := callModern(t, session, "edit_apply", applyArguments)
	if replayed["outcome"] != "provisional" || replayed["idempotency"] != "replayed" ||
		replayed["request_id"] == applied["request_id"] {
		t.Fatalf("replay = %#v after %#v", replayed, applied)
	}
	conflictArguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "apply-1",
		"operation": map[string]any{"kind": "replace_range", "target": target, "content": "delta"},
	}
	conflicted := callModern(t, session, "edit_apply", conflictArguments)
	if conflicted["outcome"] != "conflict" || conflicted["code"] != "idempotency_key_reused" {
		t.Fatalf("idempotency conflict = %#v", conflicted)
	}
	after, _ := os.ReadFile(file)
	if string(after) != "alpha gamma\n" {
		t.Fatalf("applied bytes = %q", after)
	}

	read := callModern(t, session, "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       map[string]any{"file_range": map[string]any{"path": file, "revision_id": "display-only", "byte_start": 0, "byte_end": 0, "expected_sha256": "", "before_sha256": "", "after_sha256": "", "anchor_bytes": 0}},
	})
	if content := read["data"].(map[string]any)["content"]; content != "alpha gamma\n" {
		t.Fatalf("read content = %#v", content)
	}
}

func TestModernDiagnosticInboxNoticesAndEvidenceRetrieval(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(t.TempDir())
	session, cleanup := connectOfficialClient(t, profileOrient, direct)
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	workspace := direct.get(workspacecore.ID(workspaceID))
	version := int64(1)
	report, err := workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
		Kind: workspacecore.EvidencePush, ProviderID: "gopls#1", Producer: "gopls",
		Document: file, DocumentRevision: "rev_1", DocumentVersion: &version, ExpectedVersion: &version,
		Complete: true, Selected: true, Findings: []workspacecore.DiagnosticFinding{{
			Range: workspacecore.DiagnosticRange{StartLine: 1, EndLine: 1}, Severity: 1, Message: "undefined: x",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	inspection := callModern(t, session, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
	if updates, ok := inspection["diagnostic_updates"].([]any); !ok || len(updates) != 1 {
		t.Fatalf("same-workspace notice = %#v", inspection)
	}
	diagnostics := callModern(t, session, "diagnostics", map[string]any{"workspace_id": workspaceID})
	diagnosticData, ok := diagnostics["data"].(map[string]any)
	if !ok || diagnosticData["diagnostics"] == nil {
		t.Fatalf("diagnostics response = %#v", diagnostics)
	}
	data := diagnosticData["diagnostics"].(map[string]any)
	if data["confidence"] != "authoritative" || len(data["new"].([]any)) != 1 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	evidence := callModern(t, session, "evidence_get", map[string]any{
		"workspace_id": workspaceID, "evidence_id": report.EvidenceIDs[0],
	})
	if evidence["outcome"] != "ok" {
		t.Fatalf("evidence = %#v", evidence)
	}
	cursor := data["cursor"].(string)
	acknowledged := callModern(t, session, "diagnostics", map[string]any{"workspace_id": workspaceID, "since": cursor})
	acknowledgedData := acknowledged["data"].(map[string]any)["diagnostics"].(map[string]any)
	if notices, ok := acknowledgedData["notices"].([]any); ok && len(notices) != 0 {
		t.Fatalf("acknowledged notices remain: %#v", acknowledged)
	}
}

func TestOfficialClientPersistsIdempotentPlanPreviewAcrossRestart(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	file := filepath.Join(root, "note.txt")
	original := []byte("alpha beta gamma\n")
	if err := os.WriteFile(file, original, 0o600); err != nil {
		t.Fatal(err)
	}

	direct := newDirectWorkspaces(stateDir)
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	opened := callModern(t, session, "workspace_open", map[string]any{
		"kind": "documents", "files": []string{file},
	})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "beta", "mode": "literal",
	})
	hit := searched["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	createArguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "plan-create", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "replace-beta", "kind": "replace_range",
			"target": map[string]any{"file_range": hit["range"]}, "content": "delta",
		}},
	}
	created := callModern(t, session, "change_plan", createArguments)
	if created["outcome"] != "ok" || created["idempotency"] != "created" {
		t.Fatalf("create = %#v", created)
	}
	replayed := callModern(t, session, "change_plan", createArguments)
	if replayed["outcome"] != "ok" || replayed["idempotency"] != "replayed" {
		t.Fatalf("replayed create = %#v", replayed)
	}
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	planID := plan["plan_id"].(string)
	planRevision := plan["plan_revision"].(float64)
	previewed := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "plan-preview", "action": "preview",
		"plan_id": planID, "plan_revision": planRevision,
	})
	if previewed["outcome"] != "ok" {
		t.Fatalf("preview = %#v", previewed)
	}
	preview := previewed["data"].(map[string]any)["plan"].(map[string]any)["preview"].(map[string]any)
	previewRevision := preview["preview_revision"].(string)
	if preview["canonical_changed"] != false {
		t.Fatalf("preview claims canonical mutation: %#v", preview)
	}
	cleanup()
	if current, _ := os.ReadFile(file); !bytes.Equal(current, original) {
		t.Fatalf("preview mutated canonical bytes: %q", current)
	}

	restarted := newDirectWorkspaces(stateDir)
	if restarted.loadErr != nil {
		t.Fatal(restarted.loadErr)
	}
	session, cleanup = connectOfficialClient(t, profileEdit, restarted)
	defer cleanup()
	reopened := callModern(t, session, "workspace_open", map[string]any{
		"kind": "documents", "files": []string{file},
	})
	if reopened["workspace"].(map[string]any)["id"] != workspaceID {
		t.Fatalf("workspace identity changed: %#v", reopened)
	}
	inspected := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "plan-inspect", "action": "inspect",
		"plan_id": planID, "plan_revision": planRevision,
	})
	if inspected["outcome"] != "ok" {
		t.Fatalf("inspect after restart = %#v", inspected)
	}
	restoredPreview := inspected["data"].(map[string]any)["plan"].(map[string]any)["preview"].(map[string]any)
	if restoredPreview["preview_revision"] != previewRevision {
		t.Fatalf("preview revision changed across restart: %#v", restoredPreview)
	}
	repreviewed := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "plan-repreview", "action": "preview",
		"plan_id": planID, "plan_revision": planRevision,
	})
	repreview := repreviewed["data"].(map[string]any)["plan"].(map[string]any)["preview"].(map[string]any)
	if repreview["preview_revision"] != previewRevision {
		t.Fatalf("repreview is nondeterministic: %#v", repreview)
	}
	if current, _ := os.ReadFile(file); !bytes.Equal(current, original) {
		t.Fatalf("restart preview mutated canonical bytes: %q", current)
	}
}

func TestOfficialClientUsesOpaqueHandlesAndRefinesFrozenSearchSets(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.txt")
	second := filepath.Join(root, "second.txt")
	if err := os.WriteFile(first, []byte("alpha beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("beta gamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, cleanup := connectOfficialClient(t, profileEdit, newDirectWorkspaces(t.TempDir()))
	defer cleanup()

	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "documents", "files": []string{first, second}})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "beta", "mode": "literal",
	})
	searchData := searched["data"].(map[string]any)
	resultSet := searchData["result_set"].(map[string]any)
	if resultSet["kind"] != "current_source" || resultSet["match_count"] != float64(2) ||
		resultSet["all_matches_eligible"] != true {
		t.Fatalf("search result set = %#v", resultSet)
	}
	refined := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "result_set_handle": resultSet["handle"],
		"refine": map[string]any{"path": "first.txt"},
	})
	refinedData := refined["data"].(map[string]any)
	refinedSet := refinedData["result_set"].(map[string]any)
	if refined["outcome"] != "ok" || refinedSet["parent"] != resultSet["handle"] ||
		refinedSet["retained"] != float64(1) || refinedSet["eliminated"] != float64(1) || len(refinedData["hits"].([]any)) != 1 {
		t.Fatalf("refined result = %#v", refined)
	}

	hit := searchData["hits"].([]any)[0].(map[string]any)
	opaque := hit["match_handle"].(map[string]any)["handle"].(string)
	read := callModern(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "target": map[string]any{"handle": opaque},
	})
	if read["outcome"] != "ok" || read["data"].(map[string]any)["content"] != "beta" {
		t.Fatalf("handle read = %#v", read)
	}
	applied := callModern(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "opaque-apply",
		"operation": map[string]any{"kind": "replace_range", "target": map[string]any{"handle": opaque}, "content": "delta"},
	})
	if applied["outcome"] != "provisional" {
		t.Fatalf("handle edit = %#v", applied)
	}
	content, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "alpha delta\n" {
		t.Fatalf("handle edit bytes = %q", content)
	}
}

func TestOfficialClientReadsBoundedGitProvenance(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "-C", root, "init", "-q")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	file := filepath.Join(root, "note.txt")
	if err := os.WriteFile(file, []byte("alpha\nneedle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "note.txt"}, {"commit", "-m", "mention needle"}} {
		command = exec.Command("git", append([]string{"-C", root}, arguments...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}

	session, cleanup := connectOfficialClient(t, profileOrient, newDirectWorkspaces(t.TempDir()))
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	recent := opened["data"].(map[string]any)["recent_commits"].(map[string]any)
	commits := recent["commits"].([]any)
	if len(commits) != 1 {
		t.Fatalf("recent commits = %#v", recent)
	}
	commitHandle := commits[0].(map[string]any)["handle"].(string)

	history := callModern(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "view": "history", "limit": 10,
		"target": map[string]any{"file_range": map[string]any{
			"path": "note.txt", "revision_id": "display-only", "byte_start": 0, "byte_end": 0,
			"expected_sha256": "", "before_sha256": "", "after_sha256": "", "anchor_bytes": 0,
		}},
	})
	if history["outcome"] != "ok" || len(history["data"].(map[string]any)["spans"].([]any)) == 0 {
		t.Fatalf("history = %#v", history)
	}
	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID,
		"git_history":  map[string]any{"query": "needle", "fields": []string{"message", "diff"}, "limit": 10},
	})
	set := searched["data"].(map[string]any)["result_set"].(map[string]any)
	if searched["outcome"] != "ok" || set["kind"] != "historical" || set["all_matches_eligible"] != false {
		t.Fatalf("historical search = %#v", searched)
	}
	changes := callModern(t, session, "read", map[string]any{
		"workspace_id": workspaceID, "view": "changes", "limit": 10,
		"target": map[string]any{"handle": commitHandle},
	})
	if changes["outcome"] != "ok" || len(changes["data"].(map[string]any)["changes"].([]any)) != 1 {
		t.Fatalf("changes = %#v", changes)
	}
}

func TestOfficialClientMatchesConcurrentResponsesToRequests(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	session, cleanup := connectOfficialClient(t, profileOrient, direct)
	defer cleanup()

	var ids []string
	for index := 0; index < 4; index++ {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte(strings.Repeat("x", index+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
		ids = append(ids, opened["workspace"].(map[string]any)["id"].(string))
	}

	var wait sync.WaitGroup
	failures := make(chan string, 32)
	for index := 0; index < 32; index++ {
		want := ids[index%len(ids)]
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "workspace_inspect", Arguments: map[string]any{"workspace_id": want},
			})
			if err != nil {
				failures <- err.Error()
				return
			}
			value := structuredMap(t, result)
			got := value["workspace"].(map[string]any)["id"].(string)
			if got != want {
				failures <- "response workspace " + got + " does not match request " + want
			}
		}()
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

func TestSDKPropagatesToolCancellation(t *testing.T) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "cancel-test", Version: "1"}, nil)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server.AddTool(&mcp.Tool{Name: "wait", InputSchema: schemaObject(map[string]any{})}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "cancel-client", Version: "1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{}})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler context was not cancelled")
	}
	<-done
}

func TestDirectModeRejectsMalformedAndAcceptsLargeArguments(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	if _, err := decodeArguments(json.RawMessage("[]")); err == nil {
		t.Fatal("array arguments were accepted")
	}
	searchSchema := modernCatalog(profileOrient)[2].InputSchema
	if err := validateToolArguments(searchSchema, map[string]any{"workspace_id": "ws"}); err == nil {
		t.Fatal("missing required query was accepted")
	}
	if err := validateToolArguments(searchSchema, map[string]any{
		"workspace_id": "ws", "query": "q", "unknown": true,
	}); err == nil {
		t.Fatal("unknown property was accepted")
	}
	if err := validateToolArguments(searchSchema, map[string]any{
		"workspace_id": "ws", "query": "q", "mode": "invalid",
	}); err == nil {
		t.Fatal("invalid enum value was accepted")
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, cleanup := connectOfficialClient(t, profileOrient, direct)
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	id := opened["workspace"].(map[string]any)["id"].(string)
	result := callModern(t, session, "search", map[string]any{
		"workspace_id": id, "query": strings.Repeat("z", 1<<20), "mode": "literal",
	})
	if result["outcome"] != "ok" {
		t.Fatalf("large request = %#v", result)
	}
}

func TestModernCatalogTokenBudgets(t *testing.T) {
	encoding, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range modernProfileOrder {
		session, cleanup := connectOfficialClient(t, profile, newDirectWorkspaces(t.TempDir()))
		listed, err := session.ListTools(context.Background(), nil)
		if err != nil {
			cleanup()
			t.Fatal(err)
		}
		compact, err := json.Marshal(listed.Tools)
		if err != nil {
			cleanup()
			t.Fatal(err)
		}
		pretty, err := json.MarshalIndent(listed.Tools, "", "  ")
		if err != nil {
			cleanup()
			t.Fatal(err)
		}
		totalTokens := len(encoding.Encode(string(compact), nil, nil))
		t.Logf("profile=%s tools=%d bytes=%d lines=%d cl100k_base_tokens=%d",
			profile, len(listed.Tools), len(compact), strings.Count(string(pretty), "\n")+1, totalTokens)
		for _, tool := range listed.Tools {
			encoded, err := json.Marshal(tool)
			if err != nil {
				cleanup()
				t.Fatal(err)
			}
			t.Logf("profile=%s tool=%s bytes=%d cl100k_base_tokens=%d",
				profile, tool.Name, len(encoded), len(encoding.Encode(string(encoded), nil, nil)))
		}
		cleanup()
	}
}
