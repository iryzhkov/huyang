package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func trustedPolicy(t *testing.T, root string) PipelinePolicy {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, []byte("[trust]\nroots = [\""+strings.ReplaceAll(root, "\\", "\\\\")+"\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadPipelinePolicy(root, config)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Trusted {
		t.Fatal("project root was not trusted by explicit user policy")
	}
	return policy
}

func pipelineSandbox(t *testing.T, files map[string]string) (*Sandbox, []PlanStageFile) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sandbox, err := MaterializeSandbox(context.Background(), root, t.TempDir(), ID("ws_0123456789abcdef0123456789abcdef"), "plan_pipeline", 1, "base_1", DefaultSandboxLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Cleanup() })
	var prepared []PlanStageFile
	for name, content := range files {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		prepared = append(prepared, PlanStageFile{Path: name, Before: []byte(content), After: []byte(content), BeforeExists: true, AfterExists: true, AfterDisk: DiskSnapshot{Kind: ObjectRegularText, Mode: uint32(info.Mode().Perm()), Size: int64(len(content))}})
	}
	return sandbox, prepared
}

func TestPipelinePolicyRequiresUserTrustAndTightensResources(t *testing.T) {
	if got := DefaultPipelinePolicy().Resource.MaxParallel; got != 1 {
		t.Fatalf("default max_parallel = %d, want 1", got)
	}
	root := t.TempDir()
	project := "version = 1\n[format]\nmode = \"transform\"\nscope = \"declared\"\n[format.transform]\ncommand = [\"tool\", \"--write\"]\ndeclared_writes = [\"*.go\"]\n[resource]\ntimeout_seconds = 90\nmax_parallel = 4\n"
	if err := os.WriteFile(filepath.Join(root, ".huyang.toml"), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	untrusted, err := LoadPipelinePolicy(root, filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if untrusted.Trusted {
		t.Fatal("repository command trusted without user declaration")
	}
	user := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(user, []byte("[trust]\nroots = [\""+root+"\"]\n[resource]\ntimeout_seconds = 3\nmax_output_bytes = 1024\nmax_parallel = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	trusted, err := LoadPipelinePolicy(root, user)
	if err != nil {
		t.Fatal(err)
	}
	if !trusted.Trusted || trusted.Resource.TimeoutSeconds != 3 || trusted.Resource.MaxOutputBytes != 1024 || trusted.Resource.MaxParallel != 2 {
		t.Fatalf("layered policy = %+v", trusted)
	}
}

func TestVerificationTransformCapturesCompleteToolDelta(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	sandbox, prepared := pipelineSandbox(t, map[string]string{"a.go": "package p\r\n", "keep.txt": "same\n"})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Format.Mode = "transform"
	policy.Format.Transform = CommandPolicy{Command: []string{"sh", "-c", "printf 'package p\\n' > a.go"}, DeclaredWrites: []string{"a.go"}}
	result, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{Revision: "prep_1", Transform: true}, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolDelta) != 1 || result.ToolDelta[0].Path != "a.go" || result.ToolDelta[0].Classification != "line_endings" {
		t.Fatalf("tool delta = %+v", result.ToolDelta)
	}
	if got, err := os.ReadFile(filepath.Join(sandbox.Tree, "a.go")); err != nil || string(got) != "package p\n" {
		t.Fatalf("prepared bytes = %q, %v", got, err)
	}
	var preparedGo []byte
	for _, file := range result.PreparedFiles {
		if file.Path == "a.go" {
			preparedGo = file.After
		}
	}
	if string(preparedGo) != "package p\n" {
		t.Fatalf("prepared postimage = %q", preparedGo)
	}
}

func TestVerificationUndeclaredWriteRollsBackExactly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	sandbox, prepared := pipelineSandbox(t, map[string]string{"a.go": "package p\n", "keep.txt": "exact\r\n"})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Format.Mode = "transform"
	policy.Format.Transform = CommandPolicy{Command: []string{"sh", "-c", "printf changed > keep.txt; printf generated > new.out"}, DeclaredWrites: []string{"a.go"}}
	result, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{Revision: "prep_1", Transform: true}, prepared)
	if err == nil || !strings.Contains(err.Error(), "undeclared_tool_write") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	got, readErr := os.ReadFile(filepath.Join(sandbox.Tree, "keep.txt"))
	if readErr != nil || string(got) != "exact\r\n" {
		t.Fatalf("rollback bytes = %q, %v", got, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(sandbox.Tree, "new.out")); !os.IsNotExist(statErr) {
		t.Fatalf("generated output survived rollback: %v", statErr)
	}
}

func TestVerificationRejectsProtectedLiteralRewrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	original := "package p\nvar value = \"keep\"\n"
	sandbox, prepared := pipelineSandbox(t, map[string]string{"a.go": original})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Format.Mode = "transform"
	policy.Format.Transform = CommandPolicy{Command: []string{"sh", "-c", "sed -i s/keep/changed/ a.go"}, DeclaredWrites: []string{"a.go"}}
	result, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{Revision: "prep_1", Transform: true}, prepared)
	if err == nil || !strings.Contains(err.Error(), "protected_literal_changed") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	got, _ := os.ReadFile(filepath.Join(sandbox.Tree, "a.go"))
	if string(got) != original {
		t.Fatalf("literal rewrite survived rollback: %q", got)
	}
}

