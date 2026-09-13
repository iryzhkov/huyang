package workspace

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// AnalysisBuilder collects what contributors claim. It is the only place a
// fact is merged, so the rules live in one readable spot: the same claim from
// two contributors keeps both producers, and two contributors who describe the
// same pair of nodes differently keep both facts and leave a conflict behind.
type AnalysisBuilder struct {
	mu        sync.Mutex
	caps      AnalysisCaps
	nodes     map[string]AnalysisNode
	facts     map[string]*AnalysisFact
	skipped   []string
	capped    bool
	current   string
	coverages []Coverage
}

func newAnalysisBuilder(caps AnalysisCaps) *AnalysisBuilder {
	return &AnalysisBuilder{
		caps:  caps.withDefaults(),
		nodes: map[string]AnalysisNode{},
		facts: map[string]*AnalysisFact{},
	}
}

// Node records a node. A later contributor that knows more about what a file
// is - that it is a test, generated or configuration rather than just a file
// - refines it; nothing else about a known node is overwritten, because the
// first writer is the one that had the file in hand.
func (b *AnalysisBuilder) Node(node AnalysisNode) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if node.ID == "" {
		node.ID = NodeID(node.Kind, node.Path, node.NamePath)
	}
	if known, exists := b.nodes[node.ID]; exists {
		if known.Kind == NodeFile && node.Kind != NodeFile && node.Kind != "" {
			known.Kind = node.Kind
			b.nodes[node.ID] = known
		}
		return
	}
	if len(b.nodes) >= b.caps.MaxNodes {
		b.capped = true
		return
	}
	b.nodes[node.ID] = node
}

// Fact records one contributor's claim. Repeating a claim adds a producer;
// claiming a different relation between the same two nodes adds a fact and a
// conflict.
func (b *AnalysisBuilder) Fact(from, to string, kind AnalysisEdgeKind, producer FactProducer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if producer.Name == "" {
		producer.Name = b.current
	}
	key := from + "\x00" + to + "\x00" + string(kind)
	if existing, known := b.facts[key]; known {
		existing.Producers = mergeProducers(existing.Producers, producer)
		return
	}
	if len(b.facts) >= b.caps.MaxFacts {
		b.capped = true
		return
	}
	b.facts[key] = &AnalysisFact{From: from, To: to, Kind: kind, Producers: []FactProducer{producer}}
}

// Skipped records something a contributor could not read, which is what makes
// the difference between "nothing depends on this" and "I could not tell".
func (b *AnalysisBuilder) Skipped(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.skipped = append(b.skipped, reason)
}

// Covered records what one contributor managed to read.
func (b *AnalysisBuilder) Covered(coverage Coverage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.coverages = append(b.coverages, coverage)
}

// mergeProducers keeps one entry per producer name, preferring the more
// confident claim, so a contributor that ran twice does not look like two.
func mergeProducers(existing []FactProducer, candidate FactProducer) []FactProducer {
	for index, producer := range existing {
		if producer.Name != candidate.Name {
			continue
		}
		if confidenceRank(DiagnosticConfidence(candidate.Confidence)) > confidenceRank(DiagnosticConfidence(producer.Confidence)) {
			existing[index] = candidate
		}
		return existing
	}
	return append(existing, candidate)
}

// finish freezes the builder into a snapshot: facts in a stable order, the
// disagreements named, and coverage that is complete only when nothing was
// capped and nobody skipped anything.
func (b *AnalysisBuilder) finish(key AnalysisKey) AnalysisSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := AnalysisSnapshot{ID: key.ID(), Key: key, BuiltAt: time.Now().UTC()}
	for _, node := range b.nodes {
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	sort.Slice(snapshot.Nodes, func(i, j int) bool { return snapshot.Nodes[i].ID < snapshot.Nodes[j].ID })
	for _, fact := range b.facts {
		sort.Slice(fact.Producers, func(i, j int) bool { return fact.Producers[i].Name < fact.Producers[j].Name })
		snapshot.Facts = append(snapshot.Facts, *fact)
	}
	sort.Slice(snapshot.Facts, func(i, j int) bool {
		left, right := snapshot.Facts[i], snapshot.Facts[j]
		if left.From != right.From {
			return left.From < right.From
		}
		if left.To != right.To {
			return left.To < right.To
		}
		return left.Kind < right.Kind
	})
	snapshot.Conflicts = disagreements(snapshot.Facts)
	snapshot.Coverage = Coverage{
		Complete: !b.capped && len(b.skipped) == 0,
		Capped:   b.capped,
		Skipped:  uniqueSorted(b.skipped),
		Semantic: semanticLabel(snapshot.Facts),
	}
	for _, coverage := range b.coverages {
		snapshot.Coverage.FilesConsidered += coverage.FilesConsidered
		snapshot.Coverage.FilesRead += coverage.FilesRead
		snapshot.Coverage.BytesRead += coverage.BytesRead
	}
	return snapshot
}

