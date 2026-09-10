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
	"unicode/utf8"

	"github.com/BurntSushi/toml"
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
	} `toml:"resource" json:"resource"`
	Trusted       bool   `toml:"-" json:"trusted"`
	ProjectConfig string `toml:"-" json:"project_config,omitempty"`
	UserConfig    string `toml:"-" json:"user_config,omitempty"`
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
}

func DefaultPipelinePolicy() PipelinePolicy {
	var policy PipelinePolicy
	policy.Version = 1
	policy.Format.Mode = "check"
	policy.Format.Scope = "declared"
	policy.Resource.TimeoutSeconds = 120
	policy.Resource.MaxOutputBytes = 256 << 10
	policy.Resource.MaxSnapshotBytes = 64 << 20
	policy.Resource.MaxChangedFiles = 256
	return policy
}

func LoadPipelinePolicy(projectRoot, userConfig string) (PipelinePolicy, error) {
	policy := DefaultPipelinePolicy()
	projectPath := filepath.Join(projectRoot, ".huyang.toml")
	if content, err := os.ReadFile(projectPath); err == nil {
		if _, err := toml.Decode(string(content), &policy); err != nil {
			return PipelinePolicy{}, fmt.Errorf("invalid project policy: %w", err)
		}
		policy.ProjectConfig = projectPath
	} else if !errors.Is(err, os.ErrNotExist) {
		return PipelinePolicy{}, err
	}
	if userConfig == "" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(home, ".config")
			}
		}
		if base != "" {
			userConfig = filepath.Join(base, "huyang", "config.toml")
		}
	}
	var user userPipelinePolicy
	if userConfig != "" {
		if content, err := os.ReadFile(userConfig); err == nil {
			if _, err := toml.Decode(string(content), &user); err != nil {
				return PipelinePolicy{}, fmt.Errorf("invalid user policy: %w", err)
			}
			policy.UserConfig = userConfig
		} else if !errors.Is(err, os.ErrNotExist) {
			return PipelinePolicy{}, err
		}
	}
	resolvedRoot, err := filepath.EvalSymlinks(projectRoot)
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
	if policy.Version != 1 {
		return PipelinePolicy{}, fmt.Errorf("unsupported huyang policy version %d", policy.Version)
	}
	if policy.Format.Mode == "" {
		policy.Format.Mode = "check"
	}
	if policy.Format.Mode != "off" && policy.Format.Mode != "check" && policy.Format.Mode != "transform" {
		return PipelinePolicy{}, fmt.Errorf("invalid format mode %q", policy.Format.Mode)
	}
	if policy.Format.Scope == "" {
		policy.Format.Scope = "declared"
	}
	if policy.Format.Scope != "declared" && policy.Format.Scope != "whole_repository" {
		return PipelinePolicy{}, fmt.Errorf("invalid format scope %q", policy.Format.Scope)
	}
	return policy, nil
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
				return errors.New("verification_snapshot_quota_exceeded")
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
func sanitizedCommandEnv() []string {
	allowed := []string{"PATH", "TMPDIR", "LANG", "LC_ALL", "SYSTEMROOT", "WINDIR", "PATHEXT"}
	var result []string
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok {
			result = append(result, key+"="+value)
		}
	}
	return result
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
	process := exec.CommandContext(timed, command.Command[0], command.Command[1:]...)
	process.Dir = root
	process.Env = append(sanitizedCommandEnv(), "HUYANG_SANDBOX=1")
	var output cappedCommandOutput
	output.limit = policy.Resource.MaxOutputBytes
	process.Stdout, process.Stderr = &output, &output
	runErr := process.Run()
	stage.DurationMS = time.Since(started).Milliseconds()
	stage.Output = output.String()
	if process.ProcessState != nil {
		stage.Exit = process.ProcessState.ExitCode()
	}
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
	if len(delta) > policy.Resource.MaxChangedFiles {
		_ = restoreTree(root, before)
		stage.Status = VerificationFailed
		return stage, delta, errors.New("verification_changed_file_quota_exceeded")
	}
	for _, change := range delta {
		if mutating && ((change.BeforeExists && change.BeforeKind != ObjectRegularText) || (change.AfterExists && change.AfterKind != ObjectRegularText)) {
			_ = restoreTree(root, before)
			stage.Status = VerificationFailed
			return stage, delta, fmt.Errorf("formatter_unsupported_object: %s", change.Path)
		}
		if !matchDeclared(change.Path, command.DeclaredWrites) {
			_ = restoreTree(root, before)
			stage.Status = VerificationFailed
			return stage, delta, fmt.Errorf("undeclared_tool_write: %s", change.Path)
		}
		if mutating && protectedBytesChanged(change.Path, change.Before, change.After) {
			_ = restoreTree(root, before)
			stage.Status = VerificationFailed
			return stage, delta, fmt.Errorf("protected_literal_changed: %s", change.Path)
		}
	}
	if !mutating && len(delta) > 0 {
		_ = restoreTree(root, before)
		stage.Status = VerificationFailed
		return stage, delta, errors.New("non_mutating_stage_wrote_files")
	}
	if runErr != nil {
		if timed.Err() == context.DeadlineExceeded {
			stage.Status = VerificationTimedOut
		} else if ctx.Err() != nil {
			stage.Status = VerificationCancelled
		} else {
			stage.Status = VerificationFailed
		}
		if mutating {
			_ = restoreTree(root, before)
		}
		return stage, delta, runErr
	}
	stage.Status = VerificationPassed
	return stage, delta, nil
}

