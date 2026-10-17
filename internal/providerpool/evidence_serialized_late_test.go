package providerpool

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Lua must serialize its pull/push slots across RPCs: the real late coalescer
// deliberately keeps only the last observation for a provider/document.
func TestLateEvidenceSerializedPullPushLedger(t *testing.T) {
	for _, positive := range []bool{false, true} {
		name := "empty_push"
		if positive {
			name = "positive_push"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			w, err := workspacecore.New(workspacecore.KindProject, root, 1)
			if err != nil {
				t.Fatal(err)
			}
			version := int64(9)
			finding := func(message string) []workspacecore.DiagnosticFinding {
				return []workspacecore.DiagnosticFinding{{Severity: 1, Message: message}}
			}
			push := workspacecore.DiagnosticBatch{
				Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test",
				Document: filepath.Join(root, "a.go"), DocumentRevision: "docrev_current",
				DocumentVersion: &version, ExpectedVersion: &version, TransactionID: "edit_current",
				Complete: true, Selected: true,
			}
			baseline := push
			baseline.Findings = finding("previous finding")
			initial, err := w.RecordDiagnosticEvidence(baseline)
			if err != nil {
				t.Fatal(err)
			}
			pull := push
			pull.Kind, pull.Complete, pull.DocumentVersion = workspacecore.EvidencePull, false, nil
			pull.ResultID, pull.Findings = "opaque", finding("late pull finding")
			unstamped := push
			unstamped.Complete, unstamped.DocumentVersion = false, nil
			unstamped.TransactionID, unstamped.DocumentRevision = "", ""
			unstamped.Findings = finding("late unstamped push finding")
			lastPull := pull
			lastPull.ResultID, lastPull.Findings = "changed-opaque", finding("pull before authoritative push")
			if positive {
				push.Findings = finding("authoritative push finding")
			}

			// Both array orders collide, regardless of evidence kind or strength.
			for _, mixed := range [][]workspacecore.DiagnosticBatch{{lastPull, push}, {push, lastPull}} {
				coalesced := coalesceLateDiagnosticBatches(mixed)
				if len(coalesced) != 1 || coalesced[0].Kind != mixed[1].Kind {
					t.Fatalf("unexpected late coalescing contract: %+v", coalesced)
				}
			}
			p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{
				ID: "test", Root: root, Epoch: 1,
			}}}
			cursor := initial.Cursor
			for i, batch := range []workspacecore.DiagnosticBatch{pull, unstamped, lastPull, push} {
				p.batches = []workspacecore.DiagnosticBatch{batch}
				count, err := CollectLateEvidence(context.Background(), w, p)
				if err != nil || count != 1 {
					t.Fatalf("collection %d: count=%d err=%v", i, count, err)
				}
				report, err := w.Diagnostics(cursor)
				if err != nil {
					t.Fatal(err)
				}
				if !batch.Complete {
					if report.Confidence != workspacecore.ConfidenceProvisional ||
						report.CurrentCount != i+2 || len(report.New) != 1 || len(report.Resolved) != 0 ||
						report.New[0].Finding.Message != batch.Findings[0].Message {
						t.Fatalf("collection %d lost facts or proved omission: %+v", i, report)
					}
				} else {
					if report.Confidence != workspacecore.ConfidenceAuthoritative ||
						report.CurrentCount != len(push.Findings) || len(report.Resolved) != 4 {
						t.Fatalf("current push did not establish its exact body: %+v", report)
					}
					if positive && (len(report.Current) != 1 ||
						report.Current[0].Finding.Message != push.Findings[0].Message) {
						t.Fatalf("positive authoritative body lost: %+v", report)
					}
				}
				cursor = report.Cursor
			}
			// Draining the Lua slots sends no stale provisional observation after
			// proof. The real collector must leave coverage and transitions alone.
			p.batches = nil
			count, err := CollectLateEvidence(context.Background(), w, p)
			if err != nil || count != 0 {
				t.Fatalf("drained collection: count=%d err=%v", count, err)
			}
			after, err := w.Diagnostics(cursor)
			if err != nil {
				t.Fatal(err)
			}
			if after.Confidence != workspacecore.ConfidenceAuthoritative ||
				after.CurrentCount != len(push.Findings) || len(after.New) != 0 ||
				len(after.Resolved) != 0 || len(after.Notices) != 0 {
				t.Fatalf("drained slots weakened current push proof: %+v", after)
			}
		})
	}
}
