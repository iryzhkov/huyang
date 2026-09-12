package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MaxTransferBytes bounds the content a native move or copy carries through
// the recovery journal. It equals the plan content bound (mcpapi's
// MaxPlanContentBytes): a file larger than this is refused, never journaled
// in part.
const MaxTransferBytes = 4 << 20

// Stable error codes of the native file-lifecycle operations (move_file,
// copy_file, delete_file and a guarded create_file replace). Each names a
// refusal that changed nothing.
const (
	CodeCreateTargetExists      = "create_target_exists"
	CodeMoveSourceMissing       = "move_source_missing"
	CodeMoveTargetExists        = "move_target_exists"
	CodeCopySourceMissing       = "copy_source_missing"
	CodeCopySourceTooLarge      = "copy_source_too_large"
	CodeCopySourceChanged       = "copy_source_changed"
	CodeTransferUnsupported     = "transfer_source_unsupported"
	CodeDeleteGuardRequired     = "delete_guard_required"
	CodeDeleteTargetChanged     = "delete_target_changed"
	CodeReplaceRevisionRequired = "replace_revision_required"
)

// TransferSource is the exact bytes a move or copy carries: a regular file
// inside the workspace (with its revision) or, for a copy, an absolute path
// outside it, read once and hashed so the receipt records what was copied.
type TransferSource struct {
	// Path is the display path: workspace-relative inside the root,
	// absolute outside it.
	Path     string
	Absolute string
	Content  []byte
	Mode     uint32
	SHA256   string
	// Inside reports whether the source is a workspace document; only then
	// does Revision carry its current revision.
	Inside   bool
	Revision RevisionID
}

// TransferSource reads the source of a copy. A path the workspace confines
// is read as a document; any other absolute path is read as an external
// file. Directories and symlinks are refused, as is a file over
// MaxTransferBytes, and an expected hash that does not match the bytes.
func (w *Workspace) TransferSource(from, expectedSHA256 string) (TransferSource, error) {
	source := TransferSource{Path: from}
	absolute, err := w.confinedPath(from)
	if err == nil {
		source.Inside, source.Absolute, source.Path = true, absolute, displayPath(w.identity.Root, absolute)
	} else {
		if !filepath.IsAbs(from) {
			return source, Codedf(CodeCopySourceMissing, "%s is not a workspace document; a source outside the workspace must be an absolute path", from)
		}
		source.Absolute = filepath.Clean(from)
	}
	disk, content, err := inspectPath(source.Absolute)
	if err != nil {
		return source, Codedf(CodeCopySourceMissing, "%s: %w", source.Path, err)
	}
	if err := transferableKind(disk, source.Path, CodeCopySourceMissing); err != nil {
		return source, err
	}
	if len(content) > MaxTransferBytes {
		return source, Codedf(CodeCopySourceTooLarge, "%s is %d bytes; the transfer bound is %d", source.Path, len(content), MaxTransferBytes)
	}
	source.Content, source.Mode, source.SHA256 = content, disk.Mode, hashBytes(content)
	if expectedSHA256 != "" && expectedSHA256 != source.SHA256 {
		return source, Codedf(CodeCopySourceChanged, "%s has sha256 %s, expected %s", source.Path, source.SHA256, expectedSHA256)
	}
	if source.Inside {
		snapshot, err := w.Refresh(source.Absolute, ProviderLayer{})
		if err != nil {
			return source, err
		}
		source.Revision = snapshot.Revision
	}
	return source, nil
}

// transferableKind refuses a source that a native transfer cannot carry
// exactly: a missing path (under missingCode), a directory or a symlink.
func transferableKind(disk DiskSnapshot, path, missingCode string) error {
	switch disk.Kind {
	case ObjectMissing:
		return Codedf(missingCode, "%s does not exist", path)
	case ObjectRegularText, ObjectBinary:
		return nil
	default:
		return Codedf(CodeTransferUnsupported, "%s is %s; edit_apply moves and copies regular files only, use change_plan for a symlink", path, disk.Kind)
	}
}

// transferDestination confines and checks the destination of a move or
// copy: it must not exist. With a revision the check is the usual guarded
// one; without it the current snapshot is taken. The parent directory is
// created so a transfer into a new directory is one call.
func (w *Workspace) transferDestination(workspaceID ID, to string, expected RevisionID, existsCode string) (string, error) {
	var current DocumentSnapshot
	var err error
	if expected != "" {
		current, err = w.ValidateMutation(workspaceID, to, expected, ProviderLayer{})
	} else {
		current, err = w.Snapshot(to, ProviderLayer{})
	}
	if err != nil {
		return "", err
	}
	if current.Disk.Kind != ObjectMissing {
		return "", Codedf(existsCode, "%s already exists", to)
	}
	absolute, err := w.confinedPath(to)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return "", err
	}
	return absolute, nil
}

