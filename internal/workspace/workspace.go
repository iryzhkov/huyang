package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

type ID string

type Kind string

const (
	KindProject            Kind = "project"
	KindDocuments          Kind = "documents"
	KindTransactionSandbox Kind = "transaction_sandbox"
)

type RevisionID string

type ObjectKind string

const (
	ObjectRegularText ObjectKind = "regular_text"
	ObjectBinary      ObjectKind = "binary"
	ObjectSymlink     ObjectKind = "symlink"
	ObjectDirectory   ObjectKind = "directory"
	ObjectMissing     ObjectKind = "missing"
)

type Identity struct {
	ID       ID     `json:"id"`
	Kind     Kind   `json:"kind"`
	Root     string `json:"root"`
	Epoch    uint64 `json:"epoch"`
	StateSeq uint64 `json:"state_seq"`
}

type DiskSnapshot struct {
	Kind          ObjectKind `json:"kind"`
	Device        uint64     `json:"device,omitempty"`
	Inode         uint64     `json:"inode,omitempty"`
	Size          int64      `json:"size,omitempty"`
	MTimeNS       int64      `json:"mtime_ns,omitempty"`
	Mode          uint32     `json:"mode,omitempty"`
	SymlinkTarget string     `json:"symlink_target,omitempty"`
}

type ProviderLayer struct {
	ChangedTick uint64           `json:"changedtick,omitempty"`
	Dirty       bool             `json:"dirty"`
	LSPVersions map[string]int64 `json:"lsp_versions,omitempty"`
	Content     []byte           `json:"-"`
}

type DocumentSnapshot struct {
	Workspace     Identity         `json:"workspace"`
	URI           string           `json:"uri"`
	Revision      RevisionID       `json:"revision_id"`
	ContentSHA256 string           `json:"content_sha256,omitempty"`
	Disk          DiskSnapshot     `json:"disk"`
	ChangedTick   uint64           `json:"changedtick,omitempty"`
	Dirty         bool             `json:"dirty"`
	LSPVersions   map[string]int64 `json:"lsp_versions,omitempty"`
}

type ConflictCode string

const (
	ConflictWorkspaceIDRequired    ConflictCode = "workspace_id_required"
	ConflictWorkspaceIDMismatch    ConflictCode = "workspace_id_mismatch"
	ConflictRevisionRequired       ConflictCode = "revision_required"
	ConflictWorkspaceEpoch         ConflictCode = "workspace_epoch_changed"
	ConflictDocumentChanged        ConflictCode = "document_content_changed"
	ConflictDocumentDeleted        ConflictCode = "document_deleted"
	ConflictTargetDeleted          ConflictCode = "target_deleted"
	ConflictSymbolAmbiguous        ConflictCode = "symbol_ambiguous"
	ConflictSymbolMoved            ConflictCode = "symbol_moved"
	ConflictSymbolSignatureChanged ConflictCode = "symbol_signature_changed"
	ConflictFormatOnlyRelocation   ConflictCode = "format_only_relocation"
)

type Conflict struct {
	Code     ConflictCode
	Path     string
	Expected RevisionID
	Current  RevisionID
}

func (c *Conflict) Error() string {
	switch c.Code {
	case ConflictWorkspaceIDRequired:
		return "modern mutation requires workspace_id"
	case ConflictWorkspaceIDMismatch:
		return "workspace_id does not identify this workspace"
	case ConflictRevisionRequired:
		return "modern mutation requires an explicit document revision"
	default:
		return fmt.Sprintf("%s: %s changed from %s to %s", c.Code, c.Path, c.Expected, c.Current)
	}
}

type cachedDocument struct {
	disk        DiskSnapshot
	layer       ProviderLayer
	contentHash string
	signature   string
	revision    RevisionID
}

type Workspace struct {
	mu        sync.Mutex
	identity  Identity
	documents map[string]cachedDocument
	revisions map[RevisionID]DocumentSnapshot
	allowlist map[string]struct{}
	limits    Limits
	stateDir  string
	sectioner Sectioner
	failures  []EnvironmentFailure

	handlesMu sync.Mutex
	handles   *handleStore
	git       *gitState

	plansMu sync.Mutex
	plans   map[string]PlanRecord

	prepareMu  sync.Mutex
	activePlan string
}

func New(kind Kind, root string, providerEpoch uint64) (*Workspace, error) {
	return newWorkspace(OpenOptions{Kind: kind, Root: root, ProviderEpoch: providerEpoch})
}

