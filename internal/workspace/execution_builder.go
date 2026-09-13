package workspace

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metadata has its own reserve so a full graph can still disclose every
// bounded gap. Fact payloads cannot consume the bytes needed to say "capped".
const (
	maxExecutionMetadata      = 256 << 10
	maxExecutionGaps          = 64
	maxExecutionProvenance    = 64
	maxExecutionContributors  = 16
	maxExecutionIdentityBytes = 4096
)

type ExecutionBuilder struct {
	mu      sync.Mutex
	graph   ExecutionGraph
	nodes   map[string]ExecutionNode
	edges   map[string]ExecutionEdge
	bytes   int
	covered bool
}

func normalizeGraphBudget(b GraphBudget) (GraphBudget, error) {
	defaults := DefaultGraphBudget()
	values := []*int{&b.Paths, &b.Depth, &b.Nodes, &b.Edges, &b.Bytes, &b.FunctionNodes,
		&b.AnalysisMillis, &b.TraceEvents, &b.Values, &b.ValueBytes, &b.RetainedValueBytes,
		&b.Recursion, &b.CycleVisits}
	ceilings := []int{defaults.Paths, defaults.Depth, defaults.Nodes, defaults.Edges,
		defaults.Bytes, defaults.FunctionNodes, defaults.AnalysisMillis, defaults.TraceEvents,
		defaults.Values, defaults.ValueBytes, defaults.RetainedValueBytes, defaults.Recursion, defaults.CycleVisits}
	for i, value := range values {
		if *value < 0 || *value > ceilings[i] {
			return b, Codedf("graph_budget_invalid", "budget field %d exceeds its range", i)
		}
		if *value == 0 {
			*value = ceilings[i]
		}
	}
	if b.Bytes < 2*maxExecutionMetadata {
		return b, Codedf("graph_budget_invalid", "bytes must reserve at least %d bytes", 2*maxExecutionMetadata)
	}
	return b, nil
}

func newExecutionBuilder(revision string, budget GraphBudget) *ExecutionBuilder {
	return &ExecutionBuilder{
		graph: ExecutionGraph{Revision: revision, Budget: budget,
			Nodes: []ExecutionNode{}, Edges: []ExecutionEdge{}, Risks: []ExecutionRisk{},
			Coverage: ExecutionCoverage{Complete: true, Limits: []string{}, Gaps: []string{}}},
		nodes: map[string]ExecutionNode{}, edges: map[string]ExecutionEdge{},
	}
}

func (b *ExecutionBuilder) gap(reason string) {
	b.graph.Coverage.Complete = false
	if len(b.graph.Coverage.Gaps) < maxExecutionGaps {
		b.graph.Coverage.Gaps = uniqueSorted(append(b.graph.Coverage.Gaps, sanitizeText(reason, 160)))
	}
}

func (b *ExecutionBuilder) cap(limit string) {
	b.graph.Coverage.Capped = true
	b.graph.Coverage.Complete = false
	if len(b.graph.Coverage.Limits) < maxExecutionGaps {
		b.graph.Coverage.Limits = uniqueSorted(append(b.graph.Coverage.Limits, sanitizeText(limit, 80)))
	}
}

func (b *ExecutionBuilder) Covered(coverage ExecutionCoverage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.covered = true
	for _, limit := range coverage.Limits {
		b.cap(sanitizeText(limit, 80))
	}
	if !coverage.Complete {
		b.gap("contributor_incomplete")
	}
	for _, gap := range coverage.Gaps {
		b.gap(gap)
	}
	if coverage.Capped {
		b.cap("contributor")
	}
}

func (b *ExecutionBuilder) Risk(risk ExecutionRisk) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gap(risk.Kind)
	if len(b.graph.Risks) >= maxExecutionGaps {
		b.cap("risks")
		return
	}
	risk.Detail = sanitizeText(risk.Detail, 160)
	risk.Kind = sanitizeText(risk.Kind, 80)
	risk.Node = sanitizeText(risk.Node, 80)
	b.graph.Risks = append(b.graph.Risks, risk)
}

func executionJSON(value any) string {
	encoded, _ := json.Marshal(value) // These closed structs contain only JSON values.
	return string(encoded)
}

