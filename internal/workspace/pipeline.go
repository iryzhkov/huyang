package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	goscanner "go/scanner"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

type VerificationStatus string

const (
	VerificationPassed    VerificationStatus = "passed"
	VerificationFailed    VerificationStatus = "failed"
	VerificationSkipped   VerificationStatus = "unavailable"
	VerificationCancelled VerificationStatus = "cancelled"
	VerificationTimedOut  VerificationStatus = "timed_out"
)

type CommandPolicy struct {
	Name           string   `toml:"name" json:"name,omitempty"`
	Command        []string `toml:"command" json:"command"`
	DeclaredWrites []string `toml:"declared_writes" json:"declared_writes,omitempty"`
	Covers         []string `toml:"covers" json:"covers,omitempty"`
	Variants       []string `toml:"variants" json:"variants,omitempty"`
	Required       bool     `toml:"required" json:"required,omitempty"`
	Parallel       bool     `toml:"parallel" json:"parallel,omitempty"`
}

type FormatPolicy struct {
	Gate      CommandPolicy `toml:"gate" json:"gate"`
	Transform CommandPolicy `toml:"transform" json:"transform"`
	Mode      string        `toml:"mode" json:"mode"`
	Scope     string        `toml:"scope" json:"scope"`
}

type PipelinePolicy struct {
	Version  int             `toml:"version" json:"version"`
	Format   FormatPolicy    `toml:"format" json:"format"`
	Check    []CommandPolicy `toml:"check" json:"check"`
	Tests    []CommandPolicy `toml:"tests" json:"tests"`
	Variants []VariantPolicy `toml:"variants" json:"variants,omitempty"`
	Impact   ImpactPolicy    `toml:"impact" json:"impact"`
	Resource struct {
		TimeoutSeconds   int   `toml:"timeout_seconds" json:"timeout_seconds"`
		MaxOutputBytes   int   `toml:"max_output_bytes" json:"max_output_bytes"`
		MaxSnapshotBytes int64 `toml:"max_snapshot_bytes" json:"max_snapshot_bytes"`
		MaxChangedFiles  int   `toml:"max_changed_files" json:"max_changed_files"`
		MaxParallel      int   `toml:"max_parallel" json:"max_parallel"`
	} `toml:"resource" json:"resource"`
	Trusted bool `toml:"-" json:"trusted"`
	// Detected names the repository markers (go.mod, pyproject.toml, ...)
	// the commands were derived from when no .huyang.toml exists.
	Detected      []string `toml:"-" json:"detected,omitempty"`
	ProjectConfig string   `toml:"-" json:"project_config,omitempty"`
	UserConfig    string   `toml:"-" json:"user_config,omitempty"`
}

type userPipelinePolicy struct {
	Trust struct {
		Roots []string `toml:"roots"`
	} `toml:"trust"`
	Resource struct {
		TimeoutSeconds   int   `toml:"timeout_seconds"`
		MaxOutputBytes   int   `toml:"max_output_bytes"`
		MaxSnapshotBytes int64 `toml:"max_snapshot_bytes"`
		MaxChangedFiles  int   `toml:"max_changed_files"`
		MaxParallel      int   `toml:"max_parallel"`
	} `toml:"resource"`
}

type VerificationRequest struct {
	Stages                   []string
	Revision                 string
	Transform                bool
	TestScope                string
	TestHistoryPath          string
	ApplyConfiguredTransform bool
	DiagnosticVerifier       func(context.Context, string, []PlanStageFile) (VerificationStage, error) `json:"-"`
}

type VerificationStage struct {
	Stage           string             `json:"stage"`
	Implementation  []string           `json:"implementation,omitempty"`
	Mode            string             `json:"mode"`
	Scope           []string           `json:"scope,omitempty"`
	StartedRevision string             `json:"started_revision"`
	Exit            int                `json:"exit"`
	Writes          []string           `json:"writes,omitempty"`
	Output          string             `json:"output,omitempty"`
	Status          VerificationStatus `json:"status"`
	DurationMS      int64              `json:"duration_ms"`
	Coverage        Coverage           `json:"coverage"`
	EvidenceIDs     []string           `json:"evidence_ids,omitempty"`
	TestScope       string             `json:"test_scope,omitempty"`
	TestVerdict     string             `json:"test_verdict,omitempty"`
	SelectedTests   []SelectedTest     `json:"selected_tests,omitempty"`
	ExecutedTests   []string           `json:"executed_tests,omitempty"`
	// Confidence qualifies an unavailable stage whose dimension another
	// stage corroborated, for example a parser stage without a native parser
	// for a language whose configured project check passed. Status and Exit
	// always describe what this stage itself did.
	Confidence DiagnosticConfidence `json:"confidence,omitempty"`
}

// FormattingClaims separates the formatting statements a verification result
// can make so consumers do not infer them from stage names.
type FormattingClaims struct {
	// EditorFormatted: an LSP or editor formatter changed the buffer. The
	// pipeline never runs an editor; the bridge sets this when it applied
	// one before staging.
	EditorFormatted bool `json:"editor_formatted"`
	// RepositoryFormatted: the configured repository formatter transform
	// produced the staged bytes.
	RepositoryFormatted bool `json:"repository_formatted"`
	// FormatGatePassed: the configured non-mutating repository check
	// accepted the staged bytes.
	FormatGatePassed bool `json:"format_gate_passed"`
	// NotConfigured: no repository formatter or gate is configured, so no
	// stronger claim can exist.
	NotConfigured bool `json:"not_configured"`
	// Provisional: a gate is configured but did not deliver a verdict (not
	// requested, untrusted workspace, timed out, cancelled).
	Provisional bool     `json:"provisional"`
	Reasons     []string `json:"reasons,omitempty"`
}

// canonicalStageOrder is the server-side pipeline order. Requested stages
// are sorted into it; the caller's order never changes execution order.
var canonicalStageOrder = []string{"format_gate", "parser", "diagnostics", "check", "tests"}

