package workspace

// Simulating a plan without writing anything.
//
// The preview is what makes a plan reviewable: every operation is applied to a
// copy of the documents it names, in the order the dependencies imply, and the
// result is exact bytes plus the record of where each operation's bytes landed.
// Nothing here touches the canonical tree, and a conflict found here is found
// before anything could have been half-written.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func (w *Workspace) buildPreview(plan PlanRecord) PlanPreview {
	ordered, orderErr := orderOperations(plan.Operations)
	preview := PlanPreview{PlanRevision: plan.PlanRevision, Outcome: "ok", CanonicalChanged: false}
	if orderErr != nil {
		preview.Outcome = "conflict"
		preview.Conflicts = append(preview.Conflicts, PlanConflict{Code: "operation_dependency_cycle", Message: orderErr.Error()})
		return finalizePreview(preview)
	}
	for _, operation := range ordered {
		preview.NormalizedOrder = append(preview.NormalizedOrder, operation.OpID)
	}
	builder := &previewBuilder{
		w: w, preview: preview,
		contents: make(map[string][]byte), exists: make(map[string]bool),
		before: make(map[string][]byte), touched: make(map[string]bool),
	}
	for _, operation := range ordered {
		builder.apply(operation)
	}
	return builder.finish()
}

// previewBuilder simulates one ordered plan against the current documents without
// writing. contents and exists hold the simulated state per path, before the bytes first
// read for the path, and touched the paths some operation changed.
type previewBuilder struct {
	w        *Workspace
	preview  PlanPreview
	contents map[string][]byte
	exists   map[string]bool
	before   map[string][]byte
	touched  map[string]bool
	hunks    []PlanHunk
}

func (b *previewBuilder) apply(operation PlanOperation) {
	switch operation.Kind {
	case OperationReplaceSymbol, OperationDeleteSymbol, OperationInsertBefore, OperationInsertAfter, OperationReplaceRange:
		b.applyEdit(operation)
	case OperationCreateFile:
		b.applyCreate(operation)
	case OperationDeleteFile:
		b.applyDelete(operation)
	case OperationMoveFile:
		b.applyMove(operation)
	case OperationCopyFile:
		b.applyCopy(operation)
	case OperationReplaceMatches:
		b.applyReplaceMatches(operation)
	default:
		b.preview.Conflicts = append(b.preview.Conflicts, PlanConflict{
			OpID: operation.OpID, Code: "operation_requires_later_stage",
			Message: fmt.Sprintf("%s requires provider or result-set validation unavailable before its named stage", operation.Kind),
		})
	}
}

func (b *previewBuilder) conflict(operation PlanOperation, path string, expected RevisionID, err error) {
	conflict := PlanConflict{OpID: operation.OpID, Code: ConflictDocumentChanged, Path: path, Expected: expected, Message: err.Error()}
	var typed *Conflict
	if errors.As(err, &typed) {
		conflict.Code, conflict.Current = typed.Code, typed.Current
	}
	b.preview.Conflicts = append(b.preview.Conflicts, conflict)
}

// set records the simulated content and presence of a path touched by an operation.
func (b *previewBuilder) set(path string, content []byte, exists bool) {
	b.contents[path], b.exists[path], b.touched[path] = content, exists, true
}

// record remembers where one operation's replacement landed, and moves the
// regions earlier operations wrote in the same file by however much this
// splice changed their offsets. A region this splice overwrote is marked
// superseded: those bytes are now the later operation's, and attributing a
// diagnostic in them to the earlier one would be wrong.
func (b *previewBuilder) record(operation PlanOperation, path string, start, end, replacement int) {
	delta := replacement - (end - start)
	for index := range b.hunks {
		hunk := &b.hunks[index]
		if hunk.Path != path {
			continue
		}
		switch {
		case hunk.ByteStart >= end:
			hunk.ByteStart, hunk.ByteEnd = hunk.ByteStart+delta, hunk.ByteEnd+delta
		case hunk.ByteEnd > start:
			hunk.Superseded = true
		}
	}
	b.hunks = append(b.hunks, PlanHunk{
		OpID: operation.OpID, Path: path, ByteStart: start, ByteEnd: start + replacement,
	})
}

// remember records content as both the before image and the simulated content of path.
func (b *previewBuilder) remember(path string, content []byte) []byte {
	b.contents[path], b.before[path], b.exists[path] = content, append([]byte(nil), content...), true
	return content
}

func (b *previewBuilder) load(path string) ([]byte, bool, error) {
	if content, ok := b.contents[path]; ok {
		return content, b.exists[path], nil
	}
	read, err := b.w.Read(path)
	if err != nil {
		return b.loadUnreadable(path, err)
	}
	return b.remember(path, append([]byte(nil), read.Content...)), true, nil
}

