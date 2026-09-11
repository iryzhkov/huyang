package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Idempotency receipts. A stateful call leaves a receipt keyed by workspace,
// tool and caller idempotency key so a retry replays the original result
// instead of mutating again. Receipts were once stored whole inside
// registry.json, which grew to hundreds of megabytes and was rewritten with
// fsync on every mutation. They now live in one bounded file per workspace,
// keep their full payload only for a short window, and are capped by count
// per workspace and by total bytes; a receipt evicted by the caps leaves a
// tombstone so a late retry is refused rather than re-executed.

const (
	legacyReceiptTrimBytes   = 64 << 10
	receiptDirectoryName     = "receipts"
	receiptFileVersion       = 1
	receiptEvictedCode       = "idempotency_receipt_evicted"
	receiptEvictedSummary    = "The original receipt for this idempotency key was evicted by the retention caps; the call is not replayed and was not re-executed"
	receiptTrimmedWarning    = "The replayed receipt was trimmed to its provenance fields by retention; evidence detail is available through evidence_get"
	receiptTrimmedDataMarker = "receipt_trimmed"
)

// receiptLimits bounds the receipt store. PayloadWindow is how long a receipt
// keeps its full result before being trimmed to provenance fields.
type receiptLimits struct {
	PerWorkspace  int
	TotalBytes    int
	Tombstones    int
	PayloadWindow time.Duration
}

func defaultReceiptLimits() receiptLimits {
	return receiptLimits{PerWorkspace: 256, TotalBytes: 32 << 20, Tombstones: 4096, PayloadWindow: 15 * time.Minute}
}

type persistedReceiptFile struct {
	Version  int
	Receipts []persistedReplay
}

type persistedReplay struct {
	Key           string
	ArgumentsHash string
	CompletedAt   time.Time
	Evicted       bool `json:",omitempty"`
	Result        map[string]any
}

type directReplay struct {
	argumentsHash string
	result        map[string]any
	done          chan struct{}
	complete      bool
	checkpointed  bool
	// evicted marks a tombstone: the receipt existed, the caps removed its
	// payload, and a retry with the same key must not run again.
	evicted     bool
	trimmed     bool
	completedAt time.Time
	bytes       int
}

// inFlight reports whether the request behind the receipt has not finished;
// such a receipt is never trimmed or evicted.
func (r *directReplay) inFlight() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

func replayWorkspace(key string) workspacecore.ID {
	workspace, _, _ := strings.Cut(key, "\x00")
	return workspacecore.ID(workspace)
}

func (d *directWorkspaces) receiptPath(workspaceID workspacecore.ID) string {
	return filepath.Join(d.stateDir, receiptDirectoryName, string(workspaceID)+".json")
}