// CanonicalStages validates and orders requested stage names. Unknown names
// are rejected before anything runs and duplicates collapse.
func CanonicalStages(requested []string) ([]string, error) {
	rank := make(map[string]int, len(canonicalStageOrder))
	for index, name := range canonicalStageOrder {
		rank[name] = index
	}
	seen := map[string]bool{}
	var ordered []string
	for _, name := range requested {
		if _, known := rank[name]; !known {
			return nil, fmt.Errorf("unknown verification stage %q", name)
		}
		if !seen[name] {
			seen[name] = true
			ordered = append(ordered, name)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return rank[ordered[i]] < rank[ordered[j]] })
	return ordered, nil
}

type ToolDelta struct {
	Path           string     `json:"path"`
	Before         []byte     `json:"before,omitempty"`
	After          []byte     `json:"after,omitempty"`
	BeforeExists   bool       `json:"before_exists"`
	AfterExists    bool       `json:"after_exists"`
	Classification string     `json:"classification"`
	BeforeKind     ObjectKind `json:"before_kind"`
	AfterKind      ObjectKind `json:"after_kind"`
	BeforeMode     uint32     `json:"before_mode,omitempty"`
	AfterMode      uint32     `json:"after_mode,omitempty"`
	BeforeTarget   string     `json:"before_target,omitempty"`
	AfterTarget    string     `json:"after_target,omitempty"`
}

type VerificationResult struct {
	Revision      string              `json:"revision"`
	Stages        []VerificationStage `json:"stages"`
	ToolDelta     []ToolDelta         `json:"tool_delta,omitempty"`
	PreparedFiles []PlanStageFile     `json:"-"`
	Impact        *ImpactGraph        `json:"impact,omitempty"`
	Targeted      *TargetedTestResult `json:"targeted_tests,omitempty"`
	FullTestGate  string              `json:"full_test_gate,omitempty"`
	Formatting    FormattingClaims    `json:"formatting"`
}

func DefaultPipelinePolicy() PipelinePolicy {
	var policy PipelinePolicy
	policy.Version = 1
	policy.Format.Mode = "check"
	policy.Format.Scope = "declared"
	policy.Resource.TimeoutSeconds = 120
	policy.Resource.MaxOutputBytes = 256 << 10
	policy.Resource.MaxSnapshotBytes = 256 << 20
	policy.Resource.MaxChangedFiles = 256
	policy.Resource.MaxParallel = 1
	return policy
}

func LoadPipelinePolicy(projectRoot, userConfig string) (PipelinePolicy, error) {
	return LoadPipelinePolicyForTrustedRoot(projectRoot, projectRoot, userConfig)
}

func LoadPipelinePolicyForTrustedRoot(projectRoot, trustedRoot, userConfig string) (PipelinePolicy, error) {
	policy := DefaultPipelinePolicy()
	if err := loadProjectPolicy(projectRoot, &policy); err != nil {
		return PipelinePolicy{}, err
	}
	if policy.ProjectConfig == "" {
		detectPipelinePolicy(projectRoot, &policy)
	}
	user, err := loadUserPolicy(defaultUserConfigPath(userConfig), &policy)
	if err != nil {
		return PipelinePolicy{}, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(trustedRoot)
	if err != nil {
		return PipelinePolicy{}, err
	}
	for _, trusted := range user.Trust.Roots {
		resolved, resolveErr := filepath.EvalSymlinks(trusted)
		if resolveErr == nil && (resolvedRoot == resolved || insidePath(resolved, resolvedRoot)) {
			policy.Trusted = true
			break
		}
	}
	applyUserResourceCaps(&policy, user)
	if err := validatePipelinePolicy(&policy); err != nil {
		return PipelinePolicy{}, err
	}
	return policy, nil
}

// loadProjectPolicy decodes .huyang.toml under projectRoot into policy when it exists,
// refusing unknown keys.
func loadProjectPolicy(projectRoot string, policy *PipelinePolicy) error {
	projectPath := filepath.Join(projectRoot, ".huyang.toml")
	content, err := os.ReadFile(projectPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	metadata, err := toml.Decode(string(content), policy)
	if err != nil {
		return fmt.Errorf("invalid project policy: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		key := undecoded[0].String()
		hint := ""
		if strings.HasSuffix(key, ".affected") {
			hint = "; use covers = [\"path/**\"] on the command instead"
		}
		return fmt.Errorf("unknown project policy key %q%s", key, hint)
	}
	policy.ProjectConfig = projectPath
	return nil
}

// defaultUserConfigPath returns the explicit user config path, or the XDG default.
// UserConfigPath is the user policy file consulted for trust roots and
// resource caps, for messages that tell the user where to grant trust.
func UserConfigPath() string { return defaultUserConfigPath("") }

func defaultUserConfigPath(userConfig string) string {
	if userConfig != "" {
		return userConfig
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, "huyang", "config.toml")
}

// loadUserPolicy decodes the user policy at userConfig when the path is set and the file
// exists, and records the path on the pipeline policy.
func loadUserPolicy(userConfig string, policy *PipelinePolicy) (userPipelinePolicy, error) {
	var user userPipelinePolicy
	if userConfig == "" {
		return user, nil
	}
	policy.UserConfig = userConfig
	content, err := os.ReadFile(userConfig)
	if errors.Is(err, os.ErrNotExist) {
		return user, nil
	}
	if err != nil {
		return user, err
	}
	if _, err := toml.Decode(string(content), &user); err != nil {
		return user, fmt.Errorf("invalid user policy: %w", err)
	}
	policy.UserConfig = userConfig
	return user, nil
}

// applyUserResourceCaps lowers each resource limit to the user's cap when the user set a
// smaller positive value.
func applyUserResourceCaps(policy *PipelinePolicy, user userPipelinePolicy) {
	if user.Resource.TimeoutSeconds > 0 && user.Resource.TimeoutSeconds < policy.Resource.TimeoutSeconds {
		policy.Resource.TimeoutSeconds = user.Resource.TimeoutSeconds
	}
	if user.Resource.MaxOutputBytes > 0 && user.Resource.MaxOutputBytes < policy.Resource.MaxOutputBytes {
		policy.Resource.MaxOutputBytes = user.Resource.MaxOutputBytes
	}
	if user.Resource.MaxSnapshotBytes > 0 && user.Resource.MaxSnapshotBytes < policy.Resource.MaxSnapshotBytes {
		policy.Resource.MaxSnapshotBytes = user.Resource.MaxSnapshotBytes
	}
	if user.Resource.MaxChangedFiles > 0 && user.Resource.MaxChangedFiles < policy.Resource.MaxChangedFiles {
		policy.Resource.MaxChangedFiles = user.Resource.MaxChangedFiles
	}
	if user.Resource.MaxParallel > 0 && user.Resource.MaxParallel < policy.Resource.MaxParallel {
		policy.Resource.MaxParallel = user.Resource.MaxParallel
	}
}

// validatePipelinePolicy checks the merged policy and fills the format defaults.
func validatePipelinePolicy(policy *PipelinePolicy) error {
	if policy.Version != 1 {
		return fmt.Errorf("unsupported huyang policy version %d", policy.Version)
	}
	if policy.Resource.MaxParallel < 1 || policy.Resource.MaxParallel > 8 {
		return errors.New("resource max_parallel must be between 1 and 8")
	}
	if err := validateParallelCommands(policy.Check, policy.Tests); err != nil {
		return err
	}
	if policy.Format.Mode == "" {
		policy.Format.Mode = "check"
	}
	if policy.Format.Mode != "off" && policy.Format.Mode != "check" && policy.Format.Mode != "transform" {
		return fmt.Errorf("invalid format mode %q", policy.Format.Mode)
	}
	if policy.Format.Scope == "" {
		policy.Format.Scope = "declared"
	}
	if policy.Format.Scope != "declared" && policy.Format.Scope != "whole_repository" {
		return fmt.Errorf("invalid format scope %q", policy.Format.Scope)
	}
	return nil
}

type treeObject struct {
	kind    ObjectKind
	mode    fs.FileMode
	content []byte
	target  string
}
type treeSnapshot map[string]treeObject

func captureTree(ctx context.Context, root string, maxBytes int64) (treeSnapshot, error) {
	result := treeSnapshot{}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			result[relative] = treeObject{kind: ObjectDirectory, mode: info.Mode()}
		case info.Mode().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			total += int64(len(content))
			if total > maxBytes {
				return fmt.Errorf("verification_snapshot_quota_exceeded: observed_bytes=%d limit_bytes=%d at %s; raise resource.max_snapshot_bytes in .huyang.toml or remove generated dependencies from the verification root", total, maxBytes, relative)
			}
			kind := ObjectRegularText
			if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
				kind = ObjectBinary
			}
			result[relative] = treeObject{kind: kind, mode: info.Mode(), content: content}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			result[relative] = treeObject{kind: ObjectSymlink, mode: info.Mode(), target: target}
		default:
			return fmt.Errorf("verification_special_file: %s", relative)
		}
		return nil
	})
	return result, err
}