// loadUnreadable resolves a path the text reader refused: a missing document previews as
// absent, a binary or symlink document previews with its raw bytes or target.
func (b *previewBuilder) loadUnreadable(path string, readErr error) ([]byte, bool, error) {
	snapshot, snapshotErr := b.w.Refresh(path, ProviderLayer{})
	if snapshotErr != nil {
		return nil, false, readErr
	}
	switch snapshot.Disk.Kind {
	case ObjectMissing:
		b.contents[path], b.before[path], b.exists[path] = nil, nil, false
		return nil, false, nil
	case ObjectBinary:
		absolute := filepath.Join(b.w.Identity().Root, filepath.FromSlash(path))
		content, binaryErr := os.ReadFile(absolute)
		if binaryErr != nil {
			return nil, false, binaryErr
		}
		if int64(len(content)) != snapshot.Disk.Size || hashBytes(content) != snapshot.ContentSHA256 {
			return nil, false, &Conflict{
				Code:     ConflictDocumentChanged,
				Path:     path,
				Expected: snapshot.Revision,
				Current:  snapshot.Revision,
			}
		}
		return b.remember(path, content), true, nil
	case ObjectSymlink:
		return b.remember(path, []byte(snapshot.Disk.SymlinkTarget)), true, nil
	}
	return nil, false, readErr
}

// loadPresent loads path and reports an absent document as the given error.
func (b *previewBuilder) loadPresent(path, missing string) ([]byte, error) {
	content, present, err := b.load(path)
	if err == nil && !present {
		err = errors.New(missing)
	}
	return content, err
}

// rangeMatches reports whether content[start:end] is a valid range with the expected hash.
func rangeMatches(content []byte, start, end int, expectedSHA256 string) bool {
	if start < 0 || end < start || end > len(content) {
		return false
	}
	return hashBytes(content[start:end]) == expectedSHA256
}

// splice returns content with [start:end] replaced by replacement.
func splice(content []byte, start, end int, replacement []byte) []byte {
	next := make([]byte, 0, len(content)-(end-start)+len(replacement))
	next = append(next, content[:start]...)
	next = append(next, replacement...)
	return append(next, content[end:]...)
}

func (b *previewBuilder) applyEdit(operation PlanOperation) {
	handle := *operation.Target.FileRange
	current, err := b.w.ValidateMutation(b.w.Identity().ID, handle.Path, handle.Revision, ProviderLayer{})
	if err != nil {
		b.conflict(operation, handle.Path, handle.Revision, err)
		return
	}
	content, err := b.loadPresent(handle.Path, "target file is missing")
	if err != nil {
		b.conflict(operation, handle.Path, handle.Revision, err)
		return
	}
	if !rangeMatches(content, handle.ByteStart, handle.ByteEnd, handle.ExpectedSHA256) {
		b.conflict(operation, handle.Path, handle.Revision, &Conflict{Code: ConflictDocumentChanged, Path: handle.Path, Expected: handle.Revision, Current: current.Revision})
		return
	}
	start, end := handle.ByteStart, handle.ByteEnd
	switch operation.Kind {
	case OperationInsertBefore:
		end = start
	case OperationInsertAfter:
		start = end
	}
	replacement := []byte(operation.Content)
	if operation.Indentation == "syntax_anchor" {
		replacement, err = syntaxAnchorReplacement(content, start, replacement)
		if err != nil {
			b.conflict(operation, handle.Path, handle.Revision, err)
			return
		}
	}
	b.record(operation, handle.Path, start, end, len(replacement))
	b.set(handle.Path, splice(content, start, end, replacement), true)
}

func (b *previewBuilder) applyCreate(operation PlanOperation) {
	current, err := b.w.ValidateMutation(b.w.Identity().ID, operation.Path, operation.Revision, ProviderLayer{})
	if err != nil {
		b.conflict(operation, operation.Path, operation.Revision, err)
		return
	}
	if current.Disk.Kind != ObjectMissing {
		b.conflict(operation, operation.Path, operation.Revision, errors.New("create target already exists"))
		return
	}
	_, _, _ = b.load(operation.Path)
	b.set(operation.Path, []byte(operation.Content), true)
}

func (b *previewBuilder) applyDelete(operation PlanOperation) {
	if _, err := b.w.ValidateMutation(b.w.Identity().ID, operation.Path, operation.Revision, ProviderLayer{}); err != nil {
		b.conflict(operation, operation.Path, operation.Revision, err)
		return
	}
	if _, err := b.loadPresent(operation.Path, "delete target is missing"); err != nil {
		b.conflict(operation, operation.Path, operation.Revision, err)
		return
	}
	b.set(operation.Path, nil, false)
}