func receiptSize(result map[string]any) int {
	encoded, err := json.Marshal(result)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// trimReceipt reduces a receipt to what a replay and the revision provenance
// queries need: the envelope identity, the outcome, the evidence IDs, and
// the changed paths and exact diff hashes of a canonical mutation. Bulky
// plan records, verification output and diff bodies are dropped.
func trimReceipt(result map[string]any) map[string]any {
	trimmed := make(map[string]any, 12)
	for _, key := range []string{"api_version", "request_id", "outcome", "code", "summary", "workspace", "transaction", "evidence", "idempotency", "idempotency_persisted"} {
		if value, ok := result[key]; ok {
			trimmed[key] = value
		}
	}
	trimmed["warnings"] = []string{}
	trimmed["next"] = []any{}
	data, _ := result["data"].(map[string]any)
	compact := map[string]any{receiptTrimmedDataMarker: true}
	for _, key := range []string{"canonical_changed", "from_revision", "revision", "document_revision", "changed_paths", "applied_from_provisional"} {
		if value, ok := data[key]; ok {
			compact[key] = value
		}
	}
	if change, ok := data["change"].(map[string]any); ok {
		if diff, ok := change["diff"].(map[string]any); ok {
			compact["change"] = map[string]any{"diff": compactDiffReceipt(diff)}
		}
	}
	var committed []any
	switch plan := data["plan"].(type) {
	case workspacecore.PlanRecord:
		if plan.Preparation != nil {
			for _, diff := range plan.Preparation.CommittedDiffs {
				committed = append(committed, compactDiffReceipt(exactDiffReceiptMap(diff)))
			}
		}
	case map[string]any:
		preparation, _ := plan["preparation"].(map[string]any)
		switch diffs := preparation["committed_diffs"].(type) {
		case []any:
			for _, raw := range diffs {
				if diff, ok := raw.(map[string]any); ok {
					committed = append(committed, compactDiffReceipt(diff))
				}
			}
		case []workspacecore.ExactDiff:
			for _, diff := range diffs {
				committed = append(committed, compactDiffReceipt(exactDiffReceiptMap(diff)))
			}
		}
	}
	if len(committed) > 0 {
		compact["plan"] = map[string]any{"preparation": map[string]any{"committed_diffs": committed}}
	}
	trimmed["data"] = compact
	return trimmed
}

func compactDiffReceipt(diff map[string]any) map[string]any {
	return map[string]any{
		"path": diff["path"], "before_sha256": diff["before_sha256"],
		"after_sha256": diff["after_sha256"], "patch": diff["patch"],
	}
}

func receiptIsTrimmed(result map[string]any) bool {
	data, _ := result["data"].(map[string]any)
	trimmed, _ := data[receiptTrimmedDataMarker].(bool)
	return trimmed
}

// storeReceipt records a completed receipt, applies the retention caps and
// persists the workspace's receipt file. It is the only writer of receipts.
func (d *directWorkspaces) storeReceipt(key string, replay *directReplay, result map[string]any) error {
	d.replayMu.Lock()
	replay.result = result
	replay.complete = true
	replay.completedAt = time.Now()
	replay.bytes = receiptSize(result)
	d.replays[key] = replay
	d.applyReceiptCapsLocked(time.Now())
	d.replayMu.Unlock()
	return d.persistReceipts(replayWorkspace(key))
}

// applyReceiptCapsLocked trims payloads outside the window, evicts the
// oldest completed receipts beyond the per-workspace count and the total
// byte budget, and bounds the tombstones. In-flight receipts are never
// touched. Caller holds d.replayMu.
func (d *directWorkspaces) applyReceiptCapsLocked(now time.Time) {
	type entry struct {
		key    string
		replay *directReplay
	}
	byWorkspace := map[workspacecore.ID][]entry{}
	totalBytes := 0
	for key, replay := range d.replays {
		if !replay.complete || replay.evicted || replay.inFlight() {
			continue
		}
		if !replay.trimmed && now.Sub(replay.completedAt) > d.receiptLimits.PayloadWindow {
			replay.result = trimReceipt(replay.result)
			replay.trimmed = true
			replay.bytes = receiptSize(replay.result)
		}
		workspace := replayWorkspace(key)
		byWorkspace[workspace] = append(byWorkspace[workspace], entry{key: key, replay: replay})
		totalBytes += replay.bytes
	}
	evict := func(item entry) {
		item.replay.result = nil
		item.replay.evicted = true
		totalBytes -= item.replay.bytes
		item.replay.bytes = 0
	}
	var all []entry
	for _, entries := range byWorkspace {
		sort.Slice(entries, func(i, j int) bool { return entries[i].replay.completedAt.Before(entries[j].replay.completedAt) })
		for len(entries) > d.receiptLimits.PerWorkspace {
			evict(entries[0])
			entries = entries[1:]
		}
		all = append(all, entries...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].replay.completedAt.Before(all[j].replay.completedAt) })
	for index := 0; totalBytes > d.receiptLimits.TotalBytes && index < len(all); index++ {
		evict(all[index])
	}
	tombstones := map[workspacecore.ID][]entry{}
	for key, replay := range d.replays {
		if replay.evicted {
			workspace := replayWorkspace(key)
			tombstones[workspace] = append(tombstones[workspace], entry{key: key, replay: replay})
		}
	}
	for _, entries := range tombstones {
		sort.Slice(entries, func(i, j int) bool { return entries[i].replay.completedAt.Before(entries[j].replay.completedAt) })
		for len(entries) > d.receiptLimits.Tombstones {
			delete(d.replays, entries[0].key)
			entries = entries[1:]
		}
	}
}

