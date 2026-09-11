package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iryzhkov/huyang/internal/handlers"
	"github.com/iryzhkov/huyang/internal/mcpapi"
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

// receiptStore owns the idempotency receipts of every workspace: the
// in-memory replay table, the retention caps and the per-workspace receipt
// files. It is also the source of the revision provenance queries, because
// the receipts are the only durable record of which native mutation
// produced which workspace revision.
type receiptStore struct {
	mu       sync.Mutex
	replays  map[string]*directReplay
	limits   receiptLimits
	stateDir string

	// persistMu serialises writers of the receipt files.
	persistMu sync.Mutex
}

func newReceiptStore(stateDir string, limits receiptLimits) *receiptStore {
	return &receiptStore{replays: make(map[string]*directReplay), limits: limits, stateDir: stateDir}
}

// lookupOrBegin returns the receipt already recorded or in flight for key,
// or registers a pending receipt for this call when there is none. Exactly
// one of the results is non-nil.
func (s *receiptStore) lookupOrBegin(key, argumentsHash string) (existing, pending *directReplay) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.replays[key]; ok {
		return previous, nil
	}
	pending = &directReplay{argumentsHash: argumentsHash, done: make(chan struct{})}
	s.replays[key] = pending
	return nil, pending
}

// replaySnapshot reads what a replay of a completed receipt needs.
func (s *receiptStore) replaySnapshot(replay *directReplay) (evicted, trimmed bool, result map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return replay.evicted, replay.trimmed, replay.result
}

// checkpoint persists an early receipt for a mutation whose post-mutation
// work is still running, so a lost response can be replayed even if the
// service stops before the call returns.
func (s *receiptStore) checkpoint(key string, replay *directReplay, receipt map[string]any) error {
	s.mu.Lock()
	replay.checkpointed = true
	s.mu.Unlock()
	if err := s.storeReceipt(key, replay, receipt); err != nil {
		s.mu.Lock()
		replay.complete = false
		replay.checkpointed = false
		s.mu.Unlock()
		return err
	}
	return nil
}

// checkpointedResult returns the receipt persisted by checkpoint, if any.
func (s *receiptStore) checkpointedResult(replay *directReplay) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !replay.checkpointed {
		return nil, false
	}
	return replay.result, true
}

// abandon drops a pending receipt whose call did not complete, releasing
// any waiter for the same key.
func (s *receiptStore) abandon(key string, replay *directReplay) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.replays, key)
	close(replay.done)
}

// finish releases the waiters of a completed receipt; result, when set,
// replaces the stored payload first (used when persistence failed and the
// returned envelope carries the warning).
func (s *receiptStore) finish(replay *directReplay, result map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if result != nil {
		replay.result = result
	}
	close(replay.done)
}

