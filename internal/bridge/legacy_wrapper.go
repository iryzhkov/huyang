package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const legacyWrapperEscape = "HUYANG_LEGACY_WRAPPERS"

var legacyTransactionalTools = map[string]bool{
	"replace_symbol_body":  true,
	"replace_symbol_lines": true,
	"insert_after_symbol":  true,
	"insert_before_symbol": true,
	"insert_lines":         true,
	"create_file":          true,
	"move_file":            true,
	"delete_file":          true,
	"rename_symbol":        true,
	"apply_code_action":    true,
	"replace_pattern":      true,
	"move_symbols":         true,
}

func legacyTransactionsEnabled(name string, arguments map[string]any) bool {
	if dryRun, _ := arguments["dry_run"].(bool); dryRun {
		return false
	}
	if wait, ok := arguments["wait"].(bool); ok && !wait {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv(legacyWrapperEscape)), "direct") {
		return false
	}
	key := "HUYANG_LEGACY_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_PATH"
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(key)), "direct")
}

func legacyWorkspaceStateDir(root string) (string, error) {
	base := os.Getenv("HUYANG_LEGACY_STATE_DIR")
	if base == "" {
		base = filepath.Join(os.TempDir(), "huyang-legacy-state")
	}
	absolute, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	stateDir := filepath.Join(absolute, hex.EncodeToString(sum[:12]))
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return "", err
	}
	return stateDir, nil
}

func newLegacyTransactionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "legacy_" + hex.EncodeToString(value[:]), nil
}

func legacySandboxArguments(arguments map[string]any, canonicalRoot, sandboxRoot string) map[string]any {
	mapped := make(map[string]any, len(arguments))
	for key, value := range arguments {
		mapped[key] = value
	}
	mapPath := func(value any) any {
		absolute := resolveInRoot(canonicalRoot, value)
		relative, err := filepath.Rel(canonicalRoot, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return value
		}
		return filepath.Join(sandboxRoot, relative)
	}
	for _, key := range []string{"file", "from", "to"} {
		if value, ok := mapped[key]; ok {
			mapped[key] = mapPath(value)
		}
	}
	if values, ok := mapped["files"].([]any); ok {
		files := make([]any, len(values))
		for index, value := range values {
			files[index] = mapPath(value)
		}
		mapped["files"] = files
	}
	return mapped
}

var legacyFileTransactionTools = map[string]bool{
	"create_file":  true,
	"move_file":    true,
	"delete_file":  true,
	"move_symbols": true,
}

func legacyArgumentFiles(arguments map[string]any, root string) []any {
	files := make([]any, 0)
	for _, key := range []string{"file", "from", "to"} {
		if value, ok := arguments[key]; ok {
			files = append(files, resolveInRoot(root, value))
		}
	}
	if values, ok := arguments["files"].([]any); ok {
		for _, value := range values {
			files = append(files, resolveInRoot(root, value))
		}
	}
	return files
}

func captureLegacyBuffers(ctx context.Context, ses session, sandbox *workspacecore.Sandbox, arguments map[string]any) error {
	value, err := providerCall(ses, "huyang_capture_files", map[string]any{
		"files": legacyArgumentFiles(arguments, ses.Root),
		"root":  ses.Root,
	})
	if err != nil {
		return err
	}
	result, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("legacy buffer capture returned %T", value)
	}
	files, ok := result["files"].([]any)
	if !ok {
		return fmt.Errorf("legacy buffer capture omitted files")
	}
	for _, raw := range files {
		item, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("legacy buffer capture entry returned %T", raw)
		}
		path, _ := item["path"].(string)
		content, _ := item["content"].(string)
		relative, err := filepath.Rel(ses.Root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("legacy buffer capture escaped workspace: %s", path)
		}
		target := filepath.Join(sandbox.Tree, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		info, err := os.Stat(path)
		mode := os.FileMode(0o644)
		if err == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(target, []byte(content), mode); err != nil {
			return err
		}
	}
	return nil
}

