package workspace

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Replay the captured Rust A/B causal sequence through the real ledger. The
// source and message distinguish rust-analyzer's pull from rustc's push.
func TestDiagnosticMixedStreamsCapturedRustAB(t *testing.T) {
	for _, pushFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("push_first_%t", pushFirst), func(t *testing.T) {
			w, _ := evidenceWorkspace(t)
			path := filepath.Join(w.Identity().Root, "main.go")
			before, err := w.Snapshot(path, ProviderLayer{})
			if err != nil {
				t.Fatal(err)
			}
			pulled := finding("expected i32, found &'static str")
			pulled.Code, pulled.Source = "E0308", "rust-analyzer"
			pushed := finding("mismatched types\nexpected `i32`, found `&str`")
			pushed.Code, pushed.Source = "E0308", "rustc"
			hint := pushed
			hint.Message, hint.Severity = "expected `i32` because of return type", 4
			pull := DiagnosticBatch{Kind: EvidencePull, ProviderID: "rust_analyzer#1", Producer: "rust_analyzer",
				Document: path, DocumentRevision: string(before.Revision), ExpectedVersion: diagnosticVersion(3),
				ResultID: "rust-analyzer", Selected: true, TransactionID: "edit_A", Findings: []DiagnosticFinding{pulled}}
			a, err := w.RecordDiagnosticEvidence(pull)
			if err != nil || len(a.New) != 1 {
				t.Fatalf("A: %+v %v", a, err)
			}
			push := pull
			push.Kind, push.Complete, push.DocumentVersion = EvidencePush, true, diagnosticVersion(3)
			push.Findings = []DiagnosticFinding{pushed, hint}
			baselines, err := w.RecordDiagnosticBaselines([]DiagnosticBatch{push})
			if err != nil || len(baselines) != 1 || len(baselines[0].Resolved) != 0 {
				t.Fatalf("baseline withdrew pull: %+v %v", baselines, err)
			}
			writeFile(t, path, "package main\n// hello, unchanged broken expression\n")
			post, err := w.Refresh(path, ProviderLayer{})
			if err != nil {
				t.Fatal(err)
			}
			push.DocumentVersion, push.ExpectedVersion = diagnosticVersion(4), diagnosticVersion(4)
			push.DocumentRevision, push.TransactionID = string(post.Revision), "edit_B"
			pull.ExpectedVersion, pull.DocumentRevision, pull.TransactionID = diagnosticVersion(4), string(post.Revision), "edit_B"
			batches := []DiagnosticBatch{pull, push, pull}
			if pushFirst {
				batches = []DiagnosticBatch{push, pull, push}
			}
			for i, batch := range batches {
				b, err := w.RecordDiagnosticEvidence(batch)
				if err != nil || len(b.Resolved) != 0 || len(b.New) != 0 || b.Confidence != ConfidenceProvisional {
					t.Fatalf("B observation %d falsely withdrew/reannounced/proved coverage: %+v %v", i, b, err)
				}
			}
			b, err := w.Diagnostics(a.Cursor)
			if err != nil || b.CurrentCount != 3 || b.StaleCount != 0 || len(b.Resolved) != 0 || b.Confidence != ConfidenceProvisional {
				t.Fatalf("B recovery: %+v %v", b, err)
			}
			for _, notice := range b.Notices {
				if notice.Kind == "new" || notice.Kind == "resolved" {
					t.Fatalf("B emitted false transition: %+v", notice)
				}
			}
			// C's push can withdraw only rustc. Ordinary empty pull is not
			// producer-version proof, even with a result ID and a local version.
			push.Findings, pull.Findings = nil, nil
			push.DocumentVersion, push.ExpectedVersion = diagnosticVersion(5), diagnosticVersion(5)
			c, err := w.RecordDiagnosticEvidence(push)
			if err != nil || len(c.Resolved) != 2 || c.CurrentCount != 1 || c.Confidence != ConfidenceProvisional {
				t.Fatalf("push-only withdrawal: %+v %v", c, err)
			}
			pull.Complete, pull.ExpectedVersion = true, diagnosticVersion(5)
			empty, err := w.RecordDiagnosticEvidence(pull)
			if err != nil || len(empty.Resolved) != 0 || empty.CurrentCount != 1 || empty.Confidence != ConfidenceProvisional {
				t.Fatalf("unversioned empty pull proved omission: %+v %v", empty, err)
			}
		})
	}
}