func TestVerificationRejectsNondeterministicFormatterAndRestoresOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	sandbox, prepared := pipelineSandbox(t, map[string]string{"a.txt": "one\n"})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Format.Mode = "transform"
	policy.Format.Transform = CommandPolicy{
		Command:        []string{"sh", "-c", "if grep -q one a.txt; then printf two > a.txt; else printf one > a.txt; fi"},
		DeclaredWrites: []string{"a.txt"},
	}
	result, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{Revision: "prep_1", Transform: true}, prepared)
	if err == nil || !strings.Contains(err.Error(), "formatter_nondeterministic") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	got, _ := os.ReadFile(filepath.Join(sandbox.Tree, "a.txt"))
	if string(got) != "one\n" {
		t.Fatalf("nondeterministic rollback = %q", got)
	}
}

func TestVerificationFormatGateCannotMutateAndParserSeesPreparedBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	sandbox, prepared := pipelineSandbox(t, map[string]string{"a.go": "package p\nfunc broken( {\n"})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Format.Gate = CommandPolicy{Command: []string{"sh", "-c", "printf dirty >> a.go"}, DeclaredWrites: []string{"a.go"}}
	result, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{Revision: "prep_1", Stages: []string{"format_gate"}}, prepared)
	if err == nil || !strings.Contains(err.Error(), "non_mutating_stage_wrote_files") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	got, _ := os.ReadFile(filepath.Join(sandbox.Tree, "a.go"))
	if string(got) != "package p\nfunc broken( {\n" {
		t.Fatalf("gate mutation survived: %q", got)
	}
	result, err = RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{Revision: "prep_1", Stages: []string{"parser"}}, prepared)
	if err == nil || len(result.Stages) != 1 || result.Stages[0].Status != VerificationFailed {
		t.Fatalf("parser result = %+v, %v", result, err)
	}
}

func TestParserVerificationReportsActualCoverage(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"valid.go":   "package p\n",
		"valid.json": "{\"ok\":true}\n",
		"script.rb":  "puts 'ok'\n",
		"README":     "plain text\n",
		"broken.go":  "package p\nfunc broken( {\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		files      []string
		status     VerificationStatus
		exit       int
		complete   bool
		considered int
		read       int
		skipped    string
	}{
		{name: "empty scope", status: VerificationSkipped, exit: -1, complete: false, skipped: "no_files_to_parse"},
		{name: "unsupported only", files: []string{"script.rb"}, status: VerificationSkipped, exit: -1, complete: false, considered: 1, skipped: "parser_unavailable:.rb"},
		{name: "all supported", files: []string{"valid.go", "valid.json"}, status: VerificationPassed, exit: 0, complete: true, considered: 2, read: 2},
		{name: "mixed support", files: []string{"valid.go", "script.rb", "README"}, status: VerificationSkipped, exit: -1, complete: false, considered: 3, read: 1, skipped: "parser_unavailable:.rb"},
		{name: "malformed supported", files: []string{"broken.go"}, status: VerificationFailed, exit: 1, complete: true, considered: 1, read: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stage := parserStage(root, "wsrev_2", test.files)
			if stage.Status != test.status || stage.Exit != test.exit || stage.Coverage.Complete != test.complete ||
				stage.Coverage.FilesConsidered != test.considered || stage.Coverage.FilesRead != test.read {
				t.Fatalf("parser stage = %+v", stage)
			}
			if test.read > 0 && stage.Coverage.BytesRead == 0 {
				t.Fatalf("parser did not account for read bytes: %+v", stage.Coverage)
			}
			if test.skipped != "" && !containsString(stage.Coverage.Skipped, test.skipped) {
				t.Fatalf("parser skipped reasons = %v, want %q", stage.Coverage.Skipped, test.skipped)
			}
		})
	}
}

