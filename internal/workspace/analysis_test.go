package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubContributor claims whatever it was given, so the merge rules can be
// tested without a language server.
type stubContributor struct {
	name    string
	version string
	claims  []AnalysisFact
	fail    error
	block   bool
}

func (c stubContributor) Name() string    { return c.name }
func (c stubContributor) Version() string { return c.version }

func (c stubContributor) Contribute(ctx context.Context, request AnalysisRequest, builder *AnalysisBuilder) error {
	if c.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if c.fail != nil {
		return c.fail
	}
	for _, fact := range c.claims {
		builder.Node(AnalysisNode{ID: fact.From, Kind: NodeFile, Path: fact.From})
		builder.Node(AnalysisNode{ID: fact.To, Kind: NodeFile, Path: fact.To})
		producer := FactProducer{Name: c.name, Confidence: string(ConfidenceProvisional)}
		if len(fact.Producers) > 0 {
			producer = fact.Producers[0]
			producer.Name = c.name
		}
		builder.Fact(fact.From, fact.To, fact.Kind, producer)
	}
	return nil
}

func analysisKey() AnalysisKey {
	return AnalysisKey{WorkspaceID: "ws_test", Epoch: 1, Revision: "wsrev_3", Profile: "impact/v1", ConfigHash: "cfg1"}
}

// A snapshot's identity is its whole input, and nothing else. The same inputs
// give the same id however the producers were ordered, and every input that
// could change an answer changes it.
func TestAnalysisIdentityIsDeterministicAndCoversEveryInput(t *testing.T) {
	base := analysisKey()
	base.Producers = []ProducerVersion{{Name: "b", Version: "2"}, {Name: "a", Version: "1"}}
	reordered := analysisKey()
	reordered.Producers = []ProducerVersion{{Name: "a", Version: "1"}, {Name: "b", Version: "2"}}
	if base.ID() != reordered.ID() {
		t.Fatalf("producer order changed the identity: %s vs %s", base.ID(), reordered.ID())
	}
	for name, mutate := range map[string]func(*AnalysisKey){
		"revision":     func(k *AnalysisKey) { k.Revision = "wsrev_4" },
		"epoch":        func(k *AnalysisKey) { k.Epoch = 2 },
		"workspace":    func(k *AnalysisKey) { k.WorkspaceID = "ws_other" },
		"profile":      func(k *AnalysisKey) { k.Profile = "impact/v2" },
		"config":       func(k *AnalysisKey) { k.ConfigHash = "cfg2" },
		"producer":     func(k *AnalysisKey) { k.Producers[0].Version = "9" },
		"new_producer": func(k *AnalysisKey) { k.Producers = append(k.Producers, ProducerVersion{Name: "c", Version: "1"}) },
	} {
		changed := base
		changed.Producers = append([]ProducerVersion(nil), base.Producers...)
		mutate(&changed)
		if changed.ID() == base.ID() {
			t.Fatalf("a changed %s did not change the snapshot identity", name)
		}
	}
	// A snapshot survives a round trip through JSON unchanged, because
	// everything built on it names the snapshot it read.
	snapshot, _ := BuildAnalysisSnapshot(context.Background(), AnalysisRequest{Key: base},
		stubContributor{name: "a", version: "1", claims: []AnalysisFact{{From: "x", To: "y", Kind: EdgeImports}}})
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AnalysisSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != snapshot.ID || len(decoded.Facts) != len(snapshot.Facts) {
		t.Fatalf("snapshot did not survive serialization: %s vs %s", decoded.ID, snapshot.ID)
	}
}

// Two contributors that say the same thing leave one fact with both names on
// it; two that say different things leave both facts and a conflict. Nothing
// a contributor said is ever dropped.
func TestAnalysisMergesDuplicatesAndKeepsDisagreements(t *testing.T) {
	agreeing := []AnalysisContributor{
		stubContributor{name: "native_imports", version: "1", claims: []AnalysisFact{{From: "a.go", To: "b.go", Kind: EdgeImports}}},
		stubContributor{name: "embedded_nvim", version: "1", claims: []AnalysisFact{{From: "a.go", To: "b.go", Kind: EdgeImports,
			Producers: []FactProducer{{Confidence: string(ConfidenceAuthoritative)}}}}},
	}
	snapshot, err := BuildAnalysisSnapshot(context.Background(), AnalysisRequest{Key: analysisKey()}, agreeing...)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Facts) != 1 || len(snapshot.Facts[0].Producers) != 2 {
		t.Fatalf("agreeing contributors did not merge: %#v", snapshot.Facts)
	}
	if len(snapshot.Conflicts) != 0 {
		t.Fatalf("agreement was recorded as a conflict: %#v", snapshot.Conflicts)
	}
	// The strongest claim decides how the snapshot labels its own coverage.
	if snapshot.Coverage.Semantic != "semantic_provider" {
		t.Fatalf("coverage label = %q", snapshot.Coverage.Semantic)
	}

	disagreeing := []AnalysisContributor{
		stubContributor{name: "native_imports", version: "1", claims: []AnalysisFact{{From: "a.go", To: "b.go", Kind: EdgeImports}}},
		stubContributor{name: "embedded_nvim", version: "1", claims: []AnalysisFact{{From: "a.go", To: "b.go", Kind: EdgeCalls}}},
	}
	snapshot, err = BuildAnalysisSnapshot(context.Background(), AnalysisRequest{Key: analysisKey()}, disagreeing...)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Facts) != 2 {
		t.Fatalf("a disagreement lost a fact: %#v", snapshot.Facts)
	}
	if len(snapshot.Conflicts) != 1 || len(snapshot.Conflicts[0].Kinds) != 2 {
		t.Fatalf("the disagreement was not recorded: %#v", snapshot.Conflicts)
	}
}