func restoreTree(root string, snapshot treeSnapshot) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(snapshot))
	for path := range snapshot {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], string(filepath.Separator)), strings.Count(paths[j], string(filepath.Separator))
		if di == dj {
			return paths[i] < paths[j]
		}
		return di < dj
	})
	for _, relative := range paths {
		object := snapshot[relative]
		path := filepath.Join(root, relative)
		switch object.kind {
		case ObjectDirectory:
			if err := os.MkdirAll(path, object.mode.Perm()); err != nil {
				return err
			}
		case ObjectRegularText, ObjectBinary:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, object.content, object.mode.Perm()); err != nil {
				return err
			}
		case ObjectSymlink:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(object.target, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func diffTrees(before, after treeSnapshot) []ToolDelta {
	keys := map[string]bool{}
	for path := range before {
		keys[path] = true
	}
	for path := range after {
		keys[path] = true
	}
	paths := make([]string, 0, len(keys))
	for path := range keys {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var result []ToolDelta
	for _, path := range paths {
		left, leftOK := before[path]
		right, rightOK := after[path]
		if leftOK && rightOK && left.kind == right.kind && left.mode == right.mode && left.target == right.target && bytes.Equal(left.content, right.content) {
			continue
		}
		classification := "other"
		if leftOK && rightOK && left.kind == ObjectRegularText && right.kind == ObjectRegularText {
			classification = classifyTextDelta(left.content, right.content)
		}
		result = append(result, ToolDelta{
			Path: path, Before: left.content, After: right.content, BeforeExists: leftOK, AfterExists: rightOK,
			Classification: classification, BeforeKind: left.kind, AfterKind: right.kind,
			BeforeMode: uint32(left.mode.Perm()), AfterMode: uint32(right.mode.Perm()), BeforeTarget: left.target, AfterTarget: right.target,
		})
	}
	return result
}

func classifyTextDelta(before, after []byte) string {
	if bytes.Equal(bytes.ReplaceAll(before, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(after, []byte("\r\n"), []byte("\n"))) {
		return "line_endings"
	}
	if strings.TrimSpace(string(before)) == strings.TrimSpace(string(after)) {
		return "whitespace"
	}
	return "other_text"
}

func protectedBytesChanged(path string, before, after []byte) bool {
	if filepath.Ext(path) != ".go" {
		return false
	}
	tokens := func(content []byte) []string {
		fileSet := token.NewFileSet()
		file := fileSet.AddFile(path, fileSet.Base(), len(content))
		var scanner goscanner.Scanner
		scanner.Init(file, content, nil, goscanner.ScanComments)
		var protected []string
		for {
			_, kind, literal := scanner.Scan()
			if kind == token.EOF {
				break
			}
			if kind == token.STRING || kind == token.CHAR || kind == token.COMMENT {
				protected = append(protected, kind.String()+":"+literal)
			}
		}
		return protected
	}
	left, right := tokens(before), tokens(after)
	if len(left) != len(right) {
		return true
	}
	for index := range left {
		if left[index] != right[index] {
			return true
		}
	}
	return false
}

func matchDeclared(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == path {
			return true
		}
		if ok, _ := filepath.Match(pattern, path); ok {
			return true
		}
		if strings.HasPrefix(pattern, "**/") {
			if ok, _ := filepath.Match(strings.TrimPrefix(pattern, "**/"), filepath.Base(path)); ok {
				return true
			}
		}
	}
	return false
}

type cappedCommandOutput struct {
	mu sync.Mutex
	bytes.Buffer
	limit   int
	clipped bool
}

func (w *cappedCommandOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(data)
	if w.Buffer.Len() < w.limit {
		keep := w.limit - w.Buffer.Len()
		if keep > len(data) {
			keep = len(data)
		}
		_, _ = w.Buffer.Write(data[:keep])
	}
	if w.Buffer.Len() >= w.limit && n > 0 {
		w.clipped = true
	}
	return n, nil
}
func (w *cappedCommandOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	value := w.Buffer.String()
	if w.clipped {
		value += "\n[output capped]"
	}
	return value
}
func isolatedCommandEnv() ([]string, func(), error) {
	allowed := []string{
		"PATH", "TMPDIR", "LANG", "LC_ALL", "SYSTEMROOT", "WINDIR", "PATHEXT",
		"MISE_DATA_DIR", "MISE_CACHE_DIR", "MISE_CONFIG_DIR", "RUSTUP_HOME",
		"CARGO_HOME", "GRADLE_USER_HOME", "GOMODCACHE", "NUGET_PACKAGES",
	}
	var result []string
	present := map[string]bool{}
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok {
			result = append(result, key+"="+value)
			present[key] = true
		}
	}
	if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
		defaults := map[string]string{
			"MISE_DATA_DIR":    filepath.Join(userHome, ".local", "share", "mise"),
			"MISE_CACHE_DIR":   filepath.Join(userHome, ".cache", "mise"),
			"MISE_CONFIG_DIR":  filepath.Join(userHome, ".config", "mise"),
			"RUSTUP_HOME":      filepath.Join(userHome, ".rustup"),
			"CARGO_HOME":       filepath.Join(userHome, ".cargo"),
			"GRADLE_USER_HOME": filepath.Join(userHome, ".gradle"),
			"GOMODCACHE":       filepath.Join(userHome, "go", "pkg", "mod"),
			"NUGET_PACKAGES":   filepath.Join(userHome, ".nuget", "packages"),
		}
		for key, path := range defaults {
			if present[key] {
				continue
			}
			if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
				result = append(result, key+"="+path)
			}
		}
	}
	runtimeRoot, err := os.MkdirTemp("", "huyang-command-env-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create isolated command environment: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(runtimeRoot) }
	home := filepath.Join(runtimeRoot, "home")
	cache := filepath.Join(runtimeRoot, "cache")
	goCache := filepath.Join(runtimeRoot, "go-build")
	for _, dir := range []string{home, cache, goCache} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("create isolated command environment: %w", err)
		}
	}
	if persistent := persistentBuildCache(); persistent != "" {
		goCache = persistent
	}
	result = append(result,
		"HOME="+home,
		"USERPROFILE="+home,
		"XDG_CACHE_HOME="+cache,
		"LOCALAPPDATA="+cache,
		"GOCACHE="+goCache,
	)
	return result, cleanup, nil
}

