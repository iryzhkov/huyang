package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type declarationSectioner struct{}

func (declarationSectioner) Sections(_ string, content []byte) ([]Section, error) {
	var sections []Section
	offset := 0
	for _, block := range strings.SplitAfter(string(content), "---\n") {
		body := strings.TrimSuffix(block, "---\n")
		trimmed := strings.TrimLeft(body, "\n")
		leading := len(body) - len(trimmed)
		if trimmed == "" {
			offset += len(block)
			continue
		}
		first := strings.SplitN(trimmed, "\n", 2)[0]
		fields := strings.Fields(first)
		if len(fields) >= 2 && fields[0] == "func" {
			name := strings.SplitN(fields[1], "(", 2)[0]
			start := offset + leading
			sections = append(sections, Section{Name: name, Kind: "function", ByteStart: start, ByteEnd: offset + len(body)})
		}
		offset += len(block)
	}
	return sections, nil
}

func newHandleWorkspace(t *testing.T, files map[string]string) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		writeFile(t, filepath.Join(root, name), content)
	}
	workspace, err := Open(OpenOptions{
		Kind: KindProject, Root: root, StateDir: t.TempDir(), Sectioner: declarationSectioner{},
		Limits: Limits{MaxFiles: 100, MaxBytes: 1 << 20, MaxDepth: 8, MaxMatches: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, root
}

func onlySymbol(t *testing.T, workspace *Workspace, query string) HandleRecord {
	t.Helper()
	records, coverage, err := workspace.FindSymbols(query)
	if err != nil {
		t.Fatal(err)
	}
	if !coverage.Complete || len(records) != 1 {
		t.Fatalf("FindSymbols(%q) = %d, coverage=%+v", query, len(records), coverage)
	}
	return records[0]
}

func TestSymbolHandleExactFormattingMoveAndConflicts(t *testing.T) {
	workspace, root := newHandleWorkspace(t, map[string]string{"a.go": "func foo()\nbody\n---\n"})
	record := onlySymbol(t, workspace, "foo")

	exact, err := workspace.ResolveHandle(record.Handle)
	if err != nil || exact.Status != ResolutionExact {
		t.Fatalf("exact resolution = %+v, %v", exact, err)
	}

	writeFile(t, filepath.Join(root, "a.go"), "\nfunc   foo()\nbody\n---\n")
	relocated, err := workspace.ResolveHandle(record.Handle)
	if err != nil || relocated.Status != ResolutionRelocated || relocated.Code != ConflictFormatOnlyRelocation {
		t.Fatalf("formatted resolution = %+v, %v", relocated, err)
	}

	workspace, root = newHandleWorkspace(t, map[string]string{"a.go": "func foo()\nbody\n---\n"})
	record = onlySymbol(t, workspace, "foo")
	if err := os.Rename(filepath.Join(root, "a.go"), filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	moved, err := workspace.ResolveHandle(record.Handle)
	if err != nil || moved.Status != ResolutionRelocated || moved.Current == nil || moved.Current.Path != "b.go" {
		t.Fatalf("moved resolution = %+v, %v", moved, err)
	}

	workspace, root = newHandleWorkspace(t, map[string]string{"a.go": "func foo()\nbody\n---\n"})
	record = onlySymbol(t, workspace, "foo")
	writeFile(t, filepath.Join(root, "a.go"), "func foo(x int)\nbody\n---\n")
	changed, err := workspace.ResolveHandle(record.Handle)
	if err != nil || changed.Status != ResolutionConflicted || changed.Code != ConflictSymbolSignatureChanged {
		t.Fatalf("signature resolution = %+v, %v", changed, err)
	}

	workspace, root = newHandleWorkspace(t, map[string]string{"a.go": "func foo()\nbody\n---\n"})
	record = onlySymbol(t, workspace, "foo")
	writeFile(t, filepath.Join(root, "a.go"), "func foo()\nbody\n---\nfunc foo()\nbody\n---\n")
	ambiguous, err := workspace.ResolveHandle(record.Handle)
	if err != nil || ambiguous.Status != ResolutionConflicted || ambiguous.Code != ConflictSymbolAmbiguous || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous resolution = %+v, %v", ambiguous, err)
	}

	workspace, root = newHandleWorkspace(t, map[string]string{"a.go": "func foo()\nbody\n---\n"})
	record = onlySymbol(t, workspace, "foo")
	writeFile(t, filepath.Join(root, "a.go"), "func bar()\nbody\n---\n")
	renamed, err := workspace.ResolveHandle(record.Handle)
	if err != nil || renamed.Status != ResolutionConflicted || renamed.Code != ConflictTargetDeleted {
		t.Fatalf("renamed resolution = %+v, %v", renamed, err)
	}
}

func TestSymbolHandleDeleteRecreateTTLAndEpochInvalidation(t *testing.T) {
	workspace, root := newHandleWorkspace(t, map[string]string{"a.go": "func foo()\nbody\n---\n"})
	record := onlySymbol(t, workspace, "foo")
	replacement := filepath.Join(root, "replacement")
	writeFile(t, replacement, "func foo()\nbody\n---\n")
	if err := os.Rename(replacement, filepath.Join(root, "a.go")); err != nil {
		t.Fatal(err)
	}
	recreated, err := workspace.ResolveHandle(record.Handle)
	if err != nil || recreated.Status != ResolutionConflicted || recreated.Code != ConflictTargetDeleted {
		t.Fatalf("delete/recreate resolution = %+v, %v", recreated, err)
	}

	workspace, _ = newHandleWorkspace(t, map[string]string{"a.go": "func foo()\nbody\n---\n"})
	workspace.SetHandleTTL(-time.Second)
	expired := onlySymbol(t, workspace, "foo")
	if _, err := workspace.ResolveHandle(expired.Handle); err == nil || err.Error() != "handle expired" {
		t.Fatalf("expired handle error = %v", err)
	}

	workspace.SetHandleTTL(time.Hour)
	epochBound := onlySymbol(t, workspace, "foo")
	workspace.SyncProviderEpoch(workspace.Identity().Epoch + 1)
	epoch, err := workspace.ResolveHandle(epochBound.Handle)
	if err != nil || epoch.Status != ResolutionConflicted || epoch.Code != ConflictWorkspaceEpoch {
		t.Fatalf("epoch resolution = %+v, %v", epoch, err)
	}
}

func TestRangeHandleRelocationAndResultSetLineage(t *testing.T) {
	workspace, root := newHandleWorkspace(t, map[string]string{
		"a.txt": "alpha beta\n", "b.txt": "alpha gamma\n",
	})
	result, err := workspace.Search(SearchRequest{Query: "alpha", Mode: SearchLiteral})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResultSet == nil || !result.ResultSet.Complete || !result.ResultSet.NonOverlapping ||
		!result.ResultSet.AllMatchesEligible || result.ResultSet.MatchCount != 2 {
		t.Fatalf("initial result set = %+v", result.ResultSet)
	}
	if len(result.Hits) != 2 || result.Hits[0].MatchHandle == nil {
		t.Fatalf("search handles = %+v", result.Hits)
	}
	if matches, resolveErr := workspace.ResolveAllMatches(result.ResultSet.Handle); resolveErr != nil || len(matches) != 2 {
		t.Fatalf("all-match resolution = %d, %v", len(matches), resolveErr)
	}
	overlappingHits := append([]SearchHit(nil), result.Hits...)
	overlappingHits[1].Path = overlappingHits[0].Path
	overlappingHits[1].ByteStart = overlappingHits[0].ByteStart + 1
	overlappingHits[1].ByteEnd = overlappingHits[0].ByteEnd
	overlap, err := workspace.FreezeSearch(SearchResult{
		Workspace: workspace.Identity(), Hits: overlappingHits, Coverage: result.Coverage,
		DocumentRevisions: result.DocumentRevisions, SourceFiles: result.SourceFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	if overlap.NonOverlapping || overlap.AllMatchesEligible {
		t.Fatalf("overlap eligibility = %+v", overlap)
	}
	if _, err := workspace.ResolveAllMatches(overlap.Handle); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("overlap resolution error = %v", err)
	}
	child, err := workspace.RefineResultSet(result.ResultSet.Handle, ResultRefinement{Path: "a.txt", MatchLiteral: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if child.Parent != result.ResultSet.Handle || child.Retained != 1 || child.Eliminated != 1 ||
		!child.Complete || !child.AllMatchesEligible {
		t.Fatalf("refined set = %+v", child)
	}

	beta, err := workspace.Search(SearchRequest{Query: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	handle := beta.Hits[0].MatchHandle.Handle
	writeFile(t, filepath.Join(root, "a.txt"), "prefix alpha beta\n")
	resolution, err := workspace.ResolveHandle(handle)
	if err != nil || resolution.Status != ResolutionRelocated {
		t.Fatalf("range relocation = %+v, %v", resolution, err)
	}
	rangeValue, err := resolution.RangeHandle()
	if err != nil {
		t.Fatal(err)
	}
	change, err := workspace.PreviewReplace(workspace.Identity().ID, rangeValue, []byte("delta"))
	if err != nil || string(change.Diff.After) != "prefix alpha delta\n" {
		t.Fatalf("relocated preview = %+v, %v", change, err)
	}

	if _, err := workspace.InspectResultSet(result.ResultSet.Handle); err == nil {
		t.Fatal("stale result set remained valid after source change")
	} else {
		var conflict *Conflict
		if !errors.As(err, &conflict) || conflict.Code != ConflictDocumentChanged {
			t.Fatalf("stale result set error = %v", err)
		}
	}

	historical, err := workspace.FreezeHistoricalResultSet(4, 2, Coverage{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if historical.Kind != ResultSetHistorical || historical.AllMatchesEligible {
		t.Fatalf("historical set = %+v", historical)
	}
	if _, err := workspace.RefineResultSet(historical.Handle, ResultRefinement{Path: "a"}); err == nil {
		t.Fatal("historical result set was accepted as current source")
	}
}

func TestResultSetRejectsCapsNewFilesAndEpochChanges(t *testing.T) {
	workspace, root := newHandleWorkspace(t, map[string]string{"a.txt": "alpha alpha\n"})
	workspace.limits.MaxMatches = 1
	capped, err := workspace.Search(SearchRequest{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if capped.ResultSet == nil || capped.ResultSet.Complete || capped.ResultSet.AllMatchesEligible {
		t.Fatalf("capped result set = %+v", capped.ResultSet)
	}
	if _, err := workspace.ResolveAllMatches(capped.ResultSet.Handle); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("capped all-match error = %v", err)
	}

	workspace, root = newHandleWorkspace(t, map[string]string{"a.txt": "alpha\n"})
	complete, err := workspace.Search(SearchRequest{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "b.txt"), "alpha\n")
	if _, err := workspace.InspectResultSet(complete.ResultSet.Handle); err == nil {
		t.Fatal("new source file did not invalidate complete result set")
	}

	workspace, _ = newHandleWorkspace(t, map[string]string{"a.txt": "alpha\n"})
	complete, err = workspace.Search(SearchRequest{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	workspace.SyncProviderEpoch(workspace.Identity().Epoch + 1)
	_, err = workspace.InspectResultSet(complete.ResultSet.Handle)
	var conflict *Conflict
	if !errors.As(err, &conflict) || conflict.Code != ConflictWorkspaceEpoch {
		t.Fatalf("result-set epoch error = %v", err)
	}
}
