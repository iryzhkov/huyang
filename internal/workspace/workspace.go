package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
	// Detail carries an optional human-readable qualification of the code, for
	// example why an expected revision could not be attributed to this
	// service instance.
	Detail string
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
		message := fmt.Sprintf("%s: %s changed from %s to %s", c.Code, c.Path, c.Expected, c.Current)
		if c.Expected == c.Current && c.Expected != "" {
			// The document did not change: the bytes this target named were
			// rewritten by an earlier operation of the same plan. Printing
			// one revision twice sent a reader looking for an external
			// writer that does not exist.
			message = fmt.Sprintf("%s: %s is still at %s, but the bytes this target named were replaced by an earlier operation in the same plan",
				c.Code, c.Path, c.Expected)
		}
		if c.Detail != "" {
			message += " (" + c.Detail + ")"
		}
		return message
	}
}

// maxRevisionsPerDocument bounds how many historical snapshots the workspace
// keeps for one document. Revision tokens derive from content, so a handle
// issued at an older revision still validates whenever the bytes are back;
// the history only has to be deep enough to classify a conflict as a content
// change rather than an unknown revision for the handles a caller is likely
// to still hold.
const maxRevisionsPerDocument = 32

const unknownRevisionDetail = "expected revision is not known to this service instance: it was issued before a restart, under an earlier provider epoch, or before the retained revision history"

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
	// revisionHistory keeps the insertion order of revisions per absolute
	// path so that revisions can be pruned oldest-first.
	revisionHistory map[string][]RevisionID
	knownPaths      map[string]struct{}
	pathsPrimed     bool
	allowlist       map[string]struct{}
	limits          Limits
	stateDir        string
	sectioner       Sectioner
	failures        []EnvironmentFailure

	tracesMu sync.Mutex
	traces   *executionTraceStore

	handlesMu   sync.Mutex
	handles     *handleStore
	git         *gitState
	diagnostics *diagnosticStore

	plansMu sync.Mutex
	plans   map[string]PlanRecord

	prepareMu   sync.Mutex
	activePlans map[string]struct{}
	commitFault func(point, path string) error
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

func (w *Workspace) RefreshKnownDocuments() (Identity, error) {
	w.mu.Lock()
	layers := make(map[string]ProviderLayer, len(w.documents))
	for path, document := range w.documents {
		layers[path] = cloneLayer(document.layer)
	}
	w.mu.Unlock()

	paths := make([]string, 0, len(layers))
	for path := range layers {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, err := w.Refresh(path, layers[path]); err != nil {
			return w.Identity(), err
		}
	}
	return w.Identity(), nil
}

// PrimeDocuments records a bounded baseline for every path currently visible
// in the workspace. It lets later read-only refreshes distinguish an external
// filesystem change from the first observation of a file, so revision history
// can expose the otherwise-uncovered gap.
func (w *Workspace) PrimeDocuments() error {
	files, _, err := w.collectFiles()
	if err != nil {
		return err
	}
	current := make(map[string]struct{}, len(files))
	for _, path := range files {
		current[path] = struct{}{}
	}
	w.mu.Lock()
	inventoryChanged := false
	if w.pathsPrimed {
		for path := range current {
			if _, known := w.knownPaths[path]; !known {
				inventoryChanged = true
				break
			}
		}
		if !inventoryChanged {
			for path := range w.knownPaths {
				if _, exists := current[path]; !exists {
					inventoryChanged = true
					break
				}
			}
		}
	}
	if inventoryChanged {
		// The inventory changed outside a Huyang mutation. There is no exact
		// native diff for the uncovered transition, so advance once and let
		// revision_diff expose the resulting external gap.
		w.identity.StateSeq++
	}
	w.knownPaths = current
	w.pathsPrimed = true
	w.mu.Unlock()
	for _, path := range files {
		if _, err := w.Snapshot(path, ProviderLayer{}); err != nil {
			return err
		}
	}
	return nil
}

func (w *Workspace) Refresh(path string, layer ProviderLayer) (DocumentSnapshot, error) {
	return w.snapshot(path, layer, true)
}

