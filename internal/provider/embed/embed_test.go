package embed

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"agent99/internal/provider"
)

type traceLog struct {
	mu      sync.Mutex
	entries [][]string
}

func (l *traceLog) add(executable string, args []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, append([]string{executable}, args...))
}

func (l *traceLog) snapshot() [][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([][]string, len(l.entries))
	for i := range l.entries {
		out[i] = append([]string(nil), l.entries[i]...)
	}
	return out
}

func testBackend(t *testing.T, debug bool) (*Backend, string, *traceLog) {
	t.Helper()
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "sample.lua")
	if err := os.WriteFile(file, []byte("local answer = 42\nreturn answer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trace := &traceLog{}
	backend, err := Open(Config{
		Root: root, InitFile: filepath.Join(repo, "tests", "minimal_init.lua"),
		RuntimePath: repo, Debug: debug, Trace: trace.add,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return backend, file, trace
}

func callBufferLines(ctx context.Context, backend *Backend, file, id string) (provider.Result, error) {
	return backend.Call(ctx, provider.Request{
		Context:   provider.RequestContext{RequestID: id, Cancellation: provider.CancellationProviderRestart},
		Operation: "buffer_lines",
		Arguments: map[string]any{"file": file},
	})
}

func TestEmbeddedBootstrapCompletionHealthAndTrace(t *testing.T) {
	backend, file, trace := testBackend(t, true)
	descriptor := backend.Descriptor()
	if descriptor.Backend != "embed" || descriptor.Epoch != 1 || descriptor.ProcessID <= 0 {
		t.Fatalf("unexpected descriptor: %+v", descriptor)
	}
	health := backend.Health(context.Background())
	if health.State != provider.HealthHealthy || health.Epoch != descriptor.Epoch ||
		health.Cancellation != provider.CancellationProviderRestart {
		t.Fatalf("unexpected health: %+v", health)
	}
	result, err := callBufferLines(context.Background(), backend, file, "read-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Value == nil {
		t.Fatal("completion notification returned no value")
	}
	entries := trace.snapshot()
	if len(entries) != 1 {
		t.Fatalf("startup trace count = %d; want 1", len(entries))
	}
	args := entries[0][1:]
	if !slices.Contains(args, "--embed") || !slices.Contains(args, "--headless") {
		t.Fatalf("startup trace does not use embed+headless: %q", entries[0])
	}
	for _, arg := range args {
		if arg == "--server" || arg == "--remote-expr" || arg == "--listen" {
			t.Fatalf("embedded startup trace contains socket polling argument %q: %q", arg, entries[0])
		}
	}
}

func TestCompletionNotificationsDispatchByRequestID(t *testing.T) {
	backend, _, _ := testBackend(t, false)
	backend.mu.Lock()
	g := backend.generation
	backend.mu.Unlock()
	var ignored any
	if err := g.nvim.ExecLua(`
local lsp = require("agent99.lsp")
lsp.dispatch = function(_, args)
    vim.wait(args.delay)
    return args.value
end
return true
`, &ignored); err != nil {
		t.Fatal(err)
	}

	type answer struct {
		id     string
		result provider.Result
		err    error
	}
	answers := make(chan answer, 2)
	start := func(id string, delay int) {
		go func() {
			result, err := backend.Call(context.Background(), provider.Request{
				Context: provider.RequestContext{
					RequestID: id, Cancellation: provider.CancellationProviderRestart,
				},
				Operation: "fixture",
				Arguments: map[string]any{"delay": delay, "value": id},
			})
			answers <- answer{id: id, result: result, err: err}
		}()
	}
	start("slow", 100)
	time.Sleep(10 * time.Millisecond)
	start("fast", 5)

	first := <-answers
	second := <-answers
	if first.err != nil || second.err != nil {
		t.Fatalf("completion errors: first=%v second=%v", first.err, second.err)
	}
	if first.id != "fast" || first.result.Value != "fast" ||
		second.id != "slow" || second.result.Value != "slow" {
		t.Fatalf("completion order/results = %#v then %#v", first, second)
	}
}

func TestCancellationAndDeadlineRestartWithNewEpoch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		code    provider.FailureCode
	}{
		{
			name: "cancel",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			code: provider.FailureCancelled,
		},
		{
			name: "deadline",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			},
			code: provider.FailureDeadline,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, file, trace := testBackend(t, false)
			first := backend.Descriptor()
			blockDispatch(t, backend)
			ctx, cancel := tc.context()
			if tc.name == "cancel" {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			defer cancel()
			_, err := callBufferLines(ctx, backend, file, "blocked")
			var failure *provider.Failure
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.Epoch != first.Epoch {
				t.Fatalf("failure = %#v; want %s at epoch %d", err, tc.code, first.Epoch)
			}
			second := backend.Descriptor()
			if second.Epoch != first.Epoch+1 || second.ProcessID == first.ProcessID {
				t.Fatalf("provider did not restart with a new epoch: before=%+v after=%+v", first, second)
			}
			if _, err := callBufferLines(context.Background(), backend, file, "after-restart"); err != nil {
				t.Fatalf("call after restart: %v", err)
			}
			if len(trace.snapshot()) != 2 {
				t.Fatalf("startup trace count = %d; want initial plus restart", len(trace.snapshot()))
			}
		})
	}
}

