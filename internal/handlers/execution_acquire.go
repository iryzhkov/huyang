package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const executionBatchFiles = 32
const executionProviderMillis = 6000

type executionProviderSymbol struct {
	File       string `json:"file"`
	Name       string `json:"name"`
	Line       int    `json:"line"`
	Col        int    `json:"col"`
	Unresolved bool   `json:"unresolved"`
	EndLine    int    `json:"end_line"`
}
type executionProviderEdge struct {
	Source   executionProviderSymbol `json:"source"`
	Target   executionProviderSymbol `json:"target"`
	Line     int                     `json:"line"`
	Col      int                     `json:"col"`
	Producer string                  `json:"producer"`
	Version  string                  `json:"version"`
	Method   string                  `json:"method"`
}
type executionProviderReference struct {
	Target   executionProviderSymbol `json:"target"`
	File     string                  `json:"file"`
	Line     int                     `json:"line"`
	Producer string                  `json:"producer"`
	Version  string                  `json:"version"`
}
type executionProviderBatch struct {
	Sources    map[string]string            `json:"sources"`
	References []executionProviderReference `json:"references"`
	Nodes      []executionProviderSymbol    `json:"nodes"`
	Edges      []executionProviderEdge      `json:"edges"`
	Producers  map[string]string            `json:"producers"`
	Capped     bool                         `json:"capped"`
	Incomplete bool                         `json:"incomplete"`
}

func (h *Handlers) acquireExecution(ctx context.Context, workspace *workspacecore.Workspace, sources []workspacecore.ExecutionSource, revision string, withProvider bool) []workspacecore.ExecutionContributor {
	// Acquisition order is explicit: hierarchy/references, parser, then the
	// unresolved source frontier. Each keeps its own coverage and provenance.
	contributors := []workspacecore.ExecutionContributor{}
	if withProvider {
		contributors = append(contributors, h.acquireExecutionProvider(ctx, workspace, sources, revision))
	}
	native := workspacecore.AcquireGoExecutionCalls(ctx, revision, sources, workspacecore.DefaultGraphBudget())
	bindExecutionFacts(ctx, workspace, sources, &native)
	contributors = append(contributors, native)
	return contributors
}

func (h *Handlers) acquireExecutionProvider(ctx context.Context, workspace *workspacecore.Workspace, sources []workspacecore.ExecutionSource, revision string) workspacecore.ExecutionCallFacts {
	facts := workspacecore.ExecutionCallFacts{Producer: workspacecore.ProducerVersion{Name: "execution_batch", Version: "1"},
		Coverage: workspacecore.ExecutionCoverage{Complete: false, Gaps: []string{"dynamic_dispatch_not_exhaustive"}, Limits: []string{}}}
	files := []string{}
	for _, source := range sources {
		switch filepath.Ext(source.Path) {
		case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".lua":
			if len(files) >= executionBatchFiles {
				facts.Coverage.Capped = true
				continue
			}
			files = append(files, filepath.Join(workspace.Identity().Root, source.Path))
		}
	}
	providerCtx, cancel := context.WithTimeout(ctx, time.Duration(executionProviderMillis)*time.Millisecond)
	defer cancel()
	value, err := h.callProvider(providerCtx, "execution_acquisition", workspace, "huyang_execution_batch", map[string]any{"files": files, "with_lsp": true})
	if err != nil {
		facts.Coverage.Gaps = append(facts.Coverage.Gaps, "provider_batch_unavailable")
		return facts
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		facts.Coverage.Gaps = append(facts.Coverage.Gaps, "provider_batch_invalid")
		return facts
	}
	var batch executionProviderBatch
	if err = json.Unmarshal(encoded, &batch); err != nil {
		facts.Coverage.Gaps = append(facts.Coverage.Gaps, "provider_batch_invalid")
		return facts
	}
	facts.Coverage.Capped = facts.Coverage.Capped || batch.Capped
	if batch.Incomplete {
		facts.Coverage.Gaps = append(facts.Coverage.Gaps, "provider_queries_incomplete")
	}
	versions, _ := json.Marshal(batch.Producers)
	facts.Producer.Version = fmt.Sprintf("1-%x", sha256.Sum256(versions))
	if !executionBatchMatchesSources(workspace, sources, batch) {
		facts.Coverage.Gaps = append(facts.Coverage.Gaps, "provider_source_mapping_ambiguous")
		return facts
	}
	decodeExecutionBatch(workspace, sources, revision, batch, &facts)
	bindExecutionFacts(ctx, workspace, sources, &facts)
	return facts
}