// A contributor that fails is a gap in coverage, not a failed build: the
// snapshot says what it could not see and the rest of it stays usable.
func TestAnalysisRecordsAFailedContributorAsIncompleteCoverage(t *testing.T) {
	snapshot, err := BuildAnalysisSnapshot(context.Background(), AnalysisRequest{Key: analysisKey()},
		stubContributor{name: "native_imports", version: "1", claims: []AnalysisFact{{From: "a.go", To: "b.go", Kind: EdgeImports}}},
		stubContributor{name: "embedded_nvim", version: "1", fail: errors.New("no language server attached")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Facts) != 1 {
		t.Fatalf("the working contributor was lost: %#v", snapshot.Facts)
	}
	if snapshot.Coverage.Complete {
		t.Fatal("a snapshot missing a contributor called itself complete")
	}
	if len(snapshot.Coverage.Skipped) != 1 {
		t.Fatalf("coverage does not name the gap: %#v", snapshot.Coverage.Skipped)
	}
}

// Caps and cancellation both leave a snapshot that says it is partial.
func TestAnalysisRespectsCapsAndCancellation(t *testing.T) {
	claims := []AnalysisFact{
		{From: "a.go", To: "b.go", Kind: EdgeImports},
		{From: "a.go", To: "c.go", Kind: EdgeImports},
		{From: "a.go", To: "d.go", Kind: EdgeImports},
	}
	capped, err := BuildAnalysisSnapshot(context.Background(),
		AnalysisRequest{Key: analysisKey(), Caps: AnalysisCaps{MaxFacts: 2, MaxNodes: 64, Timeout: time.Minute}},
		stubContributor{name: "native_imports", version: "1", claims: claims})
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Facts) != 2 || !capped.Coverage.Capped || capped.Coverage.Complete {
		t.Fatalf("the fact cap was not applied or not disclosed: %d facts, coverage %#v", len(capped.Facts), capped.Coverage)
	}

	cancelled, err := BuildAnalysisSnapshot(context.Background(),
		AnalysisRequest{Key: analysisKey(), Caps: AnalysisCaps{Timeout: 20 * time.Millisecond}},
		stubContributor{name: "slow", version: "1", block: true},
		stubContributor{name: "never_runs", version: "1", claims: claims})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Coverage.Complete || len(cancelled.Facts) != 0 {
		t.Fatalf("a cancelled build did not say so: %#v", cancelled)
	}
}

// A canonical revision and a prepared revision are different keys, so one can
// never be served for the other however similar the tree is.
func TestCanonicalAndPreparedSnapshotsNeverShareAnEntry(t *testing.T) {
	store := newAnalysisStore()
	canonical := analysisKey()
	prepared := analysisKey()
	prepared.Revision = "prep_9f1"
	if canonical.ID() == prepared.ID() {
		t.Fatal("a prepared revision shares the canonical snapshot identity")
	}
	store.put(AnalysisSnapshot{ID: canonical.ID(), Key: canonical})
	if _, found := store.get(prepared.ID()); found {
		t.Fatal("the prepared revision was served the canonical snapshot")
	}
	// The store is bounded, and the oldest entry goes first.
	for index := 0; index < maxAnalysisSnapshots+4; index++ {
		key := analysisKey()
		key.Revision = "wsrev_" + string(rune('a'+index))
		store.put(AnalysisSnapshot{ID: key.ID(), Key: key})
	}
	if len(store.entries) > maxAnalysisSnapshots {
		t.Fatalf("the snapshot store is unbounded: %d entries", len(store.entries))
	}
	if _, found := store.get(canonical.ID()); found {
		t.Fatal("the oldest snapshot was not evicted")
	}
}

// The native import reader keeps working, now as one contributor whose claims
// carry its name and a confidence that says what a regular expression is
// worth.
func TestNativeContributorProducesProvisionalFacts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/x\n\ngo 1.23\n")
	writeFile(t, filepath.Join(root, "ledger.go"), "package x\n\nfunc Total() int { return 7 }\n")
	writeFile(t, filepath.Join(root, "report.go"), "package x\n\nimport \"example.com/x/sub\"\n\nfunc Report() int { return Total() }\n")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "sub", "sub.go"), "package sub\n")

	snapshot, err := BuildAnalysisSnapshot(context.Background(),
		AnalysisRequest{Key: analysisKey(), Root: root, Changed: []string{"ledger.go"}},
		NativeImportContributor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) == 0 {
		t.Fatal("the native contributor found no files")
	}
	for _, fact := range snapshot.Facts {
		for _, producer := range fact.Producers {
			if producer.Name != "native_imports" || producer.Confidence != nativeConfidence {
				t.Fatalf("native fact is not attributed: %#v", producer)
			}
		}
	}
	if snapshot.Coverage.Semantic != "parser_sections" && snapshot.Coverage.Semantic != "text_only" {
		t.Fatalf("a snapshot with no language server claims %q", snapshot.Coverage.Semantic)
	}
	if names := snapshot.Producers(); len(names) != 1 || names[0] != "native_imports" {
		t.Fatalf("producers = %v", names)
	}
	// A contributor that ran and found nothing is still named, so "nobody
	// looked" and "somebody looked and saw no relation" stay different.
	quiet, err := BuildAnalysisSnapshot(context.Background(), AnalysisRequest{Key: analysisKey()},
		stubContributor{name: "embedded_nvim", version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if names := quiet.Producers(); len(names) != 1 || names[0] != "embedded_nvim" {
		t.Fatalf("a contributor that found nothing was forgotten: %v", names)
	}
}
