package workspace

import (
	"path/filepath"
	"strings"
	"time"
)

// PreparedExecutionSourceHandle shares canonical handle retention, but can
// never relocate into canonical bytes. The caller must validate that the
// preparation still exists before resolving this source reference.
func (w *Workspace) PreparedExecutionSourceHandle(source ExecutionSource, revision string, line, column int) (string, error) {
	if revision == "" || filepath.IsAbs(source.Path) || strings.HasPrefix(filepath.Clean(source.Path), "..") {
		return "", Codedf("graph_source_invalid", "invalid prepared location")
	}
	lines := strings.SplitAfter(source.Content, "\n")
	if line < 1 || line > len(lines) || column < 1 || column > len(lines[line-1])+1 {
		return "", Codedf("graph_source_invalid", "prepared location outside source")
	}
	start := 0
	for _, text := range lines[:line-1] {
		start += len(text)
	}
	start += column - 1
	end := start
	for end < len(source.Content) && source.Content[end] != '\n' {
		end++
	}
	identity := w.Identity()
	record := HandleRecord{WorkspaceID: identity.ID, Epoch: identity.Epoch, Kind: HandleRange, PreparedRevision: revision,
		Locator: SemanticLocator{Path: source.Path, Kind: "prepared_execution", ByteStart: start, ByteEnd: end, DocumentRevision: RevisionID(revision),
			ContentSHA256: source.Hash, NodeSHA256: hashBytes([]byte(source.Content[start:end]))}}
	record.Handle = HandleID("psrc_" + hashBytes([]byte(executionJSON(record)))[:32])
	store := w.handleRegistry()
	store.mu.Lock()
	defer store.mu.Unlock()
	record.ExpiresAt = time.Now().Add(store.ttl)
	store.putHandleLocked(record)
	return string(record.Handle), nil
}