// persistentBuildCache is the compiler build cache every verification stage
// shares, or an empty string when it cannot be used.
//
// HOME and XDG_CACHE_HOME stay throwaway so a repository command can neither
// read the user's configuration nor leave anything in the user's home. A
// compiler build cache is different in kind: its entries are addressed by the
// hash of their inputs, so a reused entry cannot make a later build wrong,
// which is the same reasoning that forwards GOMODCACHE. Giving every stage a
// fresh one instead made each of them compile the whole package set from
// scratch: on the benchmark fixture a cold `go build ./...` takes 7.7 s
// against 0.06 s warm, and one verify_run pays that twice, once per stage.
//
// The cache lives beside the service's own state rather than in the user's
// cache directory, so it is still Huyang's and not the user's, and the Go
// toolchain trims it on its own schedule; deleting the directory reclaims it.
// HUYANG_COMMAND_CACHE=off restores a throwaway cache per run.
func persistentBuildCache() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HUYANG_COMMAND_CACHE"))) {
	case "off", "0", "false":
		return ""
	}
	base := strings.TrimSpace(os.Getenv("HUYANG_STATE_DIR"))
	if base == "" {
		base = strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				return ""
			}
			base = filepath.Join(home, ".local", "state")
		}
		base = filepath.Join(base, "huyang")
	}
	path := filepath.Join(base, "command-cache", "go-build")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return ""
	}
	return path
}

func commandStage(ctx context.Context, root, revision, name, mode string, command CommandPolicy, policy PipelinePolicy, mutating bool) (VerificationStage, []ToolDelta, error) {
	stage := VerificationStage{Stage: name, Implementation: append([]string(nil), command.Command...), Mode: mode, StartedRevision: revision, Exit: -1, Coverage: Coverage{Complete: true}}
	if len(command.Command) == 0 {
		stage.Status = VerificationSkipped
		stage.Coverage.Complete = false
		stage.Coverage.Skipped = []string{"not_configured"}
		return stage, nil, nil
	}
	if !policy.Trusted {
		stage.Status = VerificationSkipped
		stage.Coverage.Complete = false
		stage.Coverage.Skipped = []string{"workspace_not_trusted"}
		return stage, nil, nil
	}
	before, err := captureTree(ctx, root, policy.Resource.MaxSnapshotBytes)
	if err != nil {
		stage.Status = VerificationFailed
		return stage, nil, err
	}
	started := time.Now()
	timed, cancel := context.WithTimeout(ctx, time.Duration(policy.Resource.TimeoutSeconds)*time.Second)
	defer cancel()
	environment, cleanupEnvironment, envErr := isolatedCommandEnv()
	if envErr != nil {
		stage.Status = VerificationFailed
		return stage, nil, envErr
	}
	defer cleanupEnvironment()
	runErr := runSandboxCommand(timed, root, environment, command, policy, started, &stage)
	after, snapshotErr := captureTree(context.Background(), root, policy.Resource.MaxSnapshotBytes)
	if snapshotErr != nil {
		_ = restoreTree(root, before)
		stage.Status = VerificationFailed
		return stage, nil, snapshotErr
	}
	delta := diffTrees(before, after)
	for _, change := range delta {
		stage.Writes = append(stage.Writes, change.Path)
	}
	if err := auditToolDelta(delta, command, policy, mutating); err != nil {
		_ = restoreTree(root, before)
		stage.Status = VerificationFailed
		return stage, delta, err
	}
	if runErr != nil {
		stage.Status = commandRunStatus(ctx, timed)
		if mutating {
			_ = restoreTree(root, before)
		}
		return stage, delta, commandRunError(name, stage, runErr)
	}
	stage.Status = VerificationPassed
	return stage, delta, nil
}