// ApplyCopy writes source's bytes to a new document at to, through the same
// journaled write every native mutation uses. The source is not touched.
func (w *Workspace) ApplyCopy(workspaceID ID, source TransferSource, to string, destinationRevision RevisionID) (ExactDiff, DocumentSnapshot, error) {
	if w.stateDir == "" {
		return ExactDiff{}, DocumentSnapshot{}, errors.New("native mutation recovery state directory is required")
	}
	absolute, err := w.transferDestination(workspaceID, to, destinationRevision, CodeCreateTargetExists)
	if err != nil {
		return ExactDiff{}, DocumentSnapshot{}, err
	}
	if err := w.mutateFile(absolute, false, nil, true, source.Content, transferMode(source.Mode)); err != nil {
		return ExactDiff{}, DocumentSnapshot{}, err
	}
	after, err := w.Refresh(absolute, ProviderLayer{})
	diff := exactDiff(displayPath(w.identity.Root, absolute), nil, source.Content, 0, 0, source.Content)
	return diff, after, err
}

// MoveResult is what a native move produced: the source as it was read and
// the destination's snapshot afterwards.
type MoveResult struct {
	Source      TransferSource
	Destination DocumentSnapshot
	SourceAfter DocumentSnapshot
}

// ApplyMove moves a regular file inside the workspace: the destination is
// written first with the source's exact bytes and mode, then the source is
// removed, each through its own recovery journal. A crash between the two
// leaves a complete copy at both paths and never loses the content; the
// journals resolve on the next open. The bytes are never reformatted or
// re-encoded, so the destination's content revision equals the source's.
func (w *Workspace) ApplyMove(workspaceID ID, from string, expectedSource RevisionID, to string, destinationRevision RevisionID) (MoveResult, error) {
	if w.stateDir == "" {
		return MoveResult{}, errors.New("native mutation recovery state directory is required")
	}
	current, err := w.ValidateMutation(workspaceID, from, expectedSource, ProviderLayer{})
	if err != nil {
		return MoveResult{}, err
	}
	if err := transferableKind(current.Disk, from, CodeMoveSourceMissing); err != nil {
		return MoveResult{}, err
	}
	sourceAbsolute, err := w.confinedPath(from)
	if err != nil {
		return MoveResult{}, err
	}
	disk, content, err := inspectPath(sourceAbsolute)
	if err != nil {
		return MoveResult{}, err
	}
	if hashBytes(content) != current.ContentSHA256 {
		return MoveResult{}, &Conflict{Code: ConflictDocumentChanged, Path: from, Expected: expectedSource, Current: current.Revision}
	}
	if len(content) > MaxTransferBytes {
		return MoveResult{}, Codedf(CodeCopySourceTooLarge, "%s is %d bytes; the transfer bound is %d", from, len(content), MaxTransferBytes)
	}
	source := TransferSource{Path: displayPath(w.identity.Root, sourceAbsolute), Absolute: sourceAbsolute, Content: content, Mode: disk.Mode, SHA256: hashBytes(content), Inside: true, Revision: current.Revision}
	destinationAbsolute, err := w.transferDestination(workspaceID, to, destinationRevision, CodeMoveTargetExists)
	if err != nil {
		return MoveResult{}, err
	}
	if err := w.mutateFile(destinationAbsolute, false, nil, true, content, transferMode(disk.Mode)); err != nil {
		return MoveResult{}, err
	}
	if err := w.mutateFile(sourceAbsolute, true, content, false, nil, transferMode(disk.Mode)); err != nil {
		return MoveResult{}, fmt.Errorf("destination written, source not removed: %w", err)
	}
	result := MoveResult{Source: source}
	if result.Destination, err = w.Refresh(destinationAbsolute, ProviderLayer{}); err != nil {
		return result, err
	}
	result.SourceAfter, err = w.Refresh(sourceAbsolute, ProviderLayer{})
	return result, err
}

// transferMode keeps the permission bits of a source for its destination
// and falls back to the ordinary file mode for a source that reported none.
func transferMode(mode uint32) uint32 {
	if perm := mode & 0o777; perm != 0 {
		return perm
	}
	return 0o644
}
