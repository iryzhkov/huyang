package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func bulkyReceipt(index int, payloadBytes int) map[string]any {
	return map[string]any{
		"api_version": modernAPIVersion, "request_id": fmt.Sprintf("req_%d", index), "outcome": "ok",
		"summary": "edit applied", "warnings": []string{}, "next": []any{},
		"evidence": map[string]any{"ids": []string{fmt.Sprintf("ev_%d", index)}, "truncated": false},
		"data": map[string]any{
			"canonical_changed": true, "from_revision": fmt.Sprintf("wsrev_%d", index+1), "revision": fmt.Sprintf("wsrev_%d", index+2),
			"changed_paths": []string{"main.go"},
			"change": map[string]any{"diff": map[string]any{
				"path": "main.go", "before_sha256": "aa", "after_sha256": "bb", "patch": "@@ -1 +1 @@\n",
			}},
			"bulk": strings.Repeat("x", payloadBytes),
		},
	}
}

func receiptKey(workspaceID, tool, key string) string {
	return workspaceID + "\x00" + tool + "\x00" + key
}

func TestReceiptsAreBoundedInMemoryAndOnDisk(t *testing.T) {
	stateDir := t.TempDir()
	direct := newDirectWorkspaces(stateDir)
	direct.receiptLimits = receiptLimits{PerWorkspace: 8, TotalBytes: 12 * 4096, Tombstones: 16, PayloadWindow: time.Hour}
	workspaceID := "ws_bounded"
	for index := 0; index < 40; index++ {
		key := receiptKey(workspaceID, "edit_apply", fmt.Sprintf("key_%d", index))
		replay := &directReplay{argumentsHash: fmt.Sprintf("hash_%d", index), done: closedSignal()}
		if err := direct.storeReceipt(key, replay, bulkyReceipt(index, 4096)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	direct.replayMu.Lock()
	live, tombstones, bytes := 0, 0, 0
	for _, replay := range direct.replays {
		if replay.evicted {
			tombstones++
			continue
		}
		live++
		bytes += replay.bytes
	}
	direct.replayMu.Unlock()
	if live > direct.receiptLimits.PerWorkspace || bytes > direct.receiptLimits.TotalBytes {
		t.Fatalf("live receipts = %d (%d bytes), caps %d/%d", live, bytes, direct.receiptLimits.PerWorkspace, direct.receiptLimits.TotalBytes)
	}
	if tombstones > direct.receiptLimits.Tombstones {
		t.Fatalf("tombstones = %d, cap %d", tombstones, direct.receiptLimits.Tombstones)
	}
	info, err := os.Stat(direct.receiptPath(workspacecore.ID(workspaceID)))
	if err != nil {
		t.Fatal(err)
	}
	if limit := int64(direct.receiptLimits.TotalBytes + direct.receiptLimits.Tombstones*512); info.Size() > limit {
		t.Fatalf("receipt file is %d bytes, bound %d", info.Size(), limit)
	}
	registry, err := os.ReadFile(filepath.Join(stateDir, "registry.json"))
	if err == nil && strings.Contains(string(registry), "Replays") {
		t.Fatal("registry.json still embeds receipts")
	}

	// A retry of an evicted receipt is refused, never re-executed; a retry of
	// a live receipt replays.
	direct.replayMu.Lock()
	direct.replays[receiptKey(workspaceID, "edit_apply", "key_20")].argumentsHash = argumentsHashOf(t, map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "key_20", "operation": map[string]any{"kind": "replace_range"},
	})
	direct.replayMu.Unlock()
	evicted := direct.call(context.Background(), "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "key_20", "operation": map[string]any{"kind": "replace_range"},
	})
	if evicted["outcome"] != "conflict" || evicted["code"] != receiptEvictedCode {
		t.Fatalf("evicted replay = %#v", evicted)
	}
	direct.replayMu.Lock()
	direct.replays[receiptKey(workspaceID, "edit_apply", "key_39")].argumentsHash = argumentsHashOf(t, map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "key_39", "operation": map[string]any{"kind": "replace_range"},
	})
	direct.replayMu.Unlock()
	replayed := direct.call(context.Background(), "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "key_39", "operation": map[string]any{"kind": "replace_range"},
	})
	if replayed["idempotency"] != "replayed" {
		t.Fatalf("live replay = %#v", replayed)
	}

	restarted := newDirectWorkspaces(stateDir)
	restarted.replayMu.Lock()
	restoredLive := 0
	for _, replay := range restarted.replays {
		if !replay.evicted {
			restoredLive++
		}
	}
	restarted.replayMu.Unlock()
	if restoredLive != live {
		t.Fatalf("restart restored %d live receipts, want %d", restoredLive, live)
	}
}