// runSandboxCommand executes the command inside root with the isolated environment and
// records its duration, capped output and exit code on the stage.
func runSandboxCommand(ctx context.Context, root string, environment []string, command CommandPolicy, policy PipelinePolicy, started time.Time, stage *VerificationStage) error {
	process := exec.CommandContext(ctx, command.Command[0], command.Command[1:]...)
	configureCommandCancellation(process)
	process.Dir = root
	process.Env = append(environment, "HUYANG_SANDBOX=1")
	var output cappedCommandOutput
	output.limit = policy.Resource.MaxOutputBytes
	process.Stdout, process.Stderr = &output, &output
	runErr := process.Run()
	stage.DurationMS = time.Since(started).Milliseconds()
	stage.Output = output.String()
	if process.ProcessState != nil {
		stage.Exit = process.ProcessState.ExitCode()
	}
	return runErr
}

// auditToolDelta refuses tool writes that exceed the changed-file quota, touch
// non-text objects or protected literals under a mutating stage, fall outside the
// command's declared writes, or happen at all under a non-mutating stage.
func auditToolDelta(delta []ToolDelta, command CommandPolicy, policy PipelinePolicy, mutating bool) error {
	if len(delta) > policy.Resource.MaxChangedFiles {
		return errors.New("verification_changed_file_quota_exceeded")
	}
	for _, change := range delta {
		if mutating && ((change.BeforeExists && change.BeforeKind != ObjectRegularText) || (change.AfterExists && change.AfterKind != ObjectRegularText)) {
			return fmt.Errorf("formatter_unsupported_object: %s", change.Path)
		}
		if !matchDeclared(change.Path, command.DeclaredWrites) {
			return fmt.Errorf("undeclared_tool_write: %s", change.Path)
		}
		if mutating && protectedBytesChanged(change.Path, change.Before, change.After) {
			return fmt.Errorf("protected_literal_changed: %s", change.Path)
		}
	}
	if !mutating && len(delta) > 0 {
		return errors.New("non_mutating_stage_wrote_files")
	}
	return nil
}

func commandRunStatus(ctx, timed context.Context) VerificationStatus {
	if timed.Err() == context.DeadlineExceeded {
		return VerificationTimedOut
	}
	if ctx.Err() != nil {
		return VerificationCancelled
	}
	return VerificationFailed
}

func commandRunError(name string, stage VerificationStage, runErr error) error {
	detail := strings.TrimSpace(stage.Output)
	if detail == "" {
		detail = runErr.Error()
	}
	return fmt.Errorf("verification stage %s failed (exit %d): %s", name, stage.Exit, detail)
}

func parserStage(root, revision string, files []string) VerificationStage {
	stage := VerificationStage{
		Stage: "parser", Mode: "check", StartedRevision: revision, Exit: 0,
		Scope: append([]string(nil), files...), Status: VerificationPassed,
		Coverage: Coverage{Complete: true, FilesConsidered: len(files)},
	}
	supported := 0
	skipped := map[string]bool{}
	fail := func(output string) {
		stage.Status = VerificationFailed
		stage.Exit = 1
		stage.Output += output
	}
	for _, relative := range files {
		extension := strings.ToLower(filepath.Ext(relative))
		if !parserSupports(extension) {
			reason := "parser_unavailable:" + extension
			if extension == "" {
				reason = "parser_unavailable:no_extension"
			}
			if !skipped[reason] {
				stage.Coverage.Skipped = append(stage.Coverage.Skipped, reason)
				skipped[reason] = true
			}
			continue
		}
		supported++
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			fail(err.Error() + "\n")
			continue
		}
		stage.Coverage.FilesRead++
		stage.Coverage.BytesRead += int64(len(content))
		if !utf8.Valid(content) {
			fail(relative + ": invalid UTF-8\n")
			continue
		}
		if output := parseDocument(extension, relative, content); output != "" {
			fail(output)
		}
	}
	if supported == 0 {
		stage.Status = VerificationSkipped
		stage.Exit = -1
		stage.Coverage.Complete = false
		if len(files) == 0 {
			stage.Coverage.Skipped = []string{"no_files_to_parse"}
		}
	} else if supported != len(files) {
		stage.Coverage.Complete = false
		if stage.Status == VerificationPassed {
			stage.Status = VerificationSkipped
			stage.Exit = -1
		}
	}
	return stage
}

func parserSupports(extension string) bool {
	switch extension {
	case ".go", ".json", ".jsonl", ".toml", ".yaml", ".yml", ".md", ".markdown":
		return true
	}
	return false
}

// parseDocument parses content with the native parser for its extension and returns the
// failure text, or "" when the document parses.
func parseDocument(extension, relative string, content []byte) string {
	switch extension {
	case ".go":
		if _, err := parser.ParseFile(token.NewFileSet(), relative, content, parser.AllErrors); err != nil {
			return err.Error() + "\n"
		}
	case ".json":
		var value any
		if err := json.Unmarshal(content, &value); err != nil {
			return relative + ": " + err.Error() + "\n"
		}
	case ".jsonl":
		var output string
		for lineNumber, line := range bytes.Split(content, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var value any
			if err := json.Unmarshal(line, &value); err != nil {
				output += fmt.Sprintf("%s:%d: %v\n", relative, lineNumber+1, err)
			}
		}
		return output
	case ".toml":
		var value map[string]any
		if _, err := toml.Decode(string(content), &value); err != nil {
			return relative + ": " + err.Error() + "\n"
		}
	case ".yaml", ".yml":
		var value any
		if err := yaml.Unmarshal(content, &value); err != nil {
			return relative + ": " + err.Error() + "\n"
		}
	case ".md", ".markdown":
		goldmark.DefaultParser().Parse(text.NewReader(content))
	}
	return ""
}

type commandStageRun struct {
	stage VerificationStage
	delta []ToolDelta
	err   error
}

func commandStageFailure(revision, name, mode string, command CommandPolicy, err error) commandStageRun {
	status := VerificationFailed
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = VerificationCancelled
	}
	return commandStageRun{
		stage: VerificationStage{
			Stage: name, Implementation: append([]string(nil), command.Command...), Mode: mode,
			StartedRevision: revision, Exit: -1, Status: status,
			Coverage: Coverage{Complete: false, Skipped: []string{"command_isolation_failed"}},
		},
		err: err,
	}
}

