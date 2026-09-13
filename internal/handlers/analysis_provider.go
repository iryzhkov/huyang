package handlers

// The language server as an analysis contributor.
//
// The native reader sees what a file says it imports. A language server sees
// which declarations are actually referenced, across packages and through
// indirection a regular expression cannot follow. Both go into the snapshot,
// each with its own name and confidence, so a later answer can say which kind
// of knowledge it rests on.
//
// It contributes only for the canonical revision. A sandbox holds staged bytes
// the canonical server has never seen, and a fact computed against one tree
// must not be labelled as a fact about another; prepared revisions become
// semantically inspectable in their own stage.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const (
	// analysisFileBudget and analysisSymbolBudget bound how much a snapshot
	// costs: a language server round trip per declaration adds up, and the
	// question being answered is about the files a change touches, not about
	// the whole repository.
	analysisFileBudget   = 20
	analysisSymbolBudget = 20
)

type providerAnalysisContributor struct {
	handlers  *Handlers
	workspace *workspacecore.Workspace
	epoch     uint64
}

func (providerAnalysisContributor) Name() string { return "embedded_nvim" }

// Version is the provider generation, so a restarted or upgraded language
// server produces a new snapshot rather than reusing the old one's opinions.
func (c providerAnalysisContributor) Version() string { return fmt.Sprintf("epoch%d", c.epoch) }

func (c providerAnalysisContributor) Contribute(ctx context.Context, request workspacecore.AnalysisRequest, builder *workspacecore.AnalysisBuilder) error {
	files := request.Changed
	if len(files) > analysisFileBudget {
		files = files[:analysisFileBudget]
		builder.Skipped("embedded_nvim: only the first analysis file budget was examined")
	}
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.contributeFile(ctx, path, builder); err != nil {
			return err
		}
	}
	return nil
}

// contributeFile records who references each declaration of one file.
func (c providerAnalysisContributor) contributeFile(ctx context.Context, path string, builder *workspacecore.AnalysisBuilder) error {
	value, err := c.handlers.callProvider(ctx, "analysis_symbols", c.workspace, "file_symbols", map[string]any{
		"root": c.workspace.Identity().Root, "file": path,
	})
	if err != nil {
		return err
	}
	payload, _ := value.(map[string]any)
	matches := mcpapi.AnySlice(payload["matches"])
	if len(matches) > analysisSymbolBudget {
		matches = matches[:analysisSymbolBudget]
		builder.Skipped("embedded_nvim: only the first symbols of " + path + " were examined")
	}
	fileNode := workspacecore.NodeID(workspacecore.NodeFile, path, "")
	for _, raw := range matches {
		match, _ := raw.(map[string]any)
		name := fmt.Sprint(match["name_path"])
		builder.Node(workspacecore.AnalysisNode{
			Kind: workspacecore.NodeSymbol, Path: path, NamePath: name,
		})
		builder.Fact(fileNode, workspacecore.NodeID(workspacecore.NodeSymbol, path, name),
			workspacecore.EdgeExports, c.producer())
		if err := c.contributeReferences(ctx, path, name, firstLine(match), fileNode, builder); err != nil {
			return err
		}
	}
	return nil
}

// contributeReferences records each file that refers to one declaration.
func (c providerAnalysisContributor) contributeReferences(ctx context.Context, path, name string, line int, fileNode string, builder *workspacecore.AnalysisBuilder) error {
	// The server answers about a position, so the declaration's own line goes
	// with the name: without it the request is about wherever the cursor
	// happens to be, which is nowhere.
	value, err := c.handlers.callProvider(ctx, "analysis_references", c.workspace, "references", map[string]any{
		"root": c.workspace.Identity().Root, "file": path, "symbol": leafName(name), "line": line,
	})
	if err != nil {
		// One unanswerable symbol is a gap, not a failure: the rest of the
		// file still has something to say.
		builder.Skipped("embedded_nvim: no references for " + path + "#" + name)
		return nil
	}
	payload, _ := value.(map[string]any)
	symbolNode := workspacecore.NodeID(workspacecore.NodeSymbol, path, name)
	for _, location := range referenceLocations(payload) {
		other := workspacePath(c.workspace, location.file)
		if other == path {
			continue
		}
		builder.Node(workspacecore.AnalysisNode{Kind: workspacecore.NodeFile, Path: other})
		// Both granularities: the file relation answers "what does this
		// change reach", and the symbol relation answers "who calls this
		// declaration", which is the question asked before an edit.
		builder.Fact(workspacecore.NodeID(workspacecore.NodeFile, other, ""), fileNode,
			workspacecore.EdgeReferences, c.producer())
		builder.Fact(workspacecore.NodeID(workspacecore.NodeFile, other, ""), symbolNode,
			workspacecore.EdgeCalls, c.producer())
	}
	return nil
}

// producer is what a language server's claim is worth: it read the code and
// resolved it, which is the strongest evidence this workspace has.
func (c providerAnalysisContributor) producer() workspacecore.FactProducer {
	return workspacecore.FactProducer{
		Name: c.Name(), Confidence: string(workspacecore.ConfidenceAuthoritative),
		Coverage: workspacecore.Coverage{Complete: true, Semantic: "embedded_nvim"},
	}
}

// firstLine is the line a declaration starts on, which the kernel reports as
// a "first-last" range.
func firstLine(match map[string]any) int {
	lines := fmt.Sprint(match["lines"])
	first := lines
	if index := strings.IndexByte(lines, '-'); index > 0 {
		first = lines[:index]
	}
	value, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 1
	}
	return value
}

func leafName(namePath string) string {
	if index := lastIndexAny(namePath, "/."); index >= 0 {
		return namePath[index+1:]
	}
	return namePath
}

func lastIndexAny(value, chars string) int {
	for index := len(value) - 1; index >= 0; index-- {
		for _, char := range chars {
			if rune(value[index]) == char {
				return index
			}
		}
	}
	return -1
}

// analysisContributors are the contributors available for one verification.
// The language server is included only for the canonical revision, because
// that is the only tree it has read.
func (h *Handlers) analysisContributors(ctx context.Context, workspace *workspacecore.Workspace, revision string) []workspacecore.AnalysisContributor {
	identity := workspace.Identity()
	if revision != fmt.Sprintf("wsrev_%d", identity.StateSeq) {
		return nil
	}
	backend := h.pool.Existing(workspace)
	if backend == nil {
		return nil
	}
	return []workspacecore.AnalysisContributor{providerAnalysisContributor{
		handlers: h, workspace: workspace, epoch: backend.Descriptor().Epoch,
	}}
}