func newID() (ID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("workspace id: %w", err)
	}
	return ID("ws_" + hex.EncodeToString(raw[:])), nil
}

func validID(id ID) bool {
	const prefix = "ws_"
	value := string(id)
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 {
		return false
	}
	decoded, err := hex.DecodeString(value[len(prefix):])
	return err == nil && len(decoded) == 16
}

func (w *Workspace) Identity() Identity {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.identity
}

func (w *Workspace) SyncProviderEpoch(epoch uint64) Identity {
	w.mu.Lock()
	defer w.mu.Unlock()
	if epoch != w.identity.Epoch {
		w.identity.Epoch = epoch
		w.identity.StateSeq++
	}
	return w.identity
}

func (w *Workspace) Snapshot(path string, layer ProviderLayer) (DocumentSnapshot, error) {
	return w.snapshot(path, layer, false)
}

func (w *Workspace) Refresh(path string, layer ProviderLayer) (DocumentSnapshot, error) {
	return w.snapshot(path, layer, true)
}

func (w *Workspace) snapshot(path string, layer ProviderLayer, forceHash bool) (DocumentSnapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	absolute, err := w.confinedPath(path)
	if err != nil {
		return DocumentSnapshot{}, err
	}
	previous, hadPrevious := w.documents[absolute]
	disk, content, err := inspectPath(absolute)
	if err != nil {
		return DocumentSnapshot{}, err
	}
	layer = cloneLayer(layer)

	contentHash := ""
	reuseHash := hadPrevious && !forceHash && layer.Content == nil &&
		equalDisk(previous.disk, disk)
	if reuseHash {
		contentHash = previous.contentHash
	} else {
		switch {
		case layer.Content != nil:
			contentHash = hashBytes(layer.Content)
		case content != nil:
			contentHash = hashBytes(content)
		}
	}
	signature := documentSignature(disk, layer, contentHash)
	if hadPrevious && signature != previous.signature {
		w.identity.StateSeq++
	}

	snapshot := DocumentSnapshot{
		Workspace:     w.identity,
		URI:           (&url.URL{Scheme: "file", Path: absolute}).String(),
		ContentSHA256: contentHash,
		Disk:          disk,
		ChangedTick:   layer.ChangedTick,
		Dirty:         layer.Dirty,
		LSPVersions:   cloneVersions(layer.LSPVersions),
	}
	snapshot.Revision = revisionFor(snapshot)
	w.documents[absolute] = cachedDocument{
		disk:        disk,
		layer:       layer,
		contentHash: contentHash,
		signature:   signature,
		revision:    snapshot.Revision,
	}
	w.revisions[snapshot.Revision] = snapshot
	return snapshot, nil
}

func (w *Workspace) ValidateMutation(workspaceID ID, path string, expected RevisionID, layer ProviderLayer) (DocumentSnapshot, error) {
	if workspaceID == "" {
		return DocumentSnapshot{}, &Conflict{Code: ConflictWorkspaceIDRequired}
	}
	if expected == "" {
		return DocumentSnapshot{}, &Conflict{Code: ConflictRevisionRequired}
	}

	w.mu.Lock()
	identity := w.identity
	prior, known := w.revisions[expected]
	w.mu.Unlock()
	if workspaceID != identity.ID {
		return DocumentSnapshot{}, &Conflict{Code: ConflictWorkspaceIDMismatch}
	}

	current, err := w.Refresh(path, layer)
	if err != nil {
		return DocumentSnapshot{}, err
	}
	if known && prior.Workspace.Epoch != current.Workspace.Epoch {
		return current, &Conflict{
			Code: ConflictWorkspaceEpoch, Path: path, Expected: expected, Current: current.Revision,
		}
	}
	if current.Revision != expected {
		code := ConflictDocumentChanged
		if known && prior.Disk.Kind != ObjectMissing && current.Disk.Kind == ObjectMissing {
			code = ConflictDocumentDeleted
		}
		return current, &Conflict{Code: code, Path: path, Expected: expected, Current: current.Revision}
	}
	return current, nil
}

func (w *Workspace) confinedPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("document path is required")
	}
	absolute := path
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(w.identity.Root, absolute)
	}
	absolute = filepath.Clean(absolute)
	rel, err := filepath.Rel(w.identity.Root, absolute)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("document %s is outside workspace root %s", absolute, w.identity.Root)
	}
	if w.identity.Kind == KindDocuments {
		if _, allowed := w.allowlist[absolute]; !allowed {
			return "", fmt.Errorf("document %s is not in the workspace allowlist", absolute)
		}
	}
	return absolute, nil
}