func runIsolatedCommandBatch(ctx context.Context, root, revision, name, mode string, commands []CommandPolicy, policy PipelinePolicy) ([]commandStageRun, error) {
	snapshot, err := captureTree(ctx, root, policy.Resource.MaxSnapshotBytes)
	if err != nil {
		return nil, err
	}
	results := make([]commandStageRun, len(commands))
	workers := policy.Resource.MaxParallel
	if workers < 1 {
		workers = 1
	}
	if workers > len(commands) {
		workers = len(commands)
	}
	jobs := make(chan int)
	var wait sync.WaitGroup
	run := func(index int) {
		if err := ctx.Err(); err != nil {
			results[index] = commandStageFailure(revision, name, mode, commands[index], err)
			return
		}
		cloneRoot, cloneErr := os.MkdirTemp("", "huyang-verification-command-*")
		if cloneErr != nil {
			results[index] = commandStageFailure(revision, name, mode, commands[index], cloneErr)
			return
		}
		defer os.RemoveAll(cloneRoot)
		tree := filepath.Join(cloneRoot, "tree")
		if cloneErr = os.Mkdir(tree, 0o700); cloneErr == nil {
			cloneErr = restoreTree(tree, snapshot)
		}
		if cloneErr != nil {
			results[index] = commandStageFailure(revision, name, mode, commands[index], cloneErr)
			return
		}
		stage, delta, runErr := commandStage(ctx, tree, revision, name, mode, commands[index], policy, false)
		results[index] = commandStageRun{stage: stage, delta: delta, err: runErr}
	}
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range jobs {
				run(index)
			}
		}()
	}
	for index := range commands {
		jobs <- index
	}
	close(jobs)
	wait.Wait()
	return results, nil
}

func validateParallelCommands(groups ...[]CommandPolicy) error {
	for _, commands := range groups {
		for index, command := range commands {
			if command.Parallel && len(command.DeclaredWrites) > 0 {
				name := command.Name
				if name == "" {
					name = fmt.Sprintf("command_%d", index+1)
				}
				return fmt.Errorf("parallel command %q cannot declare writes", name)
			}
		}
	}
	return nil
}

func runCommandPolicies(ctx context.Context, root, revision, name, mode string, commands []CommandPolicy, policy PipelinePolicy) ([]commandStageRun, error) {
	if err := validateParallelCommands(commands); err != nil {
		return nil, err
	}
	var results []commandStageRun
	for index := 0; index < len(commands); {
		if policy.Resource.MaxParallel <= 1 || !commands[index].Parallel {
			stage, delta, err := commandStage(ctx, root, revision, name, mode, commands[index], policy, false)
			results = append(results, commandStageRun{stage: stage, delta: delta, err: err})
			if err != nil {
				return results, err
			}
			index++
			continue
		}
		end := index + 1
		for end < len(commands) && commands[end].Parallel {
			end++
		}
		batch, err := runIsolatedCommandBatch(ctx, root, revision, name, mode, commands[index:end], policy)
		if err != nil {
			return results, err
		}
		results = append(results, batch...)
		for _, run := range batch {
			if run.err != nil {
				return results, run.err
			}
		}
		index = end
	}
	return results, nil
}

func corroborateParserWithProjectCheck(result *VerificationResult) {
	var passedChecks [][]string
	for _, stage := range result.Stages {
		if stage.Stage == "check" && stage.Status == VerificationPassed {
			passedChecks = append(passedChecks, stage.Implementation)
		}
	}
	if len(passedChecks) == 0 {
		return
	}
	for index := range result.Stages {
		stage := &result.Stages[index]
		if stage.Stage != "parser" || stage.Status != VerificationSkipped {
			continue
		}
		remaining := stage.Coverage.Skipped[:0]
		coveredCount := 0
		for _, reason := range stage.Coverage.Skipped {
			if !strings.HasPrefix(reason, "parser_unavailable:") {
				remaining = append(remaining, reason)
				continue
			}
			extension := strings.TrimPrefix(reason, "parser_unavailable:")
			extensionCovered := false
			for _, implementation := range passedChecks {
				if projectCheckCoversParserExtension(extension, implementation) {
					extensionCovered = true
					break
				}
			}
			if extensionCovered {
				coveredCount++
			} else {
				remaining = append(remaining, reason)
			}
		}
		if coveredCount == 0 {
			stage.Coverage.Skipped = remaining
			continue
		}
		// The parser stage itself still did nothing for these files: its
		// status stays unavailable and its exit stays -1. The corroboration
		// is expressed only through coverage and confidence.
		if len(remaining) == 0 {
			stage.Coverage.Semantic = "configured_project_check"
		} else {
			stage.Coverage.Semantic = "native_parser_and_configured_project_check"
		}
		stage.Confidence = ConfidenceCorroborated
		stage.Coverage.Skipped = append(remaining, fmt.Sprintf("parser_unavailable_corroborated_by_project_check:%d", coveredCount))
		stage.Implementation = append(stage.Implementation, "configured_project_check")
	}
}

// formattingClaims derives the separate formatting statements from the
// stages that ran. Stage names stay descriptive; consumers read these.
func formattingClaims(result VerificationResult, policy PipelinePolicy, requested []string) FormattingClaims {
	var claims FormattingClaims
	gateRequested := false
	for _, name := range requested {
		if name == "format_gate" {
			gateRequested = true
		}
	}
	var gate *VerificationStage
	for index := range result.Stages {
		stage := &result.Stages[index]
		switch {
		case stage.Stage == "format" && stage.Mode == "transform" && stage.Status == VerificationPassed:
			claims.RepositoryFormatted = true
		case stage.Stage == "format_gate":
			gate = stage
		}
	}
	gateConfigured := len(policy.Format.Gate.Command) > 0
	switch {
	case gate != nil && gate.Status == VerificationPassed:
		claims.FormatGatePassed = true
	case gate != nil && gate.Status == VerificationFailed:
		claims.Reasons = append(claims.Reasons, "format_gate_failed")
	case gate != nil:
		// The gate ran into a skip reason, a timeout or a cancellation.
		claims.Provisional = true
		claims.Reasons = append(claims.Reasons, "format_gate_"+string(gate.Status))
		claims.Reasons = append(claims.Reasons, gate.Coverage.Skipped...)
	case gateConfigured && !gateRequested:
		claims.Provisional = true
		claims.Reasons = append(claims.Reasons, "format_gate_not_requested")
	}
	if !gateConfigured && len(policy.Format.Transform.Command) == 0 {
		claims.NotConfigured = true
		claims.Provisional = false
		claims.Reasons = uniqueSorted(append(claims.Reasons, "not_configured"))
	}
	if claims.NotConfigured && gate != nil && gate.Status == VerificationSkipped {
		// The gate stage only records not_configured; that is already the claim.
		claims.Reasons = []string{"not_configured"}
	}
	return claims
}