func TestProviderDeathFailsConcurrentCallsAndRestartsLazily(t *testing.T) {
	backend, file, _ := testBackend(t, false)
	first := backend.Descriptor()
	blockDispatch(t, backend)
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			_, err := callBufferLines(context.Background(), backend, file, id)
			results <- err
		}(id)
	}
	time.Sleep(75 * time.Millisecond)
	backend.mu.Lock()
	g := backend.generation
	backend.mu.Unlock()
	if err := g.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-results:
			var failure *provider.Failure
			if !errors.As(err, &failure) || failure.Code != provider.FailureDied || failure.Epoch != first.Epoch {
				t.Fatalf("concurrent call failure = %#v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent call remained blocked after provider death")
		}
	}
	if _, err := callBufferLines(context.Background(), backend, file, "lazy-restart"); err != nil {
		t.Fatalf("call after provider death: %v", err)
	}
	if got := backend.Descriptor().Epoch; got != first.Epoch+1 {
		t.Fatalf("epoch after lazy restart = %d; want %d", got, first.Epoch+1)
	}
}

func TestStderrTailIsBoundedAndIncludedInDeath(t *testing.T) {
	backend, _, _ := testBackend(t, false)
	backend.mu.Lock()
	g := backend.generation
	backend.mu.Unlock()
	var ignored any
	if err := g.nvim.ExecLua(
		`io.stderr:write(string.rep("x", 65536)); io.stderr:flush(); return true`,
		&ignored,
	); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for g.stderr.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := g.stderr.Len(); got == 0 || got > stderrLimit {
		t.Fatalf("stderr retained %d bytes; want 1..%d", got, stderrLimit)
	}
	blockDispatch(t, backend)
	result := make(chan error, 1)
	go func() {
		_, err := callBufferLines(context.Background(), backend, filepath.Join(backend.config.Root, "sample.lua"), "stderr-death")
		result <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := g.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := <-result
	if err == nil || !strings.Contains(err.Error(), "stderr: ") {
		t.Fatalf("death error does not include bounded stderr evidence: %v", err)
	}
}

func TestLaunchFailureIsClassified(t *testing.T) {
	_, err := Open(Config{Root: t.TempDir(), Executable: filepath.Join(t.TempDir(), "missing-nvim")})
	var failure *provider.Failure
	if !errors.As(err, &failure) || failure.Code != provider.FailureLaunch {
		t.Fatalf("launch failure = %#v", err)
	}
}

func TestMalformedCompletionIsClassified(t *testing.T) {
	_, err := decodeCompletion(7, "not-json")
	var failure *provider.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("decodeCompletion error = %T, want *provider.Failure", err)
	}
	if failure.Code != provider.FailureProtocol || failure.Epoch != 7 {
		t.Fatalf("decodeCompletion failure = %#v, want protocol failure at epoch 7", failure)
	}
}

