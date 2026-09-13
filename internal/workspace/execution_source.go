package workspace

import (
	"context"
	"sort"
)

// ExecutionSource is an immutable input copy. Providers may read the live
// tree, but their result is accepted only while its content identity matches
// this capture. Parsers read these bytes directly.
type ExecutionSource struct {
	Path    string
	Content string
	Hash    string
}

// ExecutionSources reuses the workspace's bounded, confined enumeration.
// The hash includes relative paths and bytes, never timestamps or sandboxes.
func (w *Workspace) ExecutionSources(ctx context.Context) ([]ExecutionSource, string, Coverage, error) {
	files, coverage, err := w.collectFiles()
	if err != nil {
		return nil, "", coverage, Coded("graph_source_failed", err)
	}
	sources := []ExecutionSource{}
	total := 0
	for _, file := range files {
		if ctx.Err() != nil {
			coverage.noteSkipped("", "analysis_cancelled")
			break
		}
		disk, content, err := inspectPath(file)
		name := displayPath(w.Identity().Root, file)
		if err != nil {
			coverage.noteSkipped(name, "unreadable")
			continue
		}
		if disk.Kind != ObjectRegularText {
			coverage.noteSkipped(name, "not_regular_text")
			continue
		}
		if total+len(content) > MaxExecutionBytes {
			coverage.Capped = true
			coverage.noteSkipped(name, "execution_source_bytes")
			break
		}
		total += len(content)
		sources = append(sources, ExecutionSource{Path: name, Content: string(content), Hash: hashBytes(content)})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	identity := make([][2]string, 0, len(sources))
	for _, source := range sources {
		identity = append(identity, [2]string{source.Path, source.Hash})
	}
	return sources, "content_" + hashBytes([]byte(executionJSON(identity))), coverage, nil
}