func projectCheckCoversParserExtension(extension string, implementation []string) bool {
	command := strings.ToLower(strings.Join(implementation, " "))
	tokens := strings.FieldsFunc(command, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character) && !strings.ContainsRune("+-_./", character)
	})
	markers := map[string][]string{
		".py":    {"python", "pyright", "mypy", "ruff"},
		".rb":    {"ruby", "rubocop", "steep", "standardrb"},
		".ts":    {"tsc", "typescript", "eslint", "deno", "npm", "pnpm", "yarn", "bun"},
		".tsx":   {"tsc", "typescript", "eslint", "deno", "npm", "pnpm", "yarn", "bun"},
		".js":    {"node", "eslint", "tsc", "npm", "pnpm", "yarn", "bun"},
		".jsx":   {"node", "eslint", "tsc", "npm", "pnpm", "yarn", "bun"},
		".cs":    {"dotnet", "csc", "msbuild"},
		".kt":    {"kotlinc", "gradle", "mvn"},
		".kts":   {"kotlinc", "gradle", "mvn"},
		".java":  {"javac", "gradle", "mvn"},
		".rs":    {"cargo", "rustc", "clippy"},
		".go":    {"go"},
		".lua":   {"lua", "luac", "nvim"},
		".php":   {"php"},
		".swift": {"swift"},
		".c":     {"clang", "gcc", "cc", "cmake"},
		".h":     {"clang", "gcc", "cc", "cmake"},
		".cc":    {"clang", "g++", "c++", "cmake"},
		".cpp":   {"clang", "g++", "c++", "cmake"},
		".hpp":   {"clang", "g++", "c++", "cmake"},
	}
	for _, marker := range markers[strings.ToLower(extension)] {
		for _, token := range tokens {
			binary := filepath.Base(token)
			if binary == marker || strings.TrimRight(binary, "0123456789.") == marker {
				return true
			}
		}
	}
	return false
}

// RunVerificationPipeline runs the requested stages in the canonical
// pipeline order (see canonicalStageOrder) against the prepared sandbox and
// reports each stage as data plus the separate formatting claims.
func RunVerificationPipeline(ctx context.Context, sandbox *Sandbox, policy PipelinePolicy, request VerificationRequest, prepared []PlanStageFile) (VerificationResult, error) {
	if sandbox == nil {
		return VerificationResult{}, errors.New("prepared sandbox is required")
	}
	stages, err := CanonicalStages(request.Stages)
	if err != nil {
		return VerificationResult{Revision: request.Revision}, err
	}
	request.Stages = stages
	result, err := runVerificationPipeline(ctx, sandbox, policy, request, prepared)
	result.Formatting = formattingClaims(result, policy, stages)
	return result, err
}

func runVerificationPipeline(ctx context.Context, sandbox *Sandbox, policy PipelinePolicy, request VerificationRequest, prepared []PlanStageFile) (VerificationResult, error) {
	run := &pipelineRun{
		ctx: ctx, sandbox: sandbox, policy: policy, request: request,
		result:   VerificationResult{Revision: request.Revision, PreparedFiles: append([]PlanStageFile(nil), prepared...)},
		affected: make([]string, 0, len(prepared)),
	}
	for _, file := range prepared {
		run.affected = append(run.affected, file.Path)
	}
	if request.Transform || (request.ApplyConfiguredTransform && policy.Format.Mode == "transform") {
		if err := run.transform(); err != nil {
			return run.result, err
		}
	}
	// request.Stages is the CanonicalStages output: validated names in pipeline order.
	for _, name := range request.Stages {
		stage, known := pipelineStages[name]
		if !known {
			return run.result, fmt.Errorf("unknown verification stage %q", name)
		}
		if err := stage(run); err != nil {
			return run.result, err
		}
	}
	corroborateParserWithProjectCheck(&run.result)
	err := run.collectPreparedFiles()
	return run.result, err
}

// pipelineStages maps each canonical stage name to the method that runs it. Every stage
// appends its own VerificationStage records to the result and returns the error that
// stops the pipeline, if any.
var pipelineStages = map[string]func(*pipelineRun) error{
	"format_gate": (*pipelineRun).formatGate,
	"parser":      (*pipelineRun).parser,
	"diagnostics": (*pipelineRun).diagnostics,
	"check":       (*pipelineRun).check,
	"tests":       (*pipelineRun).tests,
}

// pipelineRun carries one verification pass over a prepared sandbox. affected lists the
// prepared paths; result accumulates stages and tool deltas in execution order.
type pipelineRun struct {
	ctx      context.Context
	sandbox  *Sandbox
	policy   PipelinePolicy
	request  VerificationRequest
	affected []string
	result   VerificationResult
}

func (run *pipelineRun) record(stage VerificationStage, delta []ToolDelta) {
	run.result.Stages = append(run.result.Stages, stage)
	run.result.ToolDelta = append(run.result.ToolDelta, delta...)
}

func (run *pipelineRun) skipped(name, mode string, coverage Coverage) VerificationStage {
	return VerificationStage{Stage: name, Mode: mode, StartedRevision: run.request.Revision, Exit: -1, Status: VerificationSkipped, Coverage: coverage}
}