func (w *Workspace) snapshot(path string, layer ProviderLayer, forceHash bool) (DocumentSnapshot, error) {
	// Path confinement, disk inspection and hashing happen outside the
	// workspace lock so concurrent readers are not serialised behind disk
	// I/O. The lock is retaken only to publish the observation, with a
	// recheck against whatever was recorded meanwhile.
	absolute, err := w.confinedPath(path)
	if err != nil {
		return DocumentSnapshot{}, err
	}
	w.mu.Lock()
	previous, hadPrevious := w.documents[absolute]
	w.mu.Unlock()

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

	w.mu.Lock()
	// Compare against the latest record rather than the one read before the
	// I/O: a concurrent snapshot may already have published this change, and
	// one external change must advance the state sequence exactly once.
	latest, hadLatest := w.documents[absolute]
	if hadLatest && signature != latest.signature {
		w.identity.StateSeq++
	}
	contentChanged := hadLatest && latest.contentHash != contentHash

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
	w.rememberRevisionLocked(absolute, snapshot)
	w.mu.Unlock()
	if contentChanged {
		// Findings recorded for the previous content can no longer be
		// reported as current. This touches the ledger's own lock and disk,
		// so it runs after the workspace lock is released.
		if err := w.noteDocumentContentChanged(absolute, snapshot.Revision); err != nil {
			return snapshot, fmt.Errorf("update diagnostic ledger for %s: %w", absolute, err)
		}
	}
	return snapshot, nil
}

// rememberRevisionLocked records a snapshot under its revision token and
// prunes the oldest snapshots of the same document beyond
// maxRevisionsPerDocument. The caller holds w.mu.
func (w *Workspace) rememberRevisionLocked(absolute string, snapshot DocumentSnapshot) {
	if w.revisionHistory == nil {
		w.revisionHistory = make(map[string][]RevisionID)
	}
	history := w.revisionHistory[absolute]
	for index, id := range history {
		if id == snapshot.Revision {
			history = append(history[:index], history[index+1:]...)
			break
		}
	}
	history = append(history, snapshot.Revision)
	for len(history) > maxRevisionsPerDocument {
		delete(w.revisions, history[0])
		history = history[1:]
	}
	w.revisionHistory[absolute] = history
	w.revisions[snapshot.Revision] = snapshot
}

// RevisionHistoryLength reports how many snapshots are retained for a
// document. It exists for tests and inspection of retention behaviour.
func (w *Workspace) RevisionHistoryLength(path string) int {
	absolute, err := w.confinedPath(path)
	if err != nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.revisionHistory[absolute])
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
		// The revision token derives from content, so an unchanged document
		// matched above even if the service never saw the token before. A
		// token that neither matches nor is retained cannot be attributed to
		// a content change: it was issued by an earlier service instance,
		// under another epoch, or before the retained history. Report that as
		// an epoch conflict with a detail instead of claiming the content
		// changed.
		if !known {
			return current, &Conflict{
				Code: ConflictWorkspaceEpoch, Path: path, Expected: expected, Current: current.Revision,
				Detail: unknownRevisionDetail,
			}
		}
		code := ConflictDocumentChanged
		if prior.Disk.Kind != ObjectMissing && current.Disk.Kind == ObjectMissing {
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
	if err := w.parentChainStaysInside(absolute, rel); err != nil {
		return "", err
	}
	if w.identity.Kind == KindDocuments {
		if _, allowed := w.allowlist[absolute]; !allowed {
			return "", fmt.Errorf("document %s is not in the workspace allowlist", absolute)
		}
	}
	return absolute, nil
}

// parentChainStaysInside walks every directory component between the root
// and the final path element with Lstat. A symlinked component is allowed
// only when it resolves inside the (already symlink-resolved) root; a link
// that leaves the root is refused, because a lexical check alone would let
// a read or write pass through it into a foreign tree. The final element is
// left alone: inspectPath observes it with Lstat and treats a symlink as an
// object of its own, so a write never follows it. Components that do not
// exist yet cannot be symlinks and end the walk.
func (w *Workspace) parentChainStaysInside(absolute, rel string) error {
	if rel == "." {
		return nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	current := w.identity.Root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return fmt.Errorf("document %s passes through unresolvable symlink %s: %w", absolute, current, err)
		}
		if !insidePath(w.identity.Root, resolved) {
			return fmt.Errorf("document %s passes through symlink %s that leaves workspace root %s", absolute, current, w.identity.Root)
		}
		// Continue from the real directory so later components are judged
		// where they actually live.
		current = resolved
	}
	return nil
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