func inspectPath(path string) (DiskSnapshot, []byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return DiskSnapshot{Kind: ObjectMissing}, nil, nil
		}
		if err != nil {
			return DiskSnapshot{}, nil, err
		}
		disk := diskSnapshot(info)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, readErr := os.Readlink(path)
			if readErr != nil {
				continue
			}
			disk.Kind = ObjectSymlink
			disk.SymlinkTarget = target
			return disk, []byte(target), nil
		case info.IsDir():
			disk.Kind = ObjectDirectory
			return disk, nil, nil
		case info.Mode().IsRegular():
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			after, statErr := os.Lstat(path)
			if statErr != nil || !sameFileObservation(info, after) {
				continue
			}
			if !utf8.Valid(content) || containsNUL(content) {
				disk.Kind = ObjectBinary
			} else {
				disk.Kind = ObjectRegularText
			}
			return disk, content, nil
		default:
			return DiskSnapshot{}, nil, fmt.Errorf("unsupported filesystem object at %s: %s", path, info.Mode())
		}
	}
	return DiskSnapshot{}, nil, fmt.Errorf("document changed repeatedly while it was being inspected: %s", path)
}

func diskSnapshot(info os.FileInfo) DiskSnapshot {
	device, inode := fileIdentity(info)
	return DiskSnapshot{
		Device:  device,
		Inode:   inode,
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
		Mode:    uint32(info.Mode()),
	}
}

func sameFileObservation(before, after os.FileInfo) bool {
	if before.Mode() != after.Mode() || before.Size() != after.Size() ||
		before.ModTime() != after.ModTime() {
		return false
	}
	beforeDevice, beforeInode := fileIdentity(before)
	afterDevice, afterInode := fileIdentity(after)
	return beforeDevice == afterDevice && beforeInode == afterInode
}

func containsNUL(content []byte) bool {
	for _, b := range content {
		if b == 0 {
			return true
		}
	}
	return false
}

func equalDisk(left, right DiskSnapshot) bool {
	return left == right
}

func cloneLayer(layer ProviderLayer) ProviderLayer {
	layer.Content = append([]byte(nil), layer.Content...)
	layer.LSPVersions = cloneVersions(layer.LSPVersions)
	return layer
}

func cloneVersions(versions map[string]int64) map[string]int64 {
	if len(versions) == 0 {
		return nil
	}
	copy := make(map[string]int64, len(versions))
	for provider, version := range versions {
		copy[provider] = version
	}
	return copy
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func documentSignature(disk DiskSnapshot, layer ProviderLayer, contentHash string) string {
	value := struct {
		Disk          DiskSnapshot     `json:"disk"`
		ContentSHA256 string           `json:"content_sha256"`
		ChangedTick   uint64           `json:"changedtick"`
		Dirty         bool             `json:"dirty"`
		LSPVersions   map[string]int64 `json:"lsp_versions,omitempty"`
	}{
		Disk: disk, ContentSHA256: contentHash, ChangedTick: layer.ChangedTick,
		Dirty: layer.Dirty, LSPVersions: layer.LSPVersions,
	}
	encoded, _ := json.Marshal(value)
	return hashBytes(encoded)
}

func revisionFor(snapshot DocumentSnapshot) RevisionID {
	value := struct {
		WorkspaceID   ID               `json:"workspace_id"`
		Epoch         uint64           `json:"epoch"`
		URI           string           `json:"uri"`
		ContentSHA256 string           `json:"content_sha256"`
		Disk          DiskSnapshot     `json:"disk"`
		ChangedTick   uint64           `json:"changedtick"`
		Dirty         bool             `json:"dirty"`
		LSPVersions   map[string]int64 `json:"lsp_versions,omitempty"`
	}{
		WorkspaceID: snapshot.Workspace.ID, Epoch: snapshot.Workspace.Epoch,
		URI: snapshot.URI, ContentSHA256: snapshot.ContentSHA256, Disk: snapshot.Disk,
		ChangedTick: snapshot.ChangedTick, Dirty: snapshot.Dirty, LSPVersions: snapshot.LSPVersions,
	}
	encoded, _ := json.Marshal(value)
	return RevisionID("docrev_" + hashBytes(encoded))
}
