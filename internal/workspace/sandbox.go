package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const sandboxMarkerVersion = 1

var ErrReflinkUnsupported = errors.New("sandbox reflink unsupported")

type SandboxLimits struct {
	MaxEntries       int
	MaxLogicalBytes  int64
	MaxSafeCopyBytes int64
	MinFreeBytes     uint64
	WallTime         time.Duration
}

func DefaultSandboxLimits() SandboxLimits {
	return SandboxLimits{
		MaxEntries: 200000, MaxLogicalBytes: 16 << 30, MaxSafeCopyBytes: 4 << 30,
		MinFreeBytes: 1 << 30, WallTime: 60 * time.Second,
	}
}

type SandboxMarker struct {
	Version      int       `json:"version"`
	WorkspaceID  ID        `json:"workspace_id"`
	PlanID       string    `json:"plan_id"`
	PlanRevision uint64    `json:"plan_revision"`
	BaseRevision string    `json:"base_revision"`
	Backend      string    `json:"backend"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"created_at"`
}

type sandboxEntry struct {
	Path   string
	Kind   ObjectKind
	Mode   fs.FileMode
	Size   int64
	Target string
	Hash   [sha256.Size]byte
}

type Sandbox struct {
	Base         string
	Root         string
	Tree         string
	Backend      string
	BaseRevision string
	WorkspaceID  ID
	PlanID       string
	PlanRevision uint64
	baseManifest []sandboxEntry
	cleanupMu    sync.Mutex
	cleaned      bool
}

func MaterializeSandbox(ctx context.Context, sourceRoot, sandboxBase string, workspaceID ID, planID string, planRevision uint64, baseRevision string, limits SandboxLimits) (*Sandbox, error) {
	if limits.MaxEntries <= 0 || limits.MaxLogicalBytes <= 0 || limits.MaxSafeCopyBytes <= 0 || limits.WallTime <= 0 {
		limits = DefaultSandboxLimits()
	}
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		return nil, err
	}
	sourceRoot, err = filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return nil, err
	}
	sandboxBase, err = filepath.Abs(sandboxBase)
	if err != nil {
		return nil, err
	}
	if insidePath(sourceRoot, sandboxBase) {
		return nil, errors.New("sandbox_destination_inside_source")
	}
	if err := os.MkdirAll(sandboxBase, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(sandboxBase, 0o700); err != nil {
		return nil, err
	}
	if available, err := sandboxFreeBytes(sandboxBase); err != nil {
		return nil, err
	} else if available < limits.MinFreeBytes {
		return nil, fmt.Errorf("sandbox_quota: %d free bytes is below %d-byte headroom", available, limits.MinFreeBytes)
	}
	timed, cancel := context.WithTimeout(ctx, limits.WallTime)
	defer cancel()
	manifest, logical, err := inventorySandboxTree(timed, sourceRoot, limits)
	if err != nil {
		return nil, err
	}
	sameDevice, err := sandboxSameDevice(sourceRoot, sandboxBase)
	if err != nil {
		return nil, err
	}
	if sameDevice {
		sandbox, cloneErr := materializeCandidate(timed, sourceRoot, sandboxBase, workspaceID, planID, planRevision, baseRevision, "reflink", manifest)
		if cloneErr == nil {
			return sandbox, nil
		}
		if !errors.Is(cloneErr, ErrReflinkUnsupported) {
			return nil, cloneErr
		}
	}
	if logical > limits.MaxSafeCopyBytes {
		return nil, fmt.Errorf("sandbox_quota: safe copy requires %d bytes, limit %d", logical, limits.MaxSafeCopyBytes)
	}
	return materializeCandidate(timed, sourceRoot, sandboxBase, workspaceID, planID, planRevision, baseRevision, "safe_copy", manifest)
}