func TestUnavailableParserCoverageDoesNotFailPipeline(t *testing.T) {
	sandbox, prepared := pipelineSandbox(t, map[string]string{"script.rb": "puts 'ok'\n"})
	result, err := RunVerificationPipeline(context.Background(), sandbox, DefaultPipelinePolicy(), VerificationRequest{
		Revision: "wsrev_2", Stages: []string{"parser"},
	}, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stages) != 1 || result.Stages[0].Status != VerificationSkipped || result.Stages[0].Coverage.Complete {
		t.Fatalf("parser result = %+v", result)
	}
}

func TestVerificationTimeoutRollsBackTransform(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	sandbox, prepared := pipelineSandbox(t, map[string]string{"a.txt": "before"})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Resource.TimeoutSeconds = 1
	policy.Format.Mode = "transform"
	policy.Format.Transform = CommandPolicy{Command: []string{"sh", "-c", "printf after > a.txt; sleep 5"}, DeclaredWrites: []string{"a.txt"}}
	started := time.Now()
	result, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{Revision: "prep_1", Transform: true}, prepared)
	if err == nil || time.Since(started) > 4*time.Second || result.Stages[0].Status != VerificationTimedOut {
		t.Fatalf("timeout result = %+v, %v", result, err)
	}
	got, _ := os.ReadFile(filepath.Join(sandbox.Tree, "a.txt"))
	if string(got) != "before" {
		t.Fatalf("timeout rollback = %q", got)
	}
}

func TestCommandStageProvidesIsolatedCacheEnvironment(t *testing.T) {
	miseData := t.TempDir()
	t.Setenv("MISE_DATA_DIR", miseData)
	environment, cleanup, err := isolatedCommandEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	values := map[string]string{}
	for _, entry := range environment {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CACHE_HOME", "LOCALAPPDATA", "GOCACHE", "MISE_DATA_DIR"} {
		path := values[name]
		if path == "" {
			t.Fatalf("%s was not configured: %v", name, environment)
		}
		if info, statErr := os.Stat(path); statErr != nil || !info.IsDir() {
			t.Fatalf("%s path %q is not a directory: %v", name, path, statErr)
		}
	}
}

func TestCommandStageFailureIncludesCapturedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	root := t.TempDir()
	policy := DefaultPipelinePolicy()
	policy.Trusted = true
	stage, _, err := commandStage(context.Background(), root, "wsrev_2", "check", "check", CommandPolicy{
		Command: []string{"sh", "-c", "printf 'actionable stderr' >&2; exit 7"},
	}, policy, false)
	if err == nil || stage.Status != VerificationFailed || stage.Exit != 7 {
		t.Fatalf("failed command stage = %+v, err=%v", stage, err)
	}
	for _, want := range []string{"check", "exit 7", "actionable stderr"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failure %q omitted %q", err, want)
		}
	}
}

func TestParallelTestsAreBoundedAndDeterministicallyOrdered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	sandbox, prepared := pipelineSandbox(t, map[string]string{"input.txt": "stable\n"})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Resource.MaxParallel = 2
	policy.Tests = []CommandPolicy{
		{Name: "first", Parallel: true, Command: []string{"sh", "-c", "sleep 1; printf first"}},
		{Name: "second", Parallel: true, Command: []string{"sh", "-c", "sleep 1; printf second"}},
		{Name: "third", Parallel: true, Command: []string{"sh", "-c", "sleep 1; printf third"}},
		{Name: "fourth", Parallel: true, Command: []string{"sh", "-c", "sleep 1; printf fourth"}},
	}
	started := time.Now()
	result, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{
		Revision: "prep_parallel", Stages: []string{"tests"},
	}, prepared)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "second", "third", "fourth"}
	if len(result.Stages) != len(want) {
		t.Fatalf("stages = %+v", result.Stages)
	}
	var totalDuration, longestDuration time.Duration
	for index, expected := range want {
		duration := time.Duration(result.Stages[index].DurationMS) * time.Millisecond
		totalDuration += duration
		if duration > longestDuration {
			longestDuration = duration
		}
		if strings.TrimSpace(result.Stages[index].Output) != expected {
			t.Fatalf("stage %d output = %q, want %q", index, result.Stages[index].Output, expected)
		}
		if len(result.Stages[index].ExecutedTests) != 1 || result.Stages[index].ExecutedTests[0] != expected {
			t.Fatalf("stage %d test metadata = %+v, want %q", index, result.Stages[index].ExecutedTests, expected)
		}
	}
	if elapsed >= totalDuration*3/4 {
		t.Fatalf("parallel tests took %s versus %s summed stage time", elapsed, totalDuration)
	}
	if elapsed < longestDuration*3/2 {
		t.Fatalf("max_parallel=2 was not enforced: elapsed %s, longest stage %s", elapsed, longestDuration)
	}
}