func (b *previewBuilder) applyMove(operation PlanOperation) {
	if _, err := b.w.ValidateMutation(b.w.Identity().ID, operation.From, operation.Revision, ProviderLayer{}); err != nil {
		b.conflict(operation, operation.From, operation.Revision, err)
		return
	}
	destination, err := b.w.ValidateMutation(b.w.Identity().ID, operation.To, operation.DestinationRevision, ProviderLayer{})
	if err != nil {
		b.conflict(operation, operation.To, operation.DestinationRevision, err)
		return
	}
	if destination.Disk.Kind != ObjectMissing {
		b.conflict(operation, operation.To, operation.DestinationRevision, errors.New("move destination exists"))
		return
	}
	source, err := b.loadPresent(operation.From, "move source is missing")
	if err != nil {
		b.conflict(operation, operation.From, operation.Revision, err)
		return
	}
	_, _, _ = b.load(operation.To)
	b.set(operation.To, append([]byte(nil), source...), true)
	b.set(operation.From, nil, false)
}

// applyCopy previews a copy: the source is the simulated content of a
// workspace document (so an earlier operation's edit is copied), or the
// external file's bytes rechecked against the bound hash.
func (b *previewBuilder) applyCopy(operation PlanOperation) {
	destination, err := b.w.ValidateMutation(b.w.Identity().ID, operation.To, operation.DestinationRevision, ProviderLayer{})
	if err != nil {
		b.conflict(operation, operation.To, operation.DestinationRevision, err)
		return
	}
	if destination.Disk.Kind != ObjectMissing {
		b.conflict(operation, operation.To, operation.DestinationRevision, errors.New("copy destination exists"))
		return
	}
	var content []byte
	if _, inside := b.w.confinedPath(operation.From); inside == nil {
		loaded, loadErr := b.loadPresent(operation.From, "copy source is missing")
		if loadErr != nil {
			b.conflict(operation, operation.From, "", loadErr)
			return
		}
		if !b.touched[operation.From] && hashBytes(loaded) != operation.ExpectedSHA256 {
			b.conflict(operation, operation.From, "", Codedf(CodeCopySourceChanged, "copy source no longer matches its bound hash"))
			return
		}
		content = loaded
	} else {
		source, sourceErr := b.w.TransferSource(operation.From, operation.ExpectedSHA256)
		if sourceErr != nil {
			b.conflict(operation, operation.From, "", sourceErr)
			return
		}
		content = source.Content
	}
	_, _, _ = b.load(operation.To)
	b.set(operation.To, append([]byte(nil), content...), true)
}

func (b *previewBuilder) applyReplaceMatches(operation PlanOperation) {
	hits, err := b.w.ResolveAllMatches(ResultSetID(operation.Target.Handle))
	if err != nil {
		b.conflict(operation, "", "", err)
		return
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Path == hits[j].Path {
			return hits[i].ByteStart > hits[j].ByteStart
		}
		return hits[i].Path < hits[j].Path
	})
	for _, hit := range hits {
		content, loadErr := b.loadPresent(hit.Path, "result-set target file is missing")
		if loadErr != nil {
			b.conflict(operation, hit.Path, hit.Range.Revision, loadErr)
			continue
		}
		if !rangeMatches(content, hit.ByteStart, hit.ByteEnd, hit.Range.ExpectedSHA256) {
			b.conflict(operation, hit.Path, hit.Range.Revision, &Conflict{Code: ConflictDocumentChanged, Path: hit.Path, Expected: hit.Range.Revision})
			continue
		}
		b.set(hit.Path, splice(content, hit.ByteStart, hit.ByteEnd, []byte(operation.Content)), true)
	}
}

// finish turns the simulated state into affected files and exact diffs, or reports the
// collected conflicts.
func (b *previewBuilder) finish() PlanPreview {
	preview := b.preview
	if len(preview.Conflicts) > 0 {
		preview.Outcome = "conflict"
		return finalizePreview(preview)
	}
	for path := range b.touched {
		preview.AffectedFiles = append(preview.AffectedFiles, path)
		if b.exists[path] {
			preview.Diffs = append(preview.Diffs, exactDiff(path, b.before[path], b.contents[path], 0, len(b.before[path]), b.contents[path]))
		} else {
			preview.Diffs = append(preview.Diffs, exactDiff(path, b.before[path], nil, 0, len(b.before[path]), nil))
		}
	}
	sort.Strings(preview.AffectedFiles)
	sort.Slice(preview.Diffs, func(i, j int) bool { return preview.Diffs[i].Path < preview.Diffs[j].Path })
	// Byte offsets are how the splices were tracked; lines are how a
	// diagnostic will arrive, so the hunks carry both.
	for index := range b.hunks {
		hunk := &b.hunks[index]
		content := b.contents[hunk.Path]
		hunk.StartLine = 1 + bytes.Count(content[:min(hunk.ByteStart, len(content))], []byte{'\n'})
		hunk.EndLine = 1 + bytes.Count(content[:min(hunk.ByteEnd, len(content))], []byte{'\n'})
	}
	preview.Hunks = b.hunks
	return finalizePreview(preview)
}

func finalizePreview(preview PlanPreview) PlanPreview {
	copy := preview
	copy.PreviewRevision = ""
	encoded, _ := json.Marshal(copy)
	sum := sha256.Sum256(encoded)
	preview.PreviewRevision = "preview_" + hex.EncodeToString(sum[:16])
	return preview
}