func materializeCandidate(ctx context.Context, sourceRoot, sandboxBase string, workspaceID ID, planID string, planRevision uint64, baseRevision, backend string, manifest []sandboxEntry) (*Sandbox, error) {
	root, err := os.MkdirTemp(sandboxBase, "sandbox-")
	if err != nil {
		return nil, err
	}
	sandbox := &Sandbox{
		Base: sandboxBase, Root: root, Tree: filepath.Join(root, "tree"), Backend: backend,
		BaseRevision: baseRevision, WorkspaceID: workspaceID, PlanID: planID,
		PlanRevision: planRevision, baseManifest: append([]sandboxEntry(nil), manifest...),
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(root)
		}
	}()
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(sandbox.Tree, 0o700); err != nil {
		return nil, err
	}
	if err := sandbox.writeMarker("materializing"); err != nil {
		return nil, err
	}
	for _, entry := range manifest {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source := filepath.Join(sourceRoot, filepath.FromSlash(entry.Path))
		target := filepath.Join(sandbox.Tree, filepath.FromSlash(entry.Path))
		switch entry.Kind {
		case ObjectDirectory:
			if err := os.Mkdir(target, entry.Mode.Perm()); err != nil {
				return nil, err
			}
		case ObjectSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return nil, err
			}
			if err := os.Symlink(entry.Target, target); err != nil {
				return nil, err
			}
		case ObjectRegularText, ObjectBinary:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return nil, err
			}
			if backend == "reflink" {
				err = sandboxCloneFile(source, target, entry.Mode.Perm())
			} else {
				err = copySandboxFile(ctx, source, target, entry.Mode.Perm())
			}
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("sandbox_special_file: %s has unsupported kind %s", entry.Path, entry.Kind)
		}
	}
	current, _, err := inventorySandboxTree(ctx, sourceRoot, SandboxLimits{MaxEntries: len(manifest) + 1, MaxLogicalBytes: 1<<63 - 1})
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current, manifest) {
		return nil, errors.New("sandbox_source_changed: canonical tree changed during materialization")
	}
	copied, _, err := inventorySandboxTree(ctx, sandbox.Tree, SandboxLimits{MaxEntries: len(manifest) + 1, MaxLogicalBytes: 1<<63 - 1})
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(copied, manifest) {
		return nil, errors.New("sandbox_manifest_mismatch: destination does not equal canonical base")
	}
	if err := sandbox.writeMarker("sealed"); err != nil {
		return nil, err
	}
	failed = false
	return sandbox, nil
}