func TestParallelChecksUseIsolatedSandboxesAndPreserveFailureOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	sandbox, prepared := pipelineSandbox(t, map[string]string{"input.txt": "stable\n"})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Resource.MaxParallel = 2
	policy.Check = []CommandPolicy{
		{Name: "rogue", Parallel: true, Command: []string{"sh", "-c", "printf changed > input.txt"}},
		{Name: "reader", Parallel: true, Command: []string{"sh", "-c", "sleep 0.1; grep -qx stable input.txt; printf original"}},
	}
	result, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{
		Revision: "prep_isolated", Stages: []string{"check"},
	}, prepared)
	if err == nil || !strings.Contains(err.Error(), "undeclared_tool_write") {
		t.Fatalf("parallel isolation result = %+v, err = %v", result, err)
	}
	if len(result.Stages) != 2 || strings.TrimSpace(result.Stages[1].Output) != "original" {
		t.Fatalf("parallel result order/content = %+v", result.Stages)
	}
	content, readErr := os.ReadFile(filepath.Join(sandbox.Tree, "input.txt"))
	if readErr != nil || string(content) != "stable\n" {
		t.Fatalf("shared sandbox changed to %q: %v", content, readErr)
	}
}

func TestParallelExecutionRequiresCommandOptIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	sandbox, prepared := pipelineSandbox(t, map[string]string{"input.txt": "stable\n"})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Resource.MaxParallel = 2
	policy.Check = []CommandPolicy{
		{Command: []string{"sh", "-c", "sleep 0.3"}},
		{Command: []string{"sh", "-c", "sleep 0.3"}},
	}
	started := time.Now()
	_, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{
		Revision: "prep_sequential", Stages: []string{"check"},
	}, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 550*time.Millisecond {
		t.Fatalf("commands without parallel opt-in overlapped: %s", elapsed)
	}
}

func TestParallelCommandWithDeclaredWritesIsRejectedBeforeExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	sandbox, prepared := pipelineSandbox(t, map[string]string{"input.txt": "stable\n"})
	policy := trustedPolicy(t, sandbox.Tree)
	policy.Resource.MaxParallel = 2
	marker := filepath.Join(t.TempDir(), "ran")
	policy.Check = []CommandPolicy{{
		Name: "writer", Parallel: true, DeclaredWrites: []string{"input.txt"},
		Command: []string{"sh", "-c", "printf ran > " + marker},
	}}
	result, err := RunVerificationPipeline(context.Background(), sandbox, policy, VerificationRequest{
		Revision: "prep_reject_writes", Stages: []string{"check"},
	}, prepared)
	if err == nil || !strings.Contains(err.Error(), "cannot declare writes") {
		t.Fatalf("declared-write parallel result = %+v, err = %v", result, err)
	}
	if len(result.Stages) != 0 {
		t.Fatalf("rejected command produced stages: %+v", result.Stages)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("rejected command ran: %v", statErr)
	}
}

func TestPipelinePolicyCanReadPreparedConfigUsingCanonicalTrustRoot(t *testing.T) {
	canonicalRoot := t.TempDir()
	preparedRoot := t.TempDir()
	projectPath := filepath.Join(preparedRoot, ".huyang.toml")
	if err := os.WriteFile(projectPath, []byte("version = 1\n[[check]]\nname = \"prepared\"\ncommand = [\"true\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	userConfig := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(userConfig, []byte("[trust]\nroots = [\""+canonicalRoot+"\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadPipelinePolicyForTrustedRoot(preparedRoot, canonicalRoot, userConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Trusted || policy.ProjectConfig != projectPath || len(policy.Check) != 1 || policy.Check[0].Name != "prepared" {
		t.Fatalf("prepared policy = %+v", policy)
	}
}