// providerBackedReplay reports whether a receipt describes a plan whose
// sandbox and provider would be needed to continue; such receipts are not
// replayed after a restart because that state is gone.
func providerBackedReplay(result map[string]any) bool {
	transaction, _ := result["transaction"].(map[string]any)
	state, _ := transaction["state"].(string)
	switch workspacecore.PlanState(state) {
	case workspacecore.PlanPreparing, workspacecore.PlanReady, workspacecore.PlanProvisional, workspacecore.PlanRollingBack, workspacecore.PlanConflicted:
		return true
	default:
		return false
	}
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

func (s *receiptStore) receiptPath(workspaceID workspacecore.ID) string {
	return filepath.Join(s.stateDir, receiptDirectoryName, string(workspaceID)+".json")
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
func (s *receiptStore) storeReceipt(key string, replay *directReplay, result map[string]any) error {
	s.mu.Lock()
	replay.result = result
	replay.complete = true
	replay.completedAt = time.Now()
	replay.bytes = receiptSize(result)
	s.replays[key] = replay
	s.applyReceiptCapsLocked(time.Now())
	s.mu.Unlock()
	return s.persistReceipts(replayWorkspace(key))
}

// applyReceiptCapsLocked trims payloads outside the window, evicts the
// oldest completed receipts beyond the per-workspace count and the total
// byte budget, and bounds the tombstones. In-flight receipts are never
// touched. Caller holds s.mu.
func (s *receiptStore) applyReceiptCapsLocked(now time.Time) {
	type entry struct {
		key    string
		replay *directReplay
	}
	byWorkspace := map[workspacecore.ID][]entry{}
	totalBytes := 0
	for key, replay := range s.replays {
		if !replay.complete || replay.evicted || replay.inFlight() {
			continue
		}
		if !replay.trimmed && now.Sub(replay.completedAt) > s.limits.PayloadWindow {
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
		for len(entries) > s.limits.PerWorkspace {
			evict(entries[0])
			entries = entries[1:]
		}
		all = append(all, entries...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].replay.completedAt.Before(all[j].replay.completedAt) })
	for index := 0; totalBytes > s.limits.TotalBytes && index < len(all); index++ {
		evict(all[index])
	}
	tombstones := map[workspacecore.ID][]entry{}
	for key, replay := range s.replays {
		if replay.evicted {
			workspace := replayWorkspace(key)
			tombstones[workspace] = append(tombstones[workspace], entry{key: key, replay: replay})
		}
	}
	for _, entries := range tombstones {
		sort.Slice(entries, func(i, j int) bool { return entries[i].replay.completedAt.Before(entries[j].replay.completedAt) })
		for len(entries) > s.limits.Tombstones {
			delete(s.replays, entries[0].key)
			entries = entries[1:]
		}
	}
}

// persistReceipts rewrites the bounded receipt file of one workspace. Only
// that file changes; the registry of workspaces is untouched.
func (s *receiptStore) persistReceipts(workspaceID workspacecore.ID) error {
	s.mu.Lock()
	receipts := make([]persistedReplay, 0)
	for key, replay := range s.replays {
		if !replay.complete || replayWorkspace(key) != workspaceID {
			continue
		}
		record := persistedReplay{Key: key, ArgumentsHash: replay.argumentsHash, CompletedAt: replay.completedAt, Evicted: replay.evicted}
		if !replay.evicted {
			record.Result = mcpapi.CloneEnvelope(replay.result)
		}
		receipts = append(receipts, record)
	}
	s.mu.Unlock()
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].Key < receipts[j].Key })
	content, err := json.Marshal(persistedReceiptFile{Version: receiptFileVersion, Receipts: receipts})
	if err != nil {
		return fmt.Errorf("encode receipts: %w", err)
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	return writeDurableFile(s.receiptPath(workspaceID), append(content, '\n'))
}

// loadReceipts restores every workspace receipt file. Receipts whose payload
// exceeds the legacy trim size are trimmed on load and the caps are applied
// afterwards; nothing here refuses to start.
func (s *receiptStore) loadReceipts() error {
	entries, err := os.ReadDir(filepath.Join(s.stateDir, receiptDirectoryName))
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
		content, err := os.ReadFile(filepath.Join(s.stateDir, receiptDirectoryName, entry.Name()))
		if err != nil {
			return fmt.Errorf("read receipts: %w", err)
		}
		var file persistedReceiptFile
		if err := json.Unmarshal(content, &file); err != nil || file.Version != receiptFileVersion {
			dropped++
			log.Printf("huyang: dropping unreadable receipt file %s", entry.Name())
			continue
		}
		loadedHere, droppedHere := s.adoptReceipts(file.Receipts)
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
func (s *receiptStore) adoptReceipts(receipts []persistedReplay) (int, int) {
	loaded, dropped := 0, 0
	s.mu.Lock()
	defer s.mu.Unlock()
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
		s.replays[receipt.Key] = replay
		loaded++
	}
	s.applyReceiptCapsLocked(time.Now())
	return loaded, dropped
}