func parserStage(root, revision string, files []string) VerificationStage {
	stage := VerificationStage{Stage: "parser", Mode: "check", StartedRevision: revision, Exit: 0, Scope: append([]string(nil), files...), Status: VerificationPassed, Coverage: Coverage{Complete: true}}
	for _, relative := range files {
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			stage.Status = VerificationFailed
			stage.Exit = 1
			stage.Output += err.Error() + "\n"
			continue
		}
		if !utf8.Valid(content) {
			stage.Status = VerificationFailed
			stage.Exit = 1
			stage.Output += relative + ": invalid UTF-8\n"
			continue
		}
		switch filepath.Ext(relative) {
		case ".go":
			if _, err := parser.ParseFile(token.NewFileSet(), relative, content, parser.AllErrors); err != nil {
				stage.Status = VerificationFailed
				stage.Exit = 1
				stage.Output += err.Error() + "\n"
			}
		case ".json":
			var value any
			if err := json.Unmarshal(content, &value); err != nil {
				stage.Status = VerificationFailed
				stage.Exit = 1
				stage.Output += relative + ": " + err.Error() + "\n"
			}
		}
	}
	return stage
}

func RunVerificationPipeline(ctx context.Context, sandbox *Sandbox, policy PipelinePolicy, request VerificationRequest, prepared []PlanStageFile) (VerificationResult, error) {
	if sandbox == nil {
		return VerificationResult{}, errors.New("prepared sandbox is required")
	}
	result := VerificationResult{Revision: request.Revision, PreparedFiles: append([]PlanStageFile(nil), prepared...)}
	affected := make([]string, 0, len(prepared))
	for _, file := range prepared {
		affected = append(affected, file.Path)
	}
	if request.Transform || (request.ApplyConfiguredTransform && policy.Format.Mode == "transform") {
		transform := policy.Format.Transform
		if policy.Format.Scope == "declared" {
			transform.DeclaredWrites = append([]string(nil), affected...)
		}
		original, snapshotErr := captureTree(ctx, sandbox.Tree, policy.Resource.MaxSnapshotBytes)
		if snapshotErr != nil {
			return result, snapshotErr
		}
		stage, delta, err := commandStage(ctx, sandbox.Tree, request.Revision, "format", "transform", transform, policy, true)
		if policy.Format.Scope == "declared" {
			stage.Scope = append([]string(nil), affected...)
		} else {
			stage.Scope = []string{"**"}
		}
		result.Stages = append(result.Stages, stage)
		result.ToolDelta = append(result.ToolDelta, delta...)
		if err != nil {
			return result, err
		}
		if request.Transform && stage.Status != VerificationPassed {
			return result, errors.New("explicit formatter transform is unavailable")
		}
		if stage.Status == VerificationPassed {
			_, secondDelta, secondErr := commandStage(ctx, sandbox.Tree, request.Revision, "format_idempotency", "check", transform, policy, true)
			if secondErr != nil || len(secondDelta) > 0 {
				_ = restoreTree(sandbox.Tree, original)
				result.Stages[len(result.Stages)-1].Status = VerificationFailed
				return result, errors.New("formatter_nondeterministic: second pass changed prepared bytes")
			}
		}
	}
	for _, name := range request.Stages {
		switch name {
		case "format_gate":
			stage, delta, err := commandStage(ctx, sandbox.Tree, request.Revision, name, "check", policy.Format.Gate, policy, false)
			result.Stages = append(result.Stages, stage)
			result.ToolDelta = append(result.ToolDelta, delta...)
			if err != nil {
				return result, err
			}
		case "parser":
			stage := parserStage(sandbox.Tree, request.Revision, affected)
			result.Stages = append(result.Stages, stage)
			if stage.Status != VerificationPassed {
				return result, errors.New("parser verification failed")
			}
		case "diagnostics":
			if request.DiagnosticVerifier == nil {
				result.Stages = append(result.Stages, VerificationStage{Stage: name, Mode: "provider", StartedRevision: request.Revision, Exit: -1, Status: VerificationSkipped, Coverage: Coverage{Complete: false, Skipped: []string{"diagnostic_provider_unavailable"}, Semantic: string(ConfidenceUnavailable)}})
				continue
			}
			stage, err := request.DiagnosticVerifier(ctx, request.Revision, result.PreparedFiles)
			result.Stages = append(result.Stages, stage)
			if err != nil {
				return result, err
			}
		case "check":
			if len(policy.Check) == 0 {
				result.Stages = append(result.Stages, VerificationStage{Stage: name, Mode: "check", StartedRevision: request.Revision, Exit: -1, Status: VerificationSkipped, Coverage: Coverage{Complete: false, Skipped: []string{"not_configured"}}})
			}
			for _, command := range policy.Check {
				stage, delta, err := commandStage(ctx, sandbox.Tree, request.Revision, name, "check", command, policy, false)
				result.Stages = append(result.Stages, stage)
				result.ToolDelta = append(result.ToolDelta, delta...)
				if err != nil {
					return result, err
				}
			}
		case "tests":
			if request.TestScope == "affected" {
				stages, targeted, err := runAffectedTests(ctx, sandbox, policy, request, affected)
				result.Stages = append(result.Stages, stages...)
				result.Targeted = targeted
				if targeted != nil {
					result.Impact = &targeted.Graph
				}
				if err != nil {
					return result, err
				}
				continue
			}
			result.FullTestGate = "not_configured"
			if len(policy.Tests) == 0 {
				result.Stages = append(result.Stages, VerificationStage{Stage: name, Mode: "check", StartedRevision: request.Revision, Exit: -1, Status: VerificationSkipped, TestScope: "full", TestVerdict: "full_tests_unavailable", Coverage: Coverage{Complete: false, Skipped: []string{"not_configured"}}})
				continue
			}
			var history []TestHistoryEntry
			result.FullTestGate = "full_tests_passed"
			for index, command := range policy.Tests {
				stage, delta, err := commandStage(ctx, sandbox.Tree, request.Revision, name, "check", command, policy, false)
				testName := command.Name
				if testName == "" {
					testName = fmt.Sprintf("test_%d", index+1)
				}
				stage.TestScope = "full"
				stage.TestVerdict = "full_tests_passed"
				stage.ExecutedTests = []string{testName}
				result.Stages = append(result.Stages, stage)
				result.ToolDelta = append(result.ToolDelta, delta...)
				history = append(history, historyEntry(request.Revision, "full", testName, command.Variants, stage))
				if err != nil {
					result.FullTestGate = "full_tests_failed"
					result.Stages[len(result.Stages)-1].TestVerdict = result.FullTestGate
					_ = recordTestHistory(request.TestHistoryPath, history)
					return result, err
				}
			}
			if err := recordTestHistory(request.TestHistoryPath, history); err != nil {
				return result, err
			}
		default:
			return result, fmt.Errorf("unknown verification stage %q", name)
		}
	}
	final, err := captureTree(ctx, sandbox.Tree, policy.Resource.MaxSnapshotBytes)
	if err != nil {
		return result, err
	}
	for i := range result.PreparedFiles {
		file := &result.PreparedFiles[i]
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
	return result, nil
}