func TestDiagnosticMixedOriginsRetainMembershipAndWeakestLane(t *testing.T) {
	for _, stale := range []bool{false, true} {
		for _, kinds := range [][]DiagnosticEvidenceKind{nil, {EvidencePush, EvidencePull}, {EvidencePush, "future_origin"}} {
			t.Run(fmt.Sprintf("stale_%t/kinds_%v", stale, kinds), func(t *testing.T) {
				s, _ := newDiagnosticStore("", "mixed")
				batch := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test",
					Document: "main.go", DocumentRevision: "a", Complete: true, Selected: true,
					DocumentVersion: diagnosticVersion(1), ExpectedVersion: diagnosticVersion(1),
					Findings: []DiagnosticFinding{finding("same")}}
				first, err := s.record(batch)
				if err != nil {
					t.Fatal(err)
				}
				item := s.state.Items[first.New[0].ID]
				item.EvidenceKinds = kinds
				s.state.Items[item.ID] = item
				if stale {
					if _, err := s.documentChanged([]string{"main.go"}, "b", time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				batch.Findings = nil
				batch.DocumentRevision = "b"
				batch.DocumentVersion, batch.ExpectedVersion = diagnosticVersion(2), diagnosticVersion(2)
				report, err := s.record(batch)
				if err != nil || len(report.Resolved) != 0 || report.CurrentCount+report.StaleCount != 1 ||
					report.Confidence != ConfidenceProvisional {
					t.Fatalf("rejected withdrawal lost membership/coverage: %+v %v", report, err)
				}
				if got := s.state.Items[item.ID].Status; (got == DiagnosticStatusStale) != stale {
					t.Fatalf("membership changed: %s", got)
				}
				// Refreshing a legacy item must not erase its unknown history.
				batch.Findings = []DiagnosticFinding{finding("same")}
				if _, err := s.record(batch); err != nil {
					t.Fatal(err)
				}
				batch.Findings = nil
				report, err = s.record(batch)
				if err != nil || len(report.Resolved) != 0 || report.CurrentCount != 1 {
					t.Fatalf("refresh laundered origin history: %+v %v", report, err)
				}
			})
		}
	}
}

func TestDiagnosticSharedIDDoesNotRefreshIndependentLane(t *testing.T) {
	s, _ := newDiagnosticStore("", "shared")
	push := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test",
		Document: "main.go", DocumentRevision: "a", Complete: true, Selected: true,
		DocumentVersion: diagnosticVersion(1), ExpectedVersion: diagnosticVersion(1),
		Findings: []DiagnosticFinding{finding("same")}}
	first, _ := s.record(push)
	pull := push
	pull.Kind, pull.DocumentVersion, pull.Complete = EvidencePull, nil, false
	if _, err := s.record(pull); err != nil {
		t.Fatal(err)
	}
	if _, err := s.documentChanged([]string{"main.go"}, "b", time.Now()); err != nil {
		t.Fatal(err)
	}
	push.DocumentRevision = "b"
	push.DocumentVersion, push.ExpectedVersion = diagnosticVersion(2), diagnosticVersion(2)
	refreshed, err := s.record(push)
	if err != nil || len(refreshed.New) != 0 || refreshed.CurrentCount != 1 || refreshed.StaleCount != 0 ||
		refreshed.Confidence != ConfidenceProvisional {
		t.Fatalf("shared ID hid stale pull lane: %+v %v", refreshed, err)
	}
	push.Findings = nil
	omitted, _ := s.record(push)
	if len(omitted.Resolved) != 0 || omitted.CurrentCount != 1 {
		t.Fatalf("push withdrew historical pull origin: %+v", omitted)
	}
	query, _ := s.query(first.Cursor)
	if query.Confidence != ConfidenceProvisional || query.Coverage["edited_documents"].State == "complete" {
		t.Fatalf("query overstated independent coverage: %+v", query)
	}
}