func mergeExecutionEvidence(existing, incoming []ExecutionEvidence) []ExecutionEvidence {
	byJSON := map[string]ExecutionEvidence{}
	for _, evidence := range append(append([]ExecutionEvidence(nil), existing...), incoming...) {
		evidence.SourceHandles = uniqueSorted(evidence.SourceHandles)
		evidence.EvidenceIDs = uniqueSorted(evidence.EvidenceIDs)
		byJSON[executionJSON(evidence)] = evidence
	}
	keys := make([]string, 0, len(byJSON))
	for key := range byJSON {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]ExecutionEvidence, 0, len(keys))
	for _, key := range keys {
		result = append(result, byJSON[key])
	}
	return result
}

func (b *ExecutionBuilder) validEvidence(evidence []ExecutionEvidence) error {
	if len(evidence) == 0 || len(evidence) > maxExecutionProvenance {
		return Codedf("graph_evidence_invalid", "facts require between 1 and %d provenance entries", maxExecutionProvenance)
	}
	for _, fact := range evidence {
		if !fact.Coverage.Complete || fact.Confidence == "unknown" || fact.Classification != "static" {
			b.gap("fact_coverage_incomplete")
		}
		if fact.Coverage.Capped || len(fact.Coverage.Limits) > 0 {
			b.cap("fact_coverage")
		}
		for _, gap := range fact.Coverage.Gaps {
			b.gap(gap)
		}
		if fact.Revision != b.graph.Revision || fact.Producer.Name == "" || fact.Producer.Version == "" {
			return Codedf("graph_evidence_invalid", "fact revision or producer identity is missing or mismatched")
		}
		if fact.Classification != "static" && fact.Classification != "observed" && fact.Classification != "inferred" {
			return Codedf("graph_evidence_invalid", "unknown fact classification")
		}
		for _, handle := range fact.SourceHandles {
			if strings.ContainsAny(handle, "/\\") {
				return Codedf("graph_source_invalid", "source handles must be opaque, not paths")
			}
		}
	}
	return nil
}

func (b *ExecutionBuilder) admit(before, after any) bool {
	delta := len(executionJSON(after)) + 1
	if before != nil {
		delta -= len(executionJSON(before)) + 1
	}
	if b.bytes+delta > b.graph.Budget.Bytes-maxExecutionMetadata {
		b.cap("bytes")
		return false
	}
	b.bytes += delta
	return true
}

// Node refuses conflicting identities instead of redirecting existing edges.
func (b *ExecutionBuilder) Node(node ExecutionNode) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if node.Kind == "external" || node.Kind == "unresolved" {
		b.gap(node.Kind + "_target")
	}
	if node.ID == "" || strings.HasPrefix(node.Path, "/") || path.Clean(node.Path) == ".." || strings.HasPrefix(path.Clean(node.Path), "../") {
		b.gap("invalid_node")
		return Codedf("graph_source_invalid", "node identity or source path is invalid")
	}
	if err := b.validEvidence(node.Evidence); err != nil {
		b.gap("invalid_evidence")
		return err
	}
	previous, exists := b.nodes[node.ID]
	if exists {
		oldCore, newCore := previous, node
		oldCore.Evidence, newCore.Evidence = nil, nil
		if executionJSON(oldCore) != executionJSON(newCore) {
			b.gap("node_conflict")
			return Codedf("graph_node_conflict", "node %s has conflicting locations", node.ID)
		}
	} else if len(b.nodes) >= b.graph.Budget.Nodes {
		b.cap("nodes")
		return nil
	}
	node.Evidence = mergeExecutionEvidence(previous.Evidence, node.Evidence)
	if len(node.Evidence) > maxExecutionProvenance {
		b.cap("provenance")
		return nil
	}
	var before any
	if exists {
		before = previous
	}
	if b.admit(before, node) {
		b.nodes[node.ID] = cloneExecution(node)
	}
	return nil
}