func TestTransactionBatchFailureRestoresEveryUnsavedBuffer(t *testing.T) {
	backend, first, _ := testBackend(t, false)
	second := filepath.Join(filepath.Dir(first), "second.lua")
	firstBytes := []byte("local answer = 42\nreturn answer\n")
	secondBytes := []byte("return 'second'\n")
	if err := os.WriteFile(second, secondBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	files := []map[string]any{
		{"path": first, "before_b64": base64.StdEncoding.EncodeToString(firstBytes), "after_b64": base64.StdEncoding.EncodeToString([]byte("return 1\n")), "before_exists": true, "after_exists": true},
		{"path": second, "before_b64": base64.StdEncoding.EncodeToString(secondBytes), "after_b64": base64.StdEncoding.EncodeToString([]byte("return 2\n")), "before_exists": true, "after_exists": true},
	}
	_, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "transaction-fault"},
		Operation: "huyang_prepare", Arguments: map[string]any{
			"plan_id": "plan_fault", "plan_revision": 1, "files": files, "fail_after": 2,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "injected provider apply failure") {
		t.Fatalf("prepare fault = %v", err)
	}
	for index, path := range []string{first, second} {
		result, callErr := callBufferLines(context.Background(), backend, path, fmt.Sprintf("inspect-%d", index))
		if callErr != nil {
			t.Fatal(callErr)
		}
		want := strings.TrimSuffix(string([][]byte{firstBytes, secondBytes}[index]), "\n")
		if !strings.Contains(fmt.Sprint(result.Value), strings.Split(want, "\n")[0]) {
			t.Fatalf("%s was not restored: %#v", path, result)
		}
		disk, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(disk, [][]byte{firstBytes, secondBytes}[index]) {
			t.Fatalf("disk changed for %s: %q, %v", path, disk, readErr)
		}
	}
}

func TestTransactionProviderDeathDropsStagedViewAndRestartsClean(t *testing.T) {
	backend, file, _ := testBackend(t, false)
	original := []byte("local answer = 42\nreturn answer\n")
	_, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "transaction-death"},
		Operation: "huyang_prepare", Arguments: map[string]any{
			"plan_id": "plan_death", "plan_revision": 1, "exit_provider_after": 1,
			"files": []map[string]any{{
				"path": file, "before_b64": base64.StdEncoding.EncodeToString(original),
				"after_b64":     base64.StdEncoding.EncodeToString([]byte("return 99\n")),
				"before_exists": true, "after_exists": true,
			}},
		},
	})
	var failure *provider.Failure
	if !errors.As(err, &failure) || failure.Code != provider.FailureDied {
		t.Fatalf("provider death = %v", err)
	}
	if _, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "transaction-death-rollback"},
		Operation: "huyang_rollback", Arguments: map[string]any{"plan_id": "plan_death"},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := callBufferLines(context.Background(), backend, file, "transaction-death-inspect")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(result.Value), "local answer = 42") {
		t.Fatalf("restarted provider retained staged bytes: %#v", result)
	}
	if disk, readErr := os.ReadFile(file); readErr != nil || !bytes.Equal(disk, original) {
		t.Fatalf("provider death changed disk: %q, %v", disk, readErr)
	}
}

func TestRootLockRejectsSecondProviderAndReleasesOnClose(t *testing.T) {
	first, _, _ := testBackend(t, false)
	_, err := Open(Config{Root: first.config.Root, Executable: "nvim"})
	var failure *provider.Failure
	if !errors.As(err, &failure) || failure.Code != provider.FailureLaunch ||
		!strings.Contains(failure.Detail, "already locked") {
		t.Fatalf("second provider failure = %#v", err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Config{
		Root: first.config.Root, InitFile: filepath.Join(repo, "tests", "minimal_init.lua"),
		RuntimePath: repo,
	})
	if err != nil {
		t.Fatalf("root lock was not released: %v", err)
	}
	if err := reopened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func blockDispatch(t *testing.T, backend *Backend) {
	t.Helper()
	backend.mu.Lock()
	g := backend.generation
	backend.mu.Unlock()
	var ignored any
	err := g.nvim.ExecLua(`
local lsp = require("agent99.lsp")
lsp.dispatch = function()
    vim.wait(30000)
    return "unexpected completion"
end
return true
`, &ignored)
	if err != nil {
		t.Fatal(err)
	}
}