// persistReceipts rewrites the bounded receipt file of one workspace. Only
// that file changes; the registry of workspaces is untouched.
func (d *directWorkspaces) persistReceipts(workspaceID workspacecore.ID) error {
	d.replayMu.Lock()
	receipts := make([]persistedReplay, 0)
	for key, replay := range d.replays {
		if !replay.complete || replayWorkspace(key) != workspaceID {
			continue
		}
		record := persistedReplay{Key: key, ArgumentsHash: replay.argumentsHash, CompletedAt: replay.completedAt, Evicted: replay.evicted}
		if !replay.evicted {
			record.Result = cloneEnvelope(replay.result)
		}
		receipts = append(receipts, record)
	}
	d.replayMu.Unlock()
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].Key < receipts[j].Key })
	content, err := json.Marshal(persistedReceiptFile{Version: receiptFileVersion, Receipts: receipts})
	if err != nil {
		return fmt.Errorf("encode receipts: %w", err)
	}
	d.persistMu.Lock()
	defer d.persistMu.Unlock()
	return writeDurableFile(d.receiptPath(workspaceID), append(content, '\n'))
}

// loadReceipts restores every workspace receipt file. Receipts whose payload
// exceeds the legacy trim size are trimmed on load and the caps are applied
// afterwards; nothing here refuses to start.
func (d *directWorkspaces) loadReceipts() error {
	entries, err := os.ReadDir(filepath.Join(d.stateDir, receiptDirectoryName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read receipts: %w", err)
	}
	loaded, dropped := 0, 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(d.stateDir, receiptDirectoryName, entry.Name()))
		if err != nil {
			return fmt.Errorf("read receipts: %w", err)
		}
		var file persistedReceiptFile
		if err := json.Unmarshal(content, &file); err != nil || file.Version != receiptFileVersion {
			dropped++
			log.Printf("huyang: dropping unreadable receipt file %s", entry.Name())
			continue
		}
		loadedHere, droppedHere := d.adoptReceipts(file.Receipts)
		loaded += loadedHere
		dropped += droppedHere
	}
	if dropped > 0 {
		log.Printf("huyang: loaded %d idempotency receipts, dropped %d", loaded, dropped)
	}
	return nil
}

// adoptReceipts installs persisted receipts in memory, trimming oversized
// payloads and skipping provider-backed receipts whose sandbox no longer
// exists. It returns the counts of adopted and dropped receipts.
func (d *directWorkspaces) adoptReceipts(receipts []persistedReplay) (int, int) {
	loaded, dropped := 0, 0
	d.replayMu.Lock()
	defer d.replayMu.Unlock()
	for _, receipt := range receipts {
		if receipt.Key == "" || (!receipt.Evicted && providerBackedReplay(receipt.Result)) {
			dropped++
			continue
		}
		replay := &directReplay{
			argumentsHash: receipt.ArgumentsHash, done: closedSignal(), complete: true,
			evicted: receipt.Evicted, completedAt: receipt.CompletedAt,
		}
		if !receipt.Evicted {
			replay.result = receipt.Result
			replay.bytes = receiptSize(receipt.Result)
			replay.trimmed = receiptIsTrimmed(receipt.Result)
			if !replay.trimmed && (replay.bytes > legacyReceiptTrimBytes || receipt.CompletedAt.IsZero()) {
				replay.result = trimReceipt(receipt.Result)
				replay.trimmed = true
				replay.bytes = receiptSize(replay.result)
			}
		}
		d.replays[receipt.Key] = replay
		loaded++
	}
	d.applyReceiptCapsLocked(time.Now())
	return loaded, dropped
}

// migrateLegacyReceipts moves the receipts a version 1 registry embedded
// into per-workspace files, trimming every payload because version 1 kept no
// completion time, and reports how many were kept and dropped.
func (d *directWorkspaces) migrateLegacyReceipts(replays []persistedReplay) error {
	if len(replays) == 0 {
		return nil
	}
	loaded, dropped := d.adoptReceipts(replays)
	log.Printf("huyang: migrated %d legacy idempotency receipts out of registry.json, dropped %d", loaded, dropped)
	workspaces := map[workspacecore.ID]bool{}
	for _, replay := range replays {
		workspaces[replayWorkspace(replay.Key)] = true
	}
	for workspaceID := range workspaces {
		if workspaceID == "" {
			continue
		}
		if err := d.persistReceipts(workspaceID); err != nil {
			return err
		}
	}
	return nil
}

// writeDurableFile writes content through a mode-0600 temporary file, file
// sync, rename and directory sync.
func writeDurableFile(path string, content []byte) error {
	directoryPath := filepath.Dir(path)
	if err := os.MkdirAll(directoryPath, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directoryPath, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