func TestDiagnosticHistoricalKindsSurviveRestartRecurrenceAndPruning(t *testing.T) {
	s, err := newDiagnosticStore(t.TempDir(), "history")
	if err != nil {
		t.Fatal(err)
	}
	batch := DiagnosticBatch{Kind: EvidencePull, ProviderID: "test#1", Producer: "test",
		Document: "main.go", Selected: true, Findings: []DiagnosticFinding{finding("recurs")}}
	first, _ := s.record(batch)
	id := first.New[0].ID
	// Model a retained resolution from a pre-fix ledger. Recurrence must keep
	// the old pull origin; it cannot treat the latest push as the whole history.
	item := s.state.Items[id]
	item.Status, item.ResolvedAt = DiagnosticStatusResolved, time.Now()
	s.state.Items[id] = item
	delete(s.state.Active, "test#1\x00main.go")
	batch.Kind, batch.Complete = EvidencePush, true
	batch.DocumentVersion, batch.ExpectedVersion = diagnosticVersion(4), diagnosticVersion(4)
	for i := 0; i < maxDiagnosticEvidenceReferences+4; i++ {
		batch.TransactionID = fmt.Sprintf("refresh_%d", i)
		if _, err := s.record(batch); err != nil {
			t.Fatal(err)
		}
	}
	// Move the pull lane's bounded references past the original positive too.
	emptyPull := batch
	emptyPull.Kind, emptyPull.DocumentVersion, emptyPull.Complete = EvidencePull, nil, false
	emptyPull.Findings = nil
	for i := 0; i < maxDiagnosticEvidenceReferences+4; i++ {
		emptyPull.ResultID = fmt.Sprintf("empty_%d", i)
		if _, err := s.record(emptyPull); err != nil {
			t.Fatal(err)
		}
	}
	// Expire the no-longer-referenced original pull payload without changing
	// its append-only origin history or lane confidence.
	s.pruneLocked(time.Now().Add(2 * diagnosticRetentionWindow))
	if _, retained := s.state.Evidence[first.EvidenceIDs[0]]; retained {
		t.Fatal("fixture did not prune the original pull proof")
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newDiagnosticStore(filepath.Dir(filepath.Dir(s.path)), "history")
	if err != nil {
		t.Fatal(err)
	}
	batch.Findings = nil
	report, err := reopened.record(batch)
	if err != nil || len(report.Resolved) != 0 || report.CurrentCount != 1 || report.Confidence != ConfidenceProvisional {
		t.Fatalf("restart/pruning discarded historical origin: %+v %v", report, err)
	}
	if got := reopened.state.Items[id]; len(got.EvidenceIDs) > maxDiagnosticEvidenceReferences || len(got.EvidenceKinds) != 2 {
		t.Fatalf("history/bounds changed: %+v", got)
	}
}

func TestDiagnosticLegacyCoverageCannotFabricateStreamProof(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			dir := t.TempDir()
			s, _ := newDiagnosticStore(dir, "legacy")
			batch := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test", Document: "main.go",
				Selected: true, Complete: true, DocumentVersion: diagnosticVersion(1), ExpectedVersion: diagnosticVersion(1),
				Findings: []DiagnosticFinding{finding("legacy unresolved"), finding("legacy resolved")}}
			first, _ := s.record(batch)
			resolved := s.state.Items[first.New[1].ID]
			resolved.Status = DiagnosticStatusResolved
			s.state.Items[resolved.ID] = resolved
			s.state.Active["test#1\x00main.go"] = []string{first.New[0].ID}
			item := s.state.Items[first.New[0].ID]
			item.EvidenceKinds = nil
			s.state.Items[item.ID] = item
			s.state.Version = version
			s.state.ProviderDimensions = map[string]DiagnosticDimension{
				"edited_documents\x00test#1\x00main.go": first.Coverage["edited_documents"],
			}
			if err := s.save(); err != nil {
				t.Fatal(err)
			}
			reopened, err := newDiagnosticStore(dir, "legacy")
			if err != nil {
				t.Fatal(err)
			}
			query, _ := reopened.query("")
			if query.Confidence != ConfidenceProvisional || reopened.state.Items[resolved.ID].Status != DiagnosticStatusResolved {
				t.Fatalf("migration fabricated proof or rewrote resolution: %+v", query)
			}
			batch.Findings = nil
			report, _ := reopened.record(batch)
			if len(report.Resolved) != 0 || report.CurrentCount != 1 || report.Confidence != ConfidenceProvisional {
				t.Fatalf("legacy omission: %+v", report)
			}
			var persisted diagnosticState
			bytes, err := json.Marshal(reopened.state)
			if err != nil || json.Unmarshal(bytes, &persisted) != nil || persisted.Version != diagnosticStateVersion {
				t.Fatalf("version not persisted: %v", err)
			}
		})
	}
}

func TestDiagnosticPushDomainResolvesMixedSourcesAcrossVersions(t *testing.T) {
	s, _ := newDiagnosticStore("", "push_domain")
	firstSource, secondSource := finding("compiler error"), finding("adapter hint")
	firstSource.Source, secondSource.Source = "rustc", "another push source"
	push := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test", Document: "main.go",
		DocumentRevision: "a", Selected: true, Complete: true,
		DocumentVersion: diagnosticVersion(3), ExpectedVersion: diagnosticVersion(3),
		Findings: []DiagnosticFinding{firstSource, secondSource}}
	first, err := s.record(push)
	if err != nil || len(first.New) != 2 {
		t.Fatalf("push fixture: %+v %v", first, err)
	}
	baseline := push
	baseline.Baseline, baseline.Findings = true, nil
	seed, err := s.record(baseline)
	if err != nil || len(seed.Resolved) != 0 || seed.CurrentCount != 2 {
		t.Fatalf("baseline omission withdrew findings: %+v %v", seed, err)
	}
	if _, err := s.documentChanged([]string{"main.go"}, "b", time.Now()); err != nil {
		t.Fatal(err)
	}
	push.DocumentRevision = "b"
	push.DocumentVersion, push.ExpectedVersion, push.Findings = diagnosticVersion(4), diagnosticVersion(4), nil
	clean, err := s.record(push)
	if err != nil || len(clean.Resolved) != 2 || clean.CurrentCount != 0 || clean.StaleCount != 0 ||
		clean.Confidence != ConfidenceAuthoritative {
		t.Fatalf("complete push did not cover its mixed Sources: %+v %v", clean, err)
	}
}
