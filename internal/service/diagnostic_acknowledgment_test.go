package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/handlers"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func diagnosticAckScratch(t *testing.T) string {
	t.Helper()
	scratch := t.TempDir()
	// TempDir siblings allocated by openTestProject share this test-owned root.
	home := filepath.Dir(scratch)
	t.Setenv("HOME", home)
	if os.Getenv("HOME") != home {
		t.Fatal("HOME must name the test scratch directory before fixture writes")
	}
	return scratch
}

func diagnosticAckReport(t *testing.T, reply map[string]any) workspacecore.DiagnosticReport {
	t.Helper()
	data, ok := reply["data"].(map[string]any)
	if !ok {
		t.Fatalf("diagnostics omitted data: %#v", reply)
	}
	report, ok := data["diagnostics"].(workspacecore.DiagnosticReport)
	if !ok {
		t.Fatalf("full diagnostics omitted report: %#v", reply)
	}
	return report
}

func TestDiagnosticAcknowledgmentDelivery(t *testing.T) {
	direct := newDirectWorkspaces(diagnosticAckScratch(t))
	workspaceID, workspace := openTestProject(t, direct, map[string]string{"main.go": "package main\n"})
	file := filepath.Join(workspace.Identity().Root, "main.go")
	a := handlers.WithClientIdentity(context.Background(), "ack-a")
	b := handlers.WithClientIdentity(context.Background(), "ack-b")
	inspect := func(ctx context.Context) map[string]any {
		return direct.call(ctx, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
	}
	full := func(ctx context.Context, since string) workspacecore.DiagnosticReport {
		return diagnosticAckReport(t, direct.call(ctx, "diagnostics", map[string]any{
			"workspace_id": workspaceID, "full": true, "since": since,
		}))
	}
	inspect(a)
	inspect(b)
	if _, err := workspace.Read(file); err != nil {
		t.Fatal(err)
	}
	recordTestFinding(t, workspace, file, 0)
	first := full(a, "")
	cursor := first.Cursor
	if len(first.Notices) != 1 || len(first.Current) != 1 {
		t.Fatalf("initial finding missing: %+v", first)
	}
	id := first.Current[0].ID
	ack := full(a, cursor)
	if len(ack.Notices) != 0 || len(ack.Current) != 1 || ack.Current[0].ID != id {
		t.Fatalf("acknowledgment lost current body: %+v", ack)
	}
	// Refreshing native bytes creates the same stale/persisting pair as M9.
	if err := os.WriteFile(file, []byte("package main\n// marker two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Read(file); err != nil {
		t.Fatal(err)
	}
	recordTestFinding(t, workspace, file, 0)
	remaining := full(a, cursor)
	if remaining.NoticesTruncated || len(remaining.Notices) != 2 ||
		remaining.Notices[0].Kind != "stale" || remaining.Notices[1].Kind != "persisting" ||
		len(remaining.Current) != 1 || remaining.Current[0].ID != id {
		t.Fatalf("retained delta lost transitions or identity: %+v", remaining)
	}
	for _, notice := range remaining.Notices {
		if notice.ID != id {
			t.Fatalf("finding identity changed: %+v", notice)
		}
	}
	delivered := inspect(a)
	updates, _ := delivered["diagnostic_updates"].([]handlers.DiagnosticUpdate)
	if delivered["diagnostic_updates_truncated"] != nil || len(updates) != 1 ||
		updates[0].Kind != "persisting" || updates[0].ID != id {
		t.Fatalf("acknowledging session has a false compact gap: %#v", delivered)
	}
	other := inspect(b)
	if other["diagnostic_updates_truncated"] != true {
		t.Fatalf("another session's real gap was hidden: %#v", other)
	}
	// Explicit zero and omitted cursors still request pruned history.
	for _, since := range []string{"", "diagcur_0"} {
		if report := full(a, since); !report.NoticesTruncated {
			t.Fatalf("retired cursor %q lost its gap flag: %+v", since, report)
		}
	}
	if report := full(a, cursor); report.NoticesTruncated || len(report.Notices) != 2 {
		t.Fatalf("a retired request moved the acknowledged floor backwards: %+v", report)
	}
}

func TestDiagnosticAcknowledgmentUsesExplicitCursor(t *testing.T) {
	direct := newDirectWorkspaces(diagnosticAckScratch(t))
	workspaceID, workspace := openTestProject(t, direct, map[string]string{"main.go": "package main\n"})
	file := filepath.Join(workspace.Identity().Root, "main.go")
	client := handlers.WithClientIdentity(context.Background(), "explicit-ack")
	direct.call(client, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
	first := recordTestFinding(t, workspace, file, 0)
	// This publication exists before the acknowledgment but is beyond its cursor.
	second := recordTestFinding(t, workspace, file, 1)
	ack := diagnosticAckReport(t, direct.call(client, "diagnostics", map[string]any{
		"workspace_id": workspaceID, "full": true, "since": first.Cursor,
	}))
	if ack.Cursor != second.Cursor || len(ack.Notices) == 0 {
		t.Fatalf("fixture has no report head beyond the explicit ack: %+v", ack)
	}
	result := direct.call(client, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
	updates, _ := result["diagnostic_updates"].([]handlers.DiagnosticUpdate)
	if result["diagnostic_updates_truncated"] != nil || len(updates) != 2 {
		t.Fatalf("delivery skipped post-ack notices or reported a false gap: %#v", result)
	}
	// A lower explicit cursor must not rewind the compact delivery cursor.
	direct.call(client, "diagnostics", map[string]any{"workspace_id": workspaceID, "since": "diagcur_0"})
	if again := direct.call(client, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"}); again["diagnostic_updates"] != nil {
		t.Fatalf("older acknowledgment replayed compact notices: %#v", again)
	}
}

func TestDiagnosticAcknowledgmentRejectsFailedCalls(t *testing.T) {
	for _, failure := range []string{"invalid", "cancelled", "unknown-workspace"} {
		t.Run(failure, func(t *testing.T) {
			direct := newDirectWorkspaces(diagnosticAckScratch(t))
			workspaceID, workspace := openTestProject(t, direct, map[string]string{"main.go": "package main\n"})
			client := handlers.WithClientIdentity(context.Background(), "failed-ack")
			direct.call(client, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
			first := recordTestFinding(t, workspace, filepath.Join(workspace.Identity().Root, "main.go"), 0)
			args := map[string]any{"workspace_id": workspaceID, "since": first.Cursor}
			ctx := client
			switch failure {
			case "invalid":
				args["since"] = "diagcur_invalid"
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "unknown-workspace":
				args["workspace_id"] = "ws_missing"
			}
			if reply := direct.call(ctx, "diagnostics", args); reply["outcome"] != "failed" {
				t.Fatalf("fixture did not fail: %#v", reply)
			}
			result := direct.call(client, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
			updates, _ := result["diagnostic_updates"].([]handlers.DiagnosticUpdate)
			if result["diagnostic_updates_truncated"] != nil || len(updates) != 1 || updates[0].Kind != "new" {
				t.Fatalf("failed acknowledgment consumed an unseen notice: %#v", result)
			}
		})
	}
}

func TestDiagnosticAcknowledgmentRetentionGap(t *testing.T) {
	direct := newDirectWorkspaces(diagnosticAckScratch(t))
	workspaceID, workspace := openTestProject(t, direct, map[string]string{"main.go": "package main\n"})
	file := filepath.Join(workspace.Identity().Root, "main.go")
	client := handlers.WithClientIdentity(context.Background(), "retention-ack")
	direct.call(client, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
	first := recordTestFinding(t, workspace, file, 0)
	direct.call(client, "diagnostics", map[string]any{"workspace_id": workspaceID, "since": first.Cursor})
	// One bulk publication exceeds the persisted 1,000-notice bound without
	// repeatedly saving the store or depending on a live language server.
	batch := workspacecore.DiagnosticBatch{
		Kind: workspacecore.EvidencePull, ProviderID: "gopls#1", Producer: "gopls",
		Document: file, Selected: true,
	}
	for i := 0; i < 1001; i++ {
		batch.Findings = append(batch.Findings, workspacecore.DiagnosticFinding{
			Range: workspacecore.DiagnosticRange{StartLine: i + 2}, Severity: 1,
			Message: fmt.Sprintf("retained finding %d", i),
		})
	}
	if _, err := workspace.RecordDiagnosticEvidence(batch); err != nil {
		t.Fatal(err)
	}
	report := diagnosticAckReport(t, direct.call(client, "diagnostics", map[string]any{
		"workspace_id": workspaceID, "full": true, "since": first.Cursor,
	}))
	if !report.NoticesTruncated || len(report.Notices) != 1000 {
		t.Fatalf("retention gap was hidden: truncated=%v notices=%d", report.NoticesTruncated, len(report.Notices))
	}
	result := direct.call(client, "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
	if result["diagnostic_updates_truncated"] != true {
		t.Fatalf("compact retention gap was hidden: %#v", result)
	}
}
