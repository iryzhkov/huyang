package workspace

import (
	"fmt"
	"strings"
	"time"
)

// ExecutionSourceHandle binds a captured location to canonical bytes. The
// deterministic key makes repeated acquisition stable without a second store;
// the existing handle registry still owns retention, expiry and epoch refusal.
func (w *Workspace) ExecutionSourceHandle(source ExecutionSource, line, column int) (string, error) {
	read, err := w.Read(source.Path)
	if err != nil {
		return "", Coded("graph_source_failed", err)
	}
	if hashBytes(read.Content) != source.Hash {
		return "", Codedf("graph_source_changed", "source changed while binding graph location")
	}
	lines := strings.SplitAfter(string(read.Content), "\n")
	if line < 1 || line > len(lines) || column < 1 || column > len(lines[line-1])+1 {
		return "", Codedf("graph_source_invalid", "location is outside captured source")
	}
	start := 0
	for _, text := range lines[:line-1] {
		start += len(text)
	}
	start += column - 1
	end := start
	for end < len(read.Content) && read.Content[end] != '\n' {
		end++
	}
	handle, err := rangeHandle(read, start, end, defaultAnchorBytes)
	if err != nil {
		return "", Coded("graph_source_invalid", err)
	}
	identity := w.Identity()
	record := HandleRecord{WorkspaceID: identity.ID, Epoch: identity.Epoch, Kind: HandleRange,
		Display: fmt.Sprintf("%s:%d:%d execution location", source.Path, line, column),
		Locator: SemanticLocator{Path: read.Path, Kind: string(HandleRange), ByteStart: start, ByteEnd: end,
			NodeSHA256: handle.ExpectedSHA256, ContentSHA256: handle.ExpectedSHA256,
			BeforeSHA256: handle.BeforeSHA256, AfterSHA256: handle.AfterSHA256, AnchorBytes: handle.AnchorBytes,
			DocumentRevision: handle.Revision, Device: read.Snapshot.Disk.Device, Inode: read.Snapshot.Disk.Inode}}
	record.Handle = HandleID("rng_" + hashBytes([]byte(executionJSON(record)))[:32])
	store := w.handleRegistry()
	store.mu.Lock()
	defer store.mu.Unlock()
	record.ExpiresAt = time.Now().Add(store.ttl)
	store.putHandleLocked(record)
	return string(record.Handle), nil
}