// migrateLegacyReceipts moves the receipts a version 1 registry embedded
// into per-workspace files, trimming every payload because version 1 kept no
// completion time, and reports how many were kept and dropped.
func (s *receiptStore) migrateLegacyReceipts(replays []persistedReplay) error {
	if len(replays) == 0 {
		return nil
	}
	loaded, dropped := s.adoptReceipts(replays)
	log.Printf("huyang: migrated %d legacy idempotency receipts out of registry.json, dropped %d", loaded, dropped)
	workspaces := map[workspacecore.ID]bool{}
	for _, replay := range replays {
		workspaces[replayWorkspace(replay.Key)] = true
	}
	for workspaceID := range workspaces {
		if workspaceID == "" {
			continue
		}
		if err := s.persistReceipts(workspaceID); err != nil {
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

func (s *receiptStore) CanonicalChangedPaths(workspaceID workspacecore.ID, target uint64) ([]string, error) {
	if target == 1 {
		return []string{}, nil
	}
	type receipt struct {
		from, to uint64
		path     string
	}
	s.mu.Lock()
	var receipts []receipt
	for key, replay := range s.replays {
		if !replay.complete || !strings.HasPrefix(key, string(workspaceID)+"\x00") {
			continue
		}
		data, _ := replay.result["data"].(map[string]any)
		if changed, _ := data["canonical_changed"].(bool); !changed {
			continue
		}
		from, fromErr := handlers.RevisionSequence(fmt.Sprint(data["from_revision"]))
		to, toErr := handlers.RevisionSequence(fmt.Sprint(data["revision"]))
		if fromErr != nil || toErr != nil || from+1 != to || to != target {
			continue
		}
		var changedPaths []string
		switch values := data["changed_paths"].(type) {
		case []string:
			changedPaths = append(changedPaths, values...)
		case []any:
			for _, value := range values {
				if path, ok := value.(string); ok {
					changedPaths = append(changedPaths, path)
				}
			}
		}
		if len(changedPaths) == 0 {
			change, _ := data["change"].(map[string]any)
			diff, _ := change["diff"].(map[string]any)
			if path, _ := diff["path"].(string); path != "" {
				changedPaths = append(changedPaths, path)
			}
		}
		for _, path := range changedPaths {
			if path != "" {
				receipts = append(receipts, receipt{from: from, to: to, path: path})
			}
		}
	}
	s.mu.Unlock()
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].from < receipts[j].from })
	paths := map[string]bool{}
	for _, item := range receipts {
		paths[item.path] = true
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("changed_file_evidence_incomplete: no native receipt covers wsrev_%d through wsrev_%d; run full verification or make a new native change", target-1, target)
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func exactDiffReceiptMap(diff workspacecore.ExactDiff) map[string]any {
	return map[string]any{
		"path": diff.Path, "before_sha256": diff.BeforeSHA256, "after_sha256": diff.AfterSHA256,
		"before": diff.Before, "after": diff.After, "patch": diff.Patch,
	}
}

func (s *receiptStore) RecordedRevisionDiffs(workspaceID string, fromSeq, toSeq uint64) []handlers.RecordedRevisionDiff {
	s.mu.Lock()
	defer s.mu.Unlock()

	var recorded []handlers.RecordedRevisionDiff
	seen := map[string]bool{}
	prefix := workspaceID + "\x00"
	for key, replay := range s.replays {
		if !replay.complete || !strings.HasPrefix(key, prefix) {
			continue
		}
		data, _ := replay.result["data"].(map[string]any)
		if changed, _ := data["canonical_changed"].(bool); !changed {
			continue
		}
		left, leftErr := handlers.RevisionSequence(fmt.Sprint(data["from_revision"]))
		right, rightErr := handlers.RevisionSequence(fmt.Sprint(data["revision"]))
		if leftErr != nil || rightErr != nil || left < fromSeq || right > toSeq {
			continue
		}

		var diffs []any
		switch {
		case strings.HasPrefix(key, prefix+"edit_apply\x00"):
			change, _ := data["change"].(map[string]any)
			if diff, ok := change["diff"].(map[string]any); ok {
				diffs = []any{diff}
			}
		case strings.HasPrefix(key, prefix+"change_plan\x00"):
			switch plan := data["plan"].(type) {
			case workspacecore.PlanRecord:
				if plan.Preparation != nil {
					for _, diff := range plan.Preparation.CommittedDiffs {
						diffs = append(diffs, exactDiffReceiptMap(diff))
					}
				}
			case map[string]any:
				preparation, _ := plan["preparation"].(map[string]any)
				switch committed := preparation["committed_diffs"].(type) {
				case []any:
					diffs = committed
				case []workspacecore.ExactDiff:
					for _, diff := range committed {
						diffs = append(diffs, exactDiffReceiptMap(diff))
					}
				}
			}
		}
		for _, raw := range diffs {
			diff, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			path := fmt.Sprint(diff["path"])
			beforeSHA := fmt.Sprint(diff["before_sha256"])
			afterSHA := fmt.Sprint(diff["after_sha256"])
			identity := fmt.Sprintf("%d\x00%d\x00%s\x00%s\x00%s\x00%s", left, right, path, beforeSHA, afterSHA, fmt.Sprint(diff["patch"]))
			if seen[identity] {
				continue
			}
			seen[identity] = true
			recorded = append(recorded, handlers.RecordedRevisionDiff{
				From: left, To: right, Diff: diff, Path: path,
				BeforeSHA: beforeSHA, AfterSHA: afterSHA,
			})
		}
	}
	return recorded
}