// transform runs the configured formatter over the sandbox and requires a second pass to
// change nothing; a non-deterministic formatter restores the tree and fails the stage.
func (run *pipelineRun) transform() error {
	policy, tree, revision := run.policy, run.sandbox.Tree, run.request.Revision
	transform := policy.Format.Transform
	if policy.Format.Scope == "declared" {
		transform.DeclaredWrites = append([]string(nil), run.affected...)
	}
	original, snapshotErr := captureTree(run.ctx, tree, policy.Resource.MaxSnapshotBytes)
	if snapshotErr != nil {
		return snapshotErr
	}
	stage, delta, err := commandStage(run.ctx, tree, revision, "format", "transform", transform, policy, true)
	if policy.Format.Scope == "declared" {
		stage.Scope = append([]string(nil), run.affected...)
	} else {
		stage.Scope = []string{"**"}
	}
	run.record(stage, delta)
	if err != nil {
		return err
	}
	if run.request.Transform && stage.Status != VerificationPassed {
		return errors.New("explicit formatter transform is unavailable")
	}
	if stage.Status != VerificationPassed {
		return nil
	}
	_, secondDelta, secondErr := commandStage(run.ctx, tree, revision, "format_idempotency", "check", transform, policy, true)
	if secondErr != nil || len(secondDelta) > 0 {
		_ = restoreTree(tree, original)
		run.result.Stages[len(run.result.Stages)-1].Status = VerificationFailed
		return errors.New("formatter_nondeterministic: second pass changed prepared bytes")
	}
	return nil
}

func (run *pipelineRun) formatGate() error {
	stage, delta, err := commandStage(run.ctx, run.sandbox.Tree, run.request.Revision, "format_gate", "check", run.policy.Format.Gate, run.policy, false)
	run.record(stage, delta)
	return err
}

func (run *pipelineRun) parser() error {
	stage := parserStage(run.sandbox.Tree, run.request.Revision, run.affected)
	run.result.Stages = append(run.result.Stages, stage)
	if stage.Status == VerificationFailed {
		return errors.New("parser verification failed")
	}
	return nil
}

func (run *pipelineRun) diagnostics() error {
	if run.request.DiagnosticVerifier == nil {
		coverage := Coverage{Complete: false, Skipped: []string{"diagnostic_provider_unavailable"}, Semantic: string(ConfidenceUnavailable)}
		run.result.Stages = append(run.result.Stages, run.skipped("diagnostics", "provider", coverage))
		return nil
	}
	stage, err := run.request.DiagnosticVerifier(run.ctx, run.request.Revision, run.result.PreparedFiles)
	run.result.Stages = append(run.result.Stages, stage)
	return err
}

func (run *pipelineRun) check() error {
	if len(run.policy.Check) == 0 {
		run.result.Stages = append(run.result.Stages, run.skipped("check", "check", Coverage{Complete: false, Skipped: []string{"not_configured"}}))
	}
	runs, err := runCommandPolicies(run.ctx, run.sandbox.Tree, run.request.Revision, "check", "check", run.policy.Check, run.policy)
	for _, item := range runs {
		run.record(item.stage, item.delta)
	}
	return err
}

func (run *pipelineRun) tests() error {
	if run.request.TestScope == "affected" {
		return run.affectedTests()
	}
	return run.fullTests()
}

func (run *pipelineRun) affectedTests() error {
	stages, targeted, err := runAffectedTests(run.ctx, run.sandbox, run.policy, run.request, run.affected)
	run.result.Stages = append(run.result.Stages, stages...)
	run.result.Targeted = targeted
	if targeted != nil {
		run.result.Impact = &targeted.Graph
	}
	return err
}

// fullTests runs every configured test command and records the full-suite gate and the
// test history; a failing command fails the gate after every command has been recorded.
func (run *pipelineRun) fullTests() error {
	result, policy, revision := &run.result, run.policy, run.request.Revision
	result.FullTestGate = "not_configured"
	if len(policy.Tests) == 0 {
		stage := run.skipped("tests", "check", Coverage{Complete: false, Skipped: []string{"not_configured"}})
		stage.TestScope, stage.TestVerdict = "full", "full_tests_unavailable"
		result.Stages = append(result.Stages, stage)
		return nil
	}
	var history []TestHistoryEntry
	result.FullTestGate = "full_tests_passed"
	stageStart := len(result.Stages)
	runs, runErr := runCommandPolicies(run.ctx, run.sandbox.Tree, revision, "tests", "check", policy.Tests, policy)
	failedIndex := -1
	for index, item := range runs {
		command := policy.Tests[index]
		stage, testName := labelFullTest(index, command, item.stage)
		if stage.Status != VerificationPassed {
			result.FullTestGate = "full_tests_unavailable"
		}
		run.record(stage, item.delta)
		history = append(history, historyEntry(revision, "full", testName, command.Variants, stage))
		if failedIndex < 0 && item.err != nil {
			failedIndex = index
		}
	}
	if runErr != nil {
		result.FullTestGate = "full_tests_failed"
		if failedIndex >= 0 {
			result.Stages[stageStart+failedIndex].TestVerdict = result.FullTestGate
		}
		_ = recordTestHistory(run.request.TestHistoryPath, history)
		return runErr
	}
	return recordTestHistory(run.request.TestHistoryPath, history)
}

// labelFullTest marks one full-suite command run with its scope, verdict and test name.
func labelFullTest(index int, command CommandPolicy, stage VerificationStage) (VerificationStage, string) {
	testName := command.Name
	if testName == "" {
		testName = fmt.Sprintf("test_%d", index+1)
	}
	stage.TestScope = "full"
	stage.TestVerdict = "full_tests_passed"
	if stage.Status != VerificationPassed {
		stage.TestVerdict = "full_tests_unavailable"
	}
	stage.ExecutedTests = []string{testName}
	return stage, testName
}

// collectPreparedFiles reads the final sandbox bytes back into the prepared files so the
// commit stages exactly what verification saw, including formatter output.
func (run *pipelineRun) collectPreparedFiles() error {
	final, err := captureTree(run.ctx, run.sandbox.Tree, run.policy.Resource.MaxSnapshotBytes)
	if err != nil {
		return err
	}
	for i := range run.result.PreparedFiles {
		file := &run.result.PreparedFiles[i]
		object, exists := final[file.Path]
		file.AfterExists = exists
		if exists {
			file.After = append([]byte(nil), object.content...)
			file.AfterDisk.Size = int64(len(object.content))
			file.AfterDisk.Mode = uint32(object.mode.Perm())
		} else {
			file.After = nil
			file.AfterDisk = DiskSnapshot{Kind: ObjectMissing}
		}
	}
	return nil
}