// disagreements finds node pairs described by more than one kind of relation
// where the claims come from different contributors. One contributor saying a
// file both imports and calls another is not a disagreement; two contributors
// describing the same pair differently is.
func disagreements(facts []AnalysisFact) []AnalysisConflict {
	type claim struct {
		kinds     map[AnalysisEdgeKind]bool
		producers map[string]bool
	}
	pairs := map[string]*claim{}
	for _, fact := range facts {
		key := fact.From + "\x00" + fact.To
		if pairs[key] == nil {
			pairs[key] = &claim{kinds: map[AnalysisEdgeKind]bool{}, producers: map[string]bool{}}
		}
		pairs[key].kinds[fact.Kind] = true
		for _, producer := range fact.Producers {
			pairs[key].producers[producer.Name] = true
		}
	}
	var conflicts []AnalysisConflict
	for key, value := range pairs {
		if len(value.kinds) < 2 || len(value.producers) < 2 {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		kinds := make([]AnalysisEdgeKind, 0, len(value.kinds))
		for kind := range value.kinds {
			kinds = append(kinds, kind)
		}
		sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
		conflicts = append(conflicts, AnalysisConflict{From: parts[0], To: parts[1], Kinds: kinds})
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].From == conflicts[j].From {
			return conflicts[i].To < conflicts[j].To
		}
		return conflicts[i].From < conflicts[j].From
	})
	return conflicts
}

// semanticLabel says what the strongest contributor to these facts was, in
// the same vocabulary the rest of the workspace uses.
func semanticLabel(facts []AnalysisFact) string {
	label := "text_only"
	for _, fact := range facts {
		for _, producer := range fact.Producers {
			if producer.Confidence == string(ConfidenceAuthoritative) {
				return "semantic_provider"
			}
			label = "parser_sections"
		}
	}
	return label
}

// BuildAnalysisSnapshot runs every contributor against one revision. A
// contributor that fails is recorded as a gap in coverage rather than failing
// the build: a snapshot that names what it could not see is more useful than
// no snapshot at all, and the caller can see the difference.
func BuildAnalysisSnapshot(ctx context.Context, request AnalysisRequest, contributors ...AnalysisContributor) (AnalysisSnapshot, error) {
	caps := request.Caps.withDefaults()
	request.Caps = caps
	ctx, cancel := context.WithTimeout(ctx, caps.Timeout)
	defer cancel()
	builder := newAnalysisBuilder(caps)
	key := request.Key
	key.Producers = nil
	for _, contributor := range contributors {
		key.Producers = append(key.Producers, ProducerVersion{Name: contributor.Name(), Version: contributor.Version()})
	}
	for _, contributor := range contributors {
		if err := ctx.Err(); err != nil {
			builder.Skipped("analysis_cancelled")
			break
		}
		builder.current = contributor.Name()
		if err := contributor.Contribute(ctx, request, builder); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				builder.Skipped("analysis_cancelled")
				break
			}
			builder.Skipped(contributor.Name() + ": " + sanitizeText(err.Error(), 160))
		}
	}
	return builder.finish(key), nil
}

// analysisStore caches snapshots by identity. There is no invalidation,
// because there is nothing to invalidate: every input that could change an
// answer is already in the key, so a stale snapshot is unreachable rather
// than wrong. The store is bounded and evicts the oldest.
type analysisStore struct {
	mu      sync.Mutex
	entries map[string]AnalysisSnapshot
	order   []string
}

func newAnalysisStore() *analysisStore {
	return &analysisStore{entries: map[string]AnalysisSnapshot{}}
}

func (s *analysisStore) get(id string) (AnalysisSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.entries[id]
	return cloneExecution(snapshot), ok
}

func (s *analysisStore) put(snapshot AnalysisSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, known := s.entries[snapshot.ID]; known {
		return
	}
	s.entries[snapshot.ID] = cloneExecution(snapshot)
	s.order = append(s.order, snapshot.ID)
	for len(s.order) > maxAnalysisSnapshots {
		delete(s.entries, s.order[0])
		s.order = s.order[1:]
	}
}