func argumentsHashOf(t *testing.T, arguments map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

func TestTrimmedReceiptsKeepRevisionProvenance(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	direct.receiptLimits.PayloadWindow = 0
	workspaceID := "ws_trim"
	first := receiptKey(workspaceID, "edit_apply", "first")
	if err := direct.storeReceipt(first, &directReplay{argumentsHash: "h1", done: closedSignal()}, bulkyReceipt(0, 4096)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := direct.storeReceipt(receiptKey(workspaceID, "edit_apply", "second"), &directReplay{argumentsHash: "h2", done: closedSignal()}, bulkyReceipt(1, 4096)); err != nil {
		t.Fatal(err)
	}
	direct.replayMu.Lock()
	replay := direct.replays[first]
	trimmed, size := replay.trimmed, replay.bytes
	data := replay.result["data"].(map[string]any)
	direct.replayMu.Unlock()
	if !trimmed || size > 1024 || data["bulk"] != nil || data[receiptTrimmedDataMarker] != true {
		t.Fatalf("receipt was not trimmed: trimmed=%v bytes=%d data=%#v", trimmed, size, data)
	}
	diffs := direct.recordedRevisionDiffs(workspaceID, 1, 2)
	if len(diffs) != 1 || diffs[0].path != "main.go" || diffs[0].afterSHA != "bb" {
		t.Fatalf("provenance after trim = %#v", diffs)
	}
	paths, err := direct.canonicalChangedPaths(workspacecore.ID(workspaceID), 2)
	if err != nil || len(paths) != 1 || paths[0] != "main.go" {
		t.Fatalf("changed paths after trim = %#v, %v", paths, err)
	}
	direct.replayMu.Lock()
	direct.replays[first].argumentsHash = argumentsHashOf(t, map[string]any{"workspace_id": workspaceID, "idempotency_key": "first"})
	direct.replayMu.Unlock()
	replayed := direct.call(context.Background(), "edit_apply", map[string]any{"workspace_id": workspaceID, "idempotency_key": "first"})
	if replayed["idempotency"] != "replayed" || len(replayed["warnings"].([]string)) != 1 {
		t.Fatalf("trimmed replay = %#v", replayed)
	}
}

func TestLegacyRegistryReceiptsMigrateIntoPerWorkspaceFiles(t *testing.T) {
	stateDir := t.TempDir()
	workspaceID := "ws_legacy"
	legacy := map[string]any{
		"Version": 1, "Workspaces": []any{},
		"Replays": []any{
			map[string]any{"Key": receiptKey(workspaceID, "edit_apply", "old"), "ArgumentsHash": "h", "Result": bulkyReceipt(0, 200<<10)},
			map[string]any{"Key": receiptKey(workspaceID, "change_plan", "pending"), "ArgumentsHash": "h2", "Result": map[string]any{
				"transaction": map[string]any{"id": "plan_1", "state": "READY"}, "data": map[string]any{},
			}},
		},
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "registry.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(stateDir)
	if direct.loadErr != nil {
		t.Fatal(direct.loadErr)
	}
	rewritten, err := os.ReadFile(filepath.Join(stateDir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry persistedRegistry
	if err := json.Unmarshal(rewritten, &registry); err != nil {
		t.Fatal(err)
	}
	if registry.Version != registryStateVersion || len(registry.Replays) != 0 || len(rewritten) > 1024 {
		t.Fatalf("migrated registry = version %d, %d replays, %d bytes", registry.Version, len(registry.Replays), len(rewritten))
	}
	receiptFile, err := os.ReadFile(direct.receiptPath(workspacecore.ID(workspaceID)))
	if err != nil {
		t.Fatal(err)
	}
	if len(receiptFile) > 4096 || strings.Contains(string(receiptFile), "xxxxxxxx") {
		t.Fatalf("migrated receipt file is %d bytes and still carries the bulk payload", len(receiptFile))
	}
	direct.replayMu.Lock()
	migrated := direct.replays[receiptKey(workspaceID, "edit_apply", "old")]
	_, pendingKept := direct.replays[receiptKey(workspaceID, "change_plan", "pending")]
	direct.replayMu.Unlock()
	if migrated == nil || !migrated.trimmed || pendingKept {
		t.Fatalf("migrated receipts = %#v, provider-backed kept=%v", migrated, pendingKept)
	}
	if diffs := direct.recordedRevisionDiffs(workspaceID, 1, 2); len(diffs) != 1 {
		t.Fatalf("provenance lost in migration: %#v", diffs)
	}
}