func inventorySandboxTree(ctx context.Context, root string, limits SandboxLimits) ([]sandboxEntry, int64, error) {
	var entries []sandboxEntry
	var logical int64
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if limits.MaxEntries > 0 && len(entries)+1 > limits.MaxEntries {
			return fmt.Errorf("sandbox_quota: entry limit %d exceeded", limits.MaxEntries)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("sandbox_path_escape")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := sandboxEntry{Path: filepath.ToSlash(relative), Mode: info.Mode(), Size: info.Size()}
		switch {
		case info.Mode().IsDir():
			entry.Kind = ObjectDirectory
		case info.Mode()&os.ModeSymlink != 0:
			entry.Kind = ObjectSymlink
			entry.Target, err = stableReadlink(path, info)
		case info.Mode().IsRegular():
			logical += info.Size()
			if limits.MaxLogicalBytes > 0 && logical > limits.MaxLogicalBytes {
				return fmt.Errorf("sandbox_quota: logical byte limit %d exceeded", limits.MaxLogicalBytes)
			}
			entry.Hash, err = stableFileHash(path, info)
			entry.Kind = ObjectRegularText
		default:
			return fmt.Errorf("sandbox_special_file: %s has unsupported mode %s", relative, info.Mode())
		}
		if err != nil {
			return err
		}
		entries = append(entries, entry)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool {
		di, dj := strings.Count(entries[i].Path, "/"), strings.Count(entries[j].Path, "/")
		if di != dj {
			return di < dj
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, logical, err
}

func stableFileHash(path string, before os.FileInfo) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	after, statErr := file.Stat()
	closeErr := file.Close()
	if copyErr != nil {
		return zero, copyErr
	}
	if statErr != nil {
		return zero, statErr
	}
	if closeErr != nil {
		return zero, closeErr
	}
	if !sameSandboxInfo(before, after) {
		return zero, errors.New("sandbox_source_changed: regular file changed while hashing")
	}
	copy(zero[:], hash.Sum(nil))
	return zero, nil
}

func stableReadlink(path string, before os.FileInfo) (string, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !sameSandboxInfo(before, after) {
		return "", errors.New("sandbox_source_changed: symlink changed while reading")
	}
	return target, nil
}

func sameSandboxInfo(a, b os.FileInfo) bool {
	adev, aino := fileIdentity(a)
	bdev, bino := fileIdentity(b)
	return adev == bdev && aino == bino && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().UnixNano() == b.ModTime().UnixNano()
}

func copySandboxFile(ctx context.Context, source, target string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	buffer := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			output.Close()
			return err
		}
		n, readErr := input.Read(buffer)
		if n > 0 {
			if _, err := output.Write(buffer[:n]); err != nil {
				output.Close()
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			output.Close()
			return readErr
		}
	}
	return output.Close()
}

func (s *Sandbox) ApplyPrepared(request PlanStageRequest) error {
	for _, file := range request.Files {
		path := filepath.Join(s.Tree, filepath.FromSlash(file.Path))
		if !insidePath(s.Tree, path) {
			return errors.New("sandbox_path_escape")
		}
		entry := CommitJournalEntry{Path: file.Path, Postimage: file.After, After: file.AfterDisk}
		if !file.AfterExists {
			entry.After = DiskSnapshot{Kind: ObjectMissing}
		}
		if err := applyCommitEntry(path, entry); err != nil {
			return err
		}
	}
	for _, file := range request.Files {
		path := filepath.Join(s.Tree, filepath.FromSlash(file.Path))
		current, content, err := inspectPath(path)
		expected := file.AfterDisk
		if !file.AfterExists {
			expected = DiskSnapshot{Kind: ObjectMissing}
		}
		matches := current.Kind == expected.Kind && current.Mode == expected.Mode &&
			current.Size == expected.Size && current.SymlinkTarget == expected.SymlinkTarget &&
			bytes.Equal(content, file.After)
		if err != nil || !matches {
			if err == nil {
				err = errors.New("prepared image differs")
			}
			return fmt.Errorf("sandbox_manifest_mismatch: %s: %w", file.Path, err)
		}
	}
	return s.writeMarker("prepared")
}

func (s *Sandbox) writeMarker(state string) error {
	marker := SandboxMarker{
		Version: sandboxMarkerVersion, WorkspaceID: s.WorkspaceID, PlanID: s.PlanID,
		PlanRevision: s.PlanRevision, BaseRevision: s.BaseRevision, Backend: s.Backend,
		State: state, CreatedAt: time.Now().UTC(),
	}
	content, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Root, "owner.json"), append(content, '\n'), 0o600)
}

func (s *Sandbox) Cleanup() error {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	if s.cleaned {
		return nil
	}
	if err := validateSandboxRoot(s.Base, s.Root, s.WorkspaceID, s.PlanID); err != nil {
		return err
	}
	if err := os.RemoveAll(s.Root); err != nil {
		return fmt.Errorf("cleanup_required: %w", err)
	}
	s.cleaned = true
	return nil
}

func ReapSandboxes(base string, referenced map[string]bool) error {
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "sandbox-") {
			continue
		}
		root := filepath.Join(base, entry.Name())
		content, err := os.ReadFile(filepath.Join(root, "owner.json"))
		if err != nil {
			continue
		}
		var marker SandboxMarker
		if json.Unmarshal(content, &marker) != nil || marker.Version != sandboxMarkerVersion || marker.PlanID == "" || referenced[marker.PlanID] {
			continue
		}
		if err := validateSandboxRoot(base, root, marker.WorkspaceID, marker.PlanID); err != nil {
			continue
		}
		if err := os.RemoveAll(root); err != nil {
			return fmt.Errorf("cleanup_required: %w", err)
		}
	}
	return nil
}

func validateSandboxRoot(base, root string, workspaceID ID, planID string) error {
	relative, err := filepath.Rel(base, root)
	if err != nil || relative == "." || strings.Contains(relative, string(filepath.Separator)) || strings.HasPrefix(relative, "..") {
		return errors.New("cleanup_required: sandbox root is not confined")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("cleanup_required: sandbox root is not an owned directory")
	}
	content, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil {
		return err
	}
	var marker SandboxMarker
	if err := json.Unmarshal(content, &marker); err != nil {
		return err
	}
	if marker.Version != sandboxMarkerVersion || marker.WorkspaceID != workspaceID || marker.PlanID != planID {
		return errors.New("cleanup_required: ownership marker mismatch")
	}
	return nil
}

func insidePath(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