func callLegacyTransaction(name string, arguments map[string]any, ses session) (string, error) {
	if ses.Workspace == nil || ses.Provider == nil {
		return "", fmt.Errorf("legacy transaction requires an owned workspace provider")
	}
	ctx := ses.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if legacyFileTransactionTools[name] {
		if err := ses.Provider.Save(ctx); err != nil {
			return "", fmt.Errorf("legacy transaction canonical flush: %w", err)
		}
	}
	transactionID, err := newLegacyTransactionID()
	if err != nil {
		return "", err
	}
	stateDir, err := legacyWorkspaceStateDir(ses.Root)
	if err != nil {
		return "", err
	}
	sandbox, err := workspacecore.MaterializeSandbox(
		ctx, ses.Root, filepath.Join(stateDir, "sandboxes"), ses.Workspace.Identity().ID,
		transactionID, 1, fmt.Sprintf("wsrev_%d", ses.Workspace.Identity().StateSeq),
		workspacecore.DefaultSandboxLimits(),
	)
	if err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = sandbox.Cleanup()
		}
	}()

	operationSession := ses
	operationSession.Context = ctx
	operationSession.LegacyDirect = true
	operationArguments := arguments
	var sandboxProvider provider.Provider
	closeSandboxProvider := false
	if legacyFileTransactionTools[name] {
		operationSession.Root = sandbox.Tree
		operationArguments = legacySandboxArguments(arguments, ses.Root, sandbox.Tree)
	}
	if name == "move_symbols" {
		sandboxProvider, err = referenceProviders.Open(providerOpenConfig{
			Root: sandbox.Tree, InitFile: os.Getenv("AGENT99_HEADLESS_INIT"), RuntimePath: shippedRuntimePath(),
		})
		if err != nil {
			return "", fmt.Errorf("legacy transaction sandbox provider: %w", err)
		}
		closeSandboxProvider = true
		defer func() {
			if closeSandboxProvider {
				_ = sandboxProvider.Close(context.Background())
			}
		}()
		profile, profileErr := provider.NewAnalysisProfile("legacy-transaction", []provider.Registration{{
			Provider: sandboxProvider, Languages: []string{"*"},
			Capabilities: sandboxProvider.Descriptor().Capabilities, Role: provider.RolePrimary,
		}})
		if profileErr != nil {
			return "", profileErr
		}
		operationSession.Provider = sandboxProvider
		operationSession.Providers = profile
		operationSession.Workspace = nil
	}
	out, err := callTool(name, operationArguments, operationSession)
	if err != nil {
		return "", err
	}
	if legacyFileTransactionTools[name] {
		if err := operationSession.Provider.Save(ctx); err != nil {
			return "", fmt.Errorf("legacy transaction sandbox save: %w", err)
		}
	} else if err := captureLegacyBuffers(ctx, ses, sandbox, arguments); err != nil {
		direct := ses
		direct.LegacyDirect = true
		_, _ = callTool("undo_edit", map[string]any{}, direct)
		return "", err
	}
	changes, err := sandbox.PreparedChanges(ctx)
	if err != nil {
		return "", err
	}
	_, err = ses.Workspace.CommitPreparedTransaction(ctx, transactionID, "op_"+name, changes, func(ctx context.Context) error {
		return legacyResync(ctx, ses)
	})
	if err != nil {
		return "", err
	}
	if legacyFileTransactionTools[name] {
		receipt := legacyReceipt{sandbox: sandbox, changes: changes}
		if name == "move_symbols" {
			detached := operationSession
			detached.Context = context.Background()
			receipt.undoSession = &detached
			receipt.closeUndo = func() { _ = sandboxProvider.Close(context.Background()) }
			closeSandboxProvider = false
		}
		rememberLegacyReceipt(ses, receipt)
		cleanup = false
		return normalizeLegacyOutput(ses, sandbox, out), nil
	}
	return out, nil
}
