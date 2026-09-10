package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	workspacecore "agent99/internal/workspace"
)

type legacyReceipt struct {
	sandbox     *workspacecore.Sandbox
	changes     []workspacecore.PlanStageFile
	undoSession *session
	closeUndo   func()
}

type legacyPendingAction struct {
	sandbox       *workspacecore.Sandbox
	transactionID string
}

var legacyWrapperState = struct {
	sync.Mutex
	receipts        map[string][]legacyReceipt
	pending         map[string]legacyPendingAction
	seenDiagnostics map[string]map[string]bool
}{
	receipts:        map[string][]legacyReceipt{},
	pending:         map[string]legacyPendingAction{},
	seenDiagnostics: map[string]map[string]bool{},
}

func legacyStateKey(ses session) string {
	return string(ses.Workspace.Identity().ID) + "\x00" + ses.Client
}

func legacyTokenFromError(err error) string {
	const marker = "token=\""
	text := err.Error()
	start := strings.Index(text, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	end := strings.IndexByte(text[start:], '"')
	if end < 0 {
		return ""
	}
	return text[start : start+end]
}

func rememberLegacyPending(ses session, token, transactionID string, sandbox *workspacecore.Sandbox) {
	key := legacyStateKey(ses) + "\x00" + token
	legacyWrapperState.Lock()
	previous, exists := legacyWrapperState.pending[key]
	legacyWrapperState.pending[key] = legacyPendingAction{sandbox: sandbox, transactionID: transactionID}
	legacyWrapperState.Unlock()
	if exists {
		_ = previous.sandbox.Cleanup()
	}
}

func rememberLegacyReceipt(ses session, receipt legacyReceipt) {
	key := legacyStateKey(ses)
	legacyWrapperState.Lock()
	legacyWrapperState.receipts[key] = append(legacyWrapperState.receipts[key], receipt)
	legacyWrapperState.Unlock()
}

func legacyResync(ctx context.Context, ses session) error {
	resyncSession := ses
	resyncSession.Context = ctx
	if _, err := providerCall(resyncSession, "huyang_workspace_resync", map[string]any{}); err != nil {
		return fmt.Errorf("canonical provider resync: %w", err)
	}
	return nil
}

func callLegacyCodeAction(arguments map[string]any, ses session) (string, bool, error) {
	token := fmt.Sprint(arguments["token"])
	key := legacyStateKey(ses) + "\x00" + token
	legacyWrapperState.Lock()
	pending, ok := legacyWrapperState.pending[key]
	legacyWrapperState.Unlock()
	if !ok {
		return "", false, nil
	}
	direct := ses
	direct.LegacyDirect = true
	out, err := callTool("apply_code_action", arguments, direct)
	if err != nil {
		return "", true, err
	}
	ctx := ses.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ses.Provider.Save(ctx); err != nil {
		return "", true, err
	}
	changes, err := pending.sandbox.PreparedChanges(ctx)
	if err != nil {
		return "", true, err
	}
	_, err = ses.Workspace.CommitPreparedTransaction(ctx, pending.transactionID, "op_apply_code_action", changes, func(ctx context.Context) error {
		return legacyResync(ctx, ses)
	})
	if err != nil {
		return "", true, err
	}
	legacyWrapperState.Lock()
	delete(legacyWrapperState.pending, key)
	legacyWrapperState.receipts[legacyStateKey(ses)] = append(
		legacyWrapperState.receipts[legacyStateKey(ses)],
		legacyReceipt{sandbox: pending.sandbox, changes: changes},
	)
	legacyWrapperState.Unlock()
	return normalizeLegacyOutput(ses, pending.sandbox, out), true, nil
}

func reverseLegacyContent(original, edited, current []byte) ([]byte, bool) {
	prefix := 0
	for prefix < len(original) && prefix < len(edited) && original[prefix] == edited[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(original)-prefix && suffix < len(edited)-prefix &&
		original[len(original)-1-suffix] == edited[len(edited)-1-suffix] {
		suffix++
	}
	oldRegion := original[prefix : len(original)-suffix]
	newRegion := edited[prefix : len(edited)-suffix]
	if len(newRegion) > 0 && bytes.Count(current, newRegion) == 1 {
		return bytes.Replace(current, newRegion, oldRegion, 1), true
	}
	start := bytes.LastIndexByte(edited[:prefix], '\n') + 1
	oldStart := bytes.LastIndexByte(original[:prefix], '\n') + 1
	end := len(edited) - suffix
	if tail := bytes.IndexByte(edited[end:], '\n'); tail >= 0 {
		end += tail + 1
	}
	oldEnd := len(original) - suffix
	if tail := bytes.IndexByte(original[oldEnd:], '\n'); tail >= 0 {
		oldEnd += tail + 1
	}
	newRegion = edited[start:end]
	oldRegion = original[oldStart:oldEnd]
	if len(newRegion) == 0 || bytes.Count(current, newRegion) != 1 {
		return nil, false
	}
	return bytes.Replace(current, newRegion, oldRegion, 1), true
}

func prepareLegacyUndoChanges(ses session, changes []workspacecore.PlanStageFile, allowOverwrite bool) ([]workspacecore.PlanStageFile, error) {
	prepared := make([]workspacecore.PlanStageFile, 0, len(changes))
	for _, change := range changes {
		snapshot, err := ses.Workspace.Refresh(change.Path, workspacecore.ProviderLayer{})
		if err != nil {
			return nil, err
		}
		currentExists := snapshot.Disk.Kind != workspacecore.ObjectMissing
		var current []byte
		absolute := filepath.Join(ses.Root, filepath.FromSlash(change.Path))
		switch snapshot.Disk.Kind {
		case workspacecore.ObjectRegularText, workspacecore.ObjectBinary:
			current, err = os.ReadFile(absolute)
		case workspacecore.ObjectSymlink:
			var target string
			target, err = os.Readlink(absolute)
			current = []byte(target)
		}
		if err != nil {
			return nil, err
		}
		desired := append([]byte(nil), change.Before...)
		if currentExists == change.AfterExists && !bytes.Equal(current, change.After) {
			if !currentExists || !change.BeforeExists || !change.AfterExists {
				return nil, fmt.Errorf("legacy undo conflict: %s changed after the edit", change.Path)
			}
			merged, ok := reverseLegacyContent(change.Before, change.After, current)
			if !ok {
				if !allowOverwrite {
					return nil, fmt.Errorf("legacy undo conflict: %s changed in the edited region", change.Path)
				}
			} else {
				desired = merged
			}
		}
		prepared = append(prepared, workspacecore.PlanStageFile{
			Path:         change.Path,
			Before:       current,
			After:        desired,
			BeforeExists: currentExists,
			AfterExists:  change.BeforeExists,
			BeforeDisk:   snapshot.Disk,
			AfterDisk:    change.BeforeDisk,
		})
	}
	return prepared, nil
}

func legacyUndoCount(arguments map[string]any, available int) int {
	if all, _ := arguments["all"].(bool); all {
		return available
	}
	count := 1
	switch value := arguments["count"].(type) {
	case float64:
		count = int(value)
	case int:
		count = value
	case string:
		if parsed, err := strconv.Atoi(value); err == nil {
			count = parsed
		}
	}
	if count < 1 {
		count = 1
	}
	if count > available {
		count = available
	}
	return count
}

func syncLegacySandbox(receipt legacyReceipt, canonicalRoot string) error {
	for _, change := range receipt.changes {
		source := filepath.Join(canonicalRoot, filepath.FromSlash(change.Path))
		target := filepath.Join(receipt.sandbox.Tree, filepath.FromSlash(change.Path))
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(source)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
			continue
		}
		content, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, content, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func canonicalizeLegacyPaths(ses session, out string) string {
	prefix := string(ses.Workspace.Identity().ID) + "\x00"
	legacyWrapperState.Lock()
	var roots []string
	for key, receipts := range legacyWrapperState.receipts {
		if strings.HasPrefix(key, prefix) {
			for _, receipt := range receipts {
				roots = append(roots, receipt.sandbox.Tree)
			}
		}
	}
	for key, pending := range legacyWrapperState.pending {
		if strings.HasPrefix(key, prefix) {
			roots = append(roots, pending.sandbox.Tree)
		}
	}
	legacyWrapperState.Unlock()
	for _, root := range roots {
		out = strings.ReplaceAll(out, root+string(filepath.Separator), "")
		out = strings.ReplaceAll(out, root, ses.Root)
	}
	return out
}

func normalizeLegacyOutput(ses session, sandbox *workspacecore.Sandbox, out string) string {
	out = strings.ReplaceAll(out, sandbox.Tree+string(filepath.Separator), "")
	out = canonicalizeLegacyPaths(ses, out)
	var result map[string]any
	if json.Unmarshal([]byte(out), &result) != nil {
		return out
	}
	values, ok := result["preexisting_new_to_list"].([]any)
	if !ok {
		return out
	}
	seenNow := map[string]bool{}
	filtered := make([]any, 0, len(values))
	key := legacyStateKey(ses)
	legacyWrapperState.Lock()
	if legacyWrapperState.seenDiagnostics[key] == nil {
		legacyWrapperState.seenDiagnostics[key] = map[string]bool{}
	}
	for _, value := range values {
		line, ok := value.(string)
		if !ok || seenNow[line] {
			continue
		}
		seenNow[line] = true
		if !legacyWrapperState.seenDiagnostics[key][line] {
			legacyWrapperState.seenDiagnostics[key][line] = true
			filtered = append(filtered, line)
		}
	}
	legacyWrapperState.Unlock()
	if len(filtered) > 0 {
		if summary, ok := result["preexisting"].(string); ok {
			fields := strings.Fields(summary)
			if len(fields) > 0 {
				if _, err := strconv.Atoi(fields[0]); err == nil {
					result["preexisting"] = strings.Replace(summary, fields[0], strconv.Itoa(len(filtered)), 1)
				}
			}
		}
	}
	if len(filtered) == 0 {
		delete(result, "preexisting_new_to_list")
		if summary, ok := result["preexisting"].(string); ok && !strings.Contains(summary, "none new to this list") {
			result["preexisting"] = summary + "; none new to this list"
		}
	} else {
		result["preexisting_new_to_list"] = filtered
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return out
	}
	return string(encoded)
}

func callDetachedLegacyUndo(arguments map[string]any, ses session, key string, stack []legacyReceipt, selected []legacyReceipt) (string, bool, error) {
	if len(selected) != 1 || selected[0].undoSession == nil {
		return "", false, nil
	}
	receipt := selected[0]
	if _, err := prepareLegacyUndoChanges(ses, receipt.changes, false); err != nil {
		return "", true, err
	}
	undoArguments := make(map[string]any, len(arguments)+1)
	for name, value := range arguments {
		undoArguments[name] = value
	}
	delete(undoArguments, "all")
	undoArguments["count"] = 1
	out, err := callTool("undo_edit", undoArguments, *receipt.undoSession)
	if err != nil {
		return "", true, err
	}
	var result struct {
		Undone []json.RawMessage `json:"undone"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return "", true, err
	}
	out = strings.ReplaceAll(out, receipt.sandbox.Tree, ses.Root)
	if len(result.Undone) == 0 {
		return out, true, nil
	}
	ctx := ses.Context
	if ctx == nil {
		ctx = context.Background()
	}
	prepared, err := prepareLegacyUndoChanges(ses, receipt.changes, arguments["all"] == true)
	if err != nil {
		return "", true, err
	}
	transactionID, err := newLegacyTransactionID()
	if err != nil {
		return "", true, err
	}
	if _, err := ses.Workspace.CommitPreparedTransaction(ctx, transactionID, "op_undo_edit", prepared, func(ctx context.Context) error {
		return legacyResync(ctx, ses)
	}); err != nil {
		return "", true, err
	}
	legacyWrapperState.Lock()
	legacyWrapperState.receipts[key] = legacyWrapperState.receipts[key][:len(stack)-1]
	legacyWrapperState.Unlock()
	_ = receipt.sandbox.Cleanup()
	if receipt.closeUndo != nil {
		receipt.closeUndo()
	}
	return out, true, nil
}

func callLegacyUndo(arguments map[string]any, ses session) (string, bool, error) {
	key := legacyStateKey(ses)
	legacyWrapperState.Lock()
	stack := legacyWrapperState.receipts[key]
	count := legacyUndoCount(arguments, len(stack))
	selected := append([]legacyReceipt(nil), stack[len(stack)-count:]...)
	legacyWrapperState.Unlock()
	if count == 0 {
		return "", false, nil
	}
	if out, handled, err := callDetachedLegacyUndo(arguments, ses, key, stack, selected); handled {
		return out, true, err
	}
	for _, receipt := range selected {
		if _, err := prepareLegacyUndoChanges(ses, receipt.changes, false); err != nil {
			if err := syncLegacySandbox(receipt, ses.Root); err != nil {
				return "", true, err
			}
		}
	}
	ctx := ses.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := legacyResync(ctx, ses); err != nil {
		return "", true, err
	}
	direct := ses
	direct.LegacyDirect = true
	out, err := callTool("undo_edit", arguments, direct)
	if err != nil {
		return "", true, err
	}
	var result struct {
		Undone  []json.RawMessage `json:"undone"`
		Dropped []json.RawMessage `json:"dropped"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return "", true, err
	}
	for _, receipt := range selected {
		out = strings.ReplaceAll(out, receipt.sandbox.Tree, ses.Root)
	}
	if len(result.Undone) == 0 {
		return out, true, nil
	}
	applyCount := count
	if len(result.Dropped) > 0 {
		applyCount--
	}
	toApply := selected[:applyCount]
	for index := len(toApply) - 1; index >= 0; index-- {
		transactionID, err := newLegacyTransactionID()
		if err != nil {
			return "", true, err
		}
		finalize := func(context.Context) error { return nil }
		if index == 0 {
			finalize = func(ctx context.Context) error { return legacyResync(ctx, ses) }
		}
		prepared, err := prepareLegacyUndoChanges(ses, toApply[index].changes, len(result.Dropped) == 0 && arguments["all"] == true)
		if err != nil {
			return "", true, err
		}
		if _, err := ses.Workspace.CommitPreparedTransaction(
			ctx, transactionID, "op_undo_edit", prepared, finalize,
		); err != nil {
			return "", true, err
		}
	}
	legacyWrapperState.Lock()
	legacyWrapperState.receipts[key] = legacyWrapperState.receipts[key][:len(stack)-count]
	legacyWrapperState.Unlock()
	for _, receipt := range selected {
		_ = receipt.sandbox.Cleanup()
	}
	return out, true, nil
}

func cleanupLegacyWorkspace(ses session) {
	prefix := string(ses.Workspace.Identity().ID) + "\x00"
	legacyWrapperState.Lock()
	var sandboxes []*workspacecore.Sandbox
	var closers []func()
	for key, receipts := range legacyWrapperState.receipts {
		if strings.HasPrefix(key, prefix) {
			for _, receipt := range receipts {
				sandboxes = append(sandboxes, receipt.sandbox)
				if receipt.closeUndo != nil {
					closers = append(closers, receipt.closeUndo)
				}
			}
			delete(legacyWrapperState.receipts, key)
		}
	}
	for key, pending := range legacyWrapperState.pending {
		if strings.HasPrefix(key, prefix) {
			sandboxes = append(sandboxes, pending.sandbox)
			delete(legacyWrapperState.pending, key)
		}
	}
	for key := range legacyWrapperState.seenDiagnostics {
		if strings.HasPrefix(key, prefix) {
			delete(legacyWrapperState.seenDiagnostics, key)
		}
	}
	legacyWrapperState.Unlock()
	for _, sandbox := range sandboxes {
		_ = sandbox.Cleanup()
	}
	for _, closeUndo := range closers {
		closeUndo()
	}
}