// contentIdentity is the part of a disk observation that describes what the
// object is rather than where or when it was stored. Device, inode, size and
// mtime are cheap change detectors used to skip re-hashing; they never enter
// the revision token, so a touch, an atomic editor save or a checkout of
// identical bytes keeps the revision and every handle issued against it.
type contentIdentity struct {
	Kind          ObjectKind `json:"kind"`
	Mode          uint32     `json:"mode"`
	SymlinkTarget string     `json:"symlink_target,omitempty"`
}

func contentIdentityOf(disk DiskSnapshot) contentIdentity {
	return contentIdentity{Kind: disk.Kind, Mode: disk.Mode, SymlinkTarget: disk.SymlinkTarget}
}

func documentSignature(disk DiskSnapshot, layer ProviderLayer, contentHash string) string {
	value := struct {
		Object        contentIdentity  `json:"object"`
		ContentSHA256 string           `json:"content_sha256"`
		ChangedTick   uint64           `json:"changedtick"`
		Dirty         bool             `json:"dirty"`
		LSPVersions   map[string]int64 `json:"lsp_versions,omitempty"`
	}{
		Object: contentIdentityOf(disk), ContentSHA256: contentHash, ChangedTick: layer.ChangedTick,
		Dirty: layer.Dirty, LSPVersions: layer.LSPVersions,
	}
	encoded, _ := json.Marshal(value)
	return hashBytes(encoded)
}

// revisionFor derives the optimistic-concurrency token for a snapshot. The
// token covers workspace identity, provider epoch, URI, object kind and mode,
// the content hash and the provider layer. It deliberately excludes
// filesystem metadata; see contentIdentity.
func revisionFor(snapshot DocumentSnapshot) RevisionID {
	value := struct {
		WorkspaceID   ID               `json:"workspace_id"`
		Epoch         uint64           `json:"epoch"`
		URI           string           `json:"uri"`
		Object        contentIdentity  `json:"object"`
		ContentSHA256 string           `json:"content_sha256"`
		ChangedTick   uint64           `json:"changedtick"`
		Dirty         bool             `json:"dirty"`
		LSPVersions   map[string]int64 `json:"lsp_versions,omitempty"`
	}{
		WorkspaceID: snapshot.Workspace.ID, Epoch: snapshot.Workspace.Epoch,
		URI: snapshot.URI, Object: contentIdentityOf(snapshot.Disk), ContentSHA256: snapshot.ContentSHA256,
		ChangedTick: snapshot.ChangedTick, Dirty: snapshot.Dirty, LSPVersions: snapshot.LSPVersions,
	}
	encoded, _ := json.Marshal(value)
	return RevisionID("docrev_" + hashBytes(encoded))
}

// atomicWriteFile writes data to path through a same-directory temporary
// file, fsyncs the file, renames it into place and fsyncs the parent
// directory so the replacement is durable before the call returns. It is the
// single implementation every state file, native mutation and native recovery
// write in this package uses; commit postimages share its core through
// commitWrite, which adds the no-replace variant and the wider mode mask.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if err := writeTempAndRename(path, data, mode.Perm(), true); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

// writeTempAndRename is the temp-write core: the temporary file is created
// beside path, written, chmoded to exactly mode, fsynced and closed, then
// renamed over path (replace) or moved into place only if nothing exists there
// (no replace). The temporary file is removed on every failure. The parent
// directory is not synced here.
func writeTempAndRename(path string, content []byte, mode fs.FileMode, replace bool) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".huyang-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	remove := true
	defer func() {
		_ = temp.Close()
		if remove {
			_ = os.Remove(name)
		}
	}()
	if _, err := temp.Write(content); err != nil {
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if replace {
		if err := os.Rename(name, path); err != nil {
			return err
		}
	} else if err := renameNoReplace(name, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return clobberRefusal(path)
		}
		return err
	}
	remove = false
	return nil
}