// Edge identity includes condition and kind. Disagreeing claims stay separate;
// only exactly equal structural facts merge their provenance.
func (b *ExecutionBuilder) Edge(edge ExecutionEdge) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.HasPrefix(edge.SitePath, "/") || path.Clean(edge.SitePath) == ".." || strings.HasPrefix(path.Clean(edge.SitePath), "../") {
		b.gap("invalid_call_site")
		return Codedf("graph_source_invalid", "call-site paths must stay workspace-relative")
	}
	if err := b.validEvidence(edge.Evidence); err != nil {
		b.gap("invalid_evidence")
		return err
	}
	if _, ok := b.nodes[edge.From]; !ok {
		b.gap("missing_endpoint")
		return nil
	}
	if _, ok := b.nodes[edge.To]; !ok {
		b.gap("missing_endpoint")
		return nil
	}
	core := edge
	core.ID, core.Evidence = "", nil
	edge.ID = "edge_" + hashBytes([]byte(executionJSON(core)))[:32]
	previous, exists := b.edges[edge.ID]
	if !exists && len(b.edges) >= b.graph.Budget.Edges {
		b.cap("edges")
		return nil
	}
	edge.Evidence = mergeExecutionEvidence(previous.Evidence, edge.Evidence)
	if len(edge.Evidence) > maxExecutionProvenance {
		b.cap("provenance")
		return nil
	}
	var before any
	if exists {
		before = previous
	}
	if b.admit(before, edge) {
		b.edges[edge.ID] = cloneExecution(edge)
	}
	return nil
}

func cloneExecution[T any](value T) T {
	var copy T
	_ = json.Unmarshal([]byte(executionJSON(value)), &copy)
	return copy
}

func (b *ExecutionBuilder) finish(key AnalysisKey) AnalysisSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	graph := b.graph
	graph.ID = "graph_" + strings.TrimPrefix(key.ID(), "snap_")
	for _, node := range b.nodes {
		graph.Nodes = append(graph.Nodes, node)
	}
	for _, edge := range b.edges {
		graph.Edges = append(graph.Edges, edge)
	}
	sort.Slice(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].ID < graph.Nodes[j].ID })
	sort.Slice(graph.Edges, func(i, j int) bool { return graph.Edges[i].ID < graph.Edges[j].ID })
	sort.Slice(graph.Risks, func(i, j int) bool { return executionJSON(graph.Risks[i]) < executionJSON(graph.Risks[j]) })
	return cloneExecution(AnalysisSnapshot{ID: key.ID(), Key: key, Execution: &graph,
		Coverage: Coverage{Complete: graph.Coverage.Complete, Capped: graph.Coverage.Capped, Skipped: graph.Coverage.Gaps}})
}

// BuildExecutionSnapshot uses the existing snapshot identity and lifetime.
// Failed contributors leave a queryable partial graph, including cancellation.
func BuildExecutionSnapshot(ctx context.Context, request ExecutionRequest, contributors ...ExecutionContributor) (AnalysisSnapshot, error) {
	budget, err := normalizeGraphBudget(request.Budget)
	if err != nil {
		return AnalysisSnapshot{}, err
	}
	if len(contributors) > maxExecutionContributors {
		return AnalysisSnapshot{}, Codedf("graph_contributors_exceeded", "at most %d contributors", maxExecutionContributors)
	}
	request.Budget = budget
	key := request.Key
	key.ExecutionBudget = &budget
	key.Producers = nil
	for _, contributor := range contributors {
		key.Producers = append(key.Producers, ProducerVersion{Name: contributor.Name(), Version: contributor.Version()})
	}
	sort.Slice(key.Producers, func(i, j int) bool { return executionJSON(key.Producers[i]) < executionJSON(key.Producers[j]) })
	if key.Revision == "" || len(executionJSON(key)) > maxExecutionIdentityBytes {
		return AnalysisSnapshot{}, Codedf("graph_identity_invalid", "content revision is required and identity is limited to 4096 bytes")
	}
	request.Key = key
	builder := newExecutionBuilder(key.Revision, budget)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(budget.AnalysisMillis)*time.Millisecond)
	defer cancel()
	if len(contributors) == 0 {
		builder.gap("no_execution_contributors")
	}
	for _, contributor := range contributors {
		builder.covered = false
		if ctx.Err() != nil {
			builder.gap("analysis_cancelled")
			break
		}
		if err := contributor.ContributeExecution(ctx, request, builder); err != nil {
			builder.gap(contributor.Name() + ": " + ErrorCode(err))
		}
		if !builder.covered {
			builder.gap(contributor.Name() + ": coverage_not_declared")
		}
	}
	if ctx.Err() != nil {
		builder.gap("analysis_cancelled")
	}
	return builder.finish(key), nil
}