func decodeExecutionBatch(workspace *workspacecore.Workspace, sources []workspacecore.ExecutionSource, revision string, batch executionProviderBatch, facts *workspacecore.ExecutionCallFacts) {
	known := map[string]workspacecore.ExecutionSource{}
	for _, source := range sources {
		known[filepath.Join(workspace.Identity().Root, source.Path)] = source
	}
	nodes := map[string]workspacecore.ExecutionNode{}
	for _, edge := range batch.Edges {
		confidence := "semantic"
		if edge.Method == "parser_candidate" {
			confidence = "parser"
		}
		evidence := []workspacecore.ExecutionEvidence{{Revision: revision, Producer: workspacecore.ProducerVersion{Name: edge.Producer, Version: edge.Version},
			Confidence: confidence, Classification: "static", SourceHandles: []string{}, EvidenceIDs: []string{},
			Coverage: workspacecore.ExecutionCoverage{Complete: false, Gaps: []string{"dynamic_dispatch_not_exhaustive"}, Limits: []string{}}}}
		from := executionProviderNode(edge.Source, known, evidence)
		to := executionProviderNode(edge.Target, known, evidence)
		mergeExecutionProviderNode(nodes, from)
		mergeExecutionProviderNode(nodes, to)
		kind := "calls"
		if edge.Method == "implementation" {
			kind = "dynamic_dispatch"
		}
		facts.Edges = append(facts.Edges, workspacecore.ExecutionEdge{From: from.ID, To: to.ID, Kind: kind,
			SitePath: from.Path, SiteLine: edge.Line, SiteColumn: edge.Col, Evidence: evidence})
	}
	for _, raw := range batch.Nodes {
		evidence := []workspacecore.ExecutionEvidence{{Revision: revision, Producer: facts.Producer, Confidence: "parser", Classification: "static",
			SourceHandles: []string{}, EvidenceIDs: []string{}, Coverage: facts.Coverage}}
		node := executionProviderNode(raw, known, evidence)
		if _, exists := nodes[node.ID]; !exists {
			nodes[node.ID] = node
		}
	}
	decodeExecutionReferences(batch, known, revision, nodes, facts)
	for _, node := range nodes {
		facts.Nodes = append(facts.Nodes, node)
	}
	sort.Slice(facts.Nodes, func(i, j int) bool { return facts.Nodes[i].ID < facts.Nodes[j].ID })
}

func executionProviderNode(symbol executionProviderSymbol, known map[string]workspacecore.ExecutionSource, evidence []workspacecore.ExecutionEvidence) workspacecore.ExecutionNode {
	evidence = append([]workspacecore.ExecutionEvidence(nil), evidence...)
	source, ok := known[filepath.Clean(symbol.File)]
	name := leafName(symbol.Name)
	if !ok {
		return workspacecore.ExecutionNode{ID: fmt.Sprintf("external:%x", sha256.Sum256([]byte(symbol.File+"#"+symbol.Name))), Kind: "external", Name: name, Evidence: evidence}
	}
	kind := "symbol"
	if symbol.Unresolved {
		kind = "unresolved"
	}
	id := workspacecore.ExecutionSymbolID(source.Path, symbol.Line, symbol.Col)
	if symbol.Unresolved {
		id = "unresolved:" + id
	}
	return workspacecore.ExecutionNode{ID: id, Kind: kind, Path: source.Path, Name: name, Line: symbol.Line, Column: symbol.Col, Evidence: evidence}
}

func bindExecutionFacts(ctx context.Context, workspace *workspacecore.Workspace, sources []workspacecore.ExecutionSource, facts *workspacecore.ExecutionCallFacts) {
	known := map[string]workspacecore.ExecutionSource{}
	for _, source := range sources {
		known[source.Path] = source
	}
	bind := func(path string, line, column int, evidence []workspacecore.ExecutionEvidence) {
		if ctx.Err() != nil {
			facts.Coverage.Capped = true
			facts.Coverage.Limits = []string{"analysis_time"}
			return
		}
		source, ok := known[path]
		if !ok {
			return
		}
		handle, err := workspace.ExecutionSourceHandle(source, line, column)
		if err != nil {
			facts.Coverage.Gaps = append(facts.Coverage.Gaps, "source_handle_unavailable")
			return
		}
		for i := range evidence {
			evidence[i].SourceHandles = []string{handle}
		}
	}
	for i := range facts.Nodes {
		node := &facts.Nodes[i]
		bind(node.Path, node.Line, node.Column, node.Evidence)
	}
	for i := range facts.Edges {
		edge := &facts.Edges[i]
		bind(edge.SitePath, edge.SiteLine, edge.SiteColumn, edge.Evidence)
	}
}
