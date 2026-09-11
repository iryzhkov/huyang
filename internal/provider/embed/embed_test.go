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

	"github.com/iryzhkov/huyang/internal/provider"
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

const sampleSource = "local answer = 42\nreturn answer\n"

func testBackend(t *testing.T, debug bool, options ...func(*Config)) (*Backend, string, *traceLog) {
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
	if err := os.WriteFile(file, []byte(sampleSource), 0o644); err != nil {
		t.Fatal(err)
	}
	trace := &traceLog{}
	config := Config{
		Root: root, InitFile: filepath.Join(repo, "tests", "minimal_init.lua"),
		RuntimePath: repo, Debug: debug, Trace: trace.add,
	}
	for _, option := range options {
		option(&config)
	}
	backend, err := Open(config)
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

func shortCancelGrace(config *Config) { config.CancelGrace = 300 * time.Millisecond }

// callTree runs a real kernel operation that needs no language server.
func callTree(ctx context.Context, backend *Backend, root, id string) (provider.Result, error) {
	return backend.Call(ctx, provider.Request{
		Context:   provider.RequestContext{RequestID: id},
		Operation: "workspace_tree",
		Arguments: map[string]any{"root": root},
	})
}

// bufferText reads a file through the kernel's buffer view, loading it when
// the generation has not seen it yet.
func bufferText(t *testing.T, backend *Backend, path string) string {
	t.Helper()
	backend.mu.Lock()
	g := backend.generation
	backend.mu.Unlock()
	var text string
	err := g.nvim.ExecLua(`
local bufnr = require("huyang.core").load_buf(...)
local lines = vim.api.nvim_buf_get_lines(bufnr, 0, -1, false)
local text = table.concat(lines, "\n")
if vim.bo[bufnr].eol then text = text .. "\n" end
return text
`, &text, path)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func execLua(t *testing.T, backend *Backend, code string) {
	t.Helper()
	backend.mu.Lock()
	g := backend.generation
	backend.mu.Unlock()
	var ignored any
	if err := g.nvim.ExecLua(code, &ignored); err != nil {
		t.Fatal(err)
	}
}

func prepareFile(path string, before, after []byte) map[string]any {
	return map[string]any{
		"path":          path,
		"before_b64":    base64.StdEncoding.EncodeToString(before),
		"after_b64":     base64.StdEncoding.EncodeToString(after),
		"before_exists": before != nil, "after_exists": after != nil,
	}
}

func TestEmbeddedBootstrapCompletionHealthAndTrace(t *testing.T) {
	backend, _, trace := testBackend(t, true)
	descriptor := backend.Descriptor()
	if descriptor.Backend != "embed" || descriptor.Epoch != 1 || descriptor.ProcessID <= 0 ||
		descriptor.Cancellation != provider.CancellationCooperative {
		t.Fatalf("unexpected descriptor: %+v", descriptor)
	}
	if !slices.Contains(descriptor.Capabilities, provider.CapabilityNavigation) {
		t.Fatalf("handshake capabilities missing navigation: %v", descriptor.Capabilities)
	}
	health := backend.Health(context.Background())
	if health.State != provider.HealthHealthy || health.Epoch != descriptor.Epoch ||
		health.Cancellation != provider.CancellationCooperative {
		t.Fatalf("unexpected health: %+v", health)
	}
	result, err := callTree(context.Background(), backend, backend.config.Root, "read-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Value == nil {
		t.Fatal("completion notification returned no value")
	}
	if result.Health.State != provider.HealthHealthy || result.Health.Epoch != descriptor.Epoch {
		t.Fatalf("completion carried no kernel health: %+v", result.Health)
	}
	if len(result.Touched) != 0 {
		t.Fatalf("read operation reported touched documents: %+v", result.Touched)
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
	execLua(t, backend, `
local lsp = require("huyang.lsp")
lsp.dispatch = function(_, args)
    vim.wait(args.delay)
    return args.value
end
return true
`)

	type answer struct {
		id     string
		result provider.Result
		err    error
	}
	answers := make(chan answer, 2)
	start := func(id string, delay int) {
		go func() {
			result, err := backend.Call(context.Background(), provider.Request{
				Context:   provider.RequestContext{RequestID: id},
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

// yieldingDispatch replaces the kernel dispatcher with one that sleeps
// cooperatively (core.sleep yields through core.await), so a cancel can
// wake it. It returns the arguments' value after args.delay milliseconds.
func yieldingDispatch(t *testing.T, backend *Backend) {
	t.Helper()
	execLua(t, backend, `
local lsp = require("huyang.lsp")
local core = require("huyang.core")
lsp.dispatch = function(_, args)
    core.sleep(args.delay)
    return { value = args.value, transaction_id = require("huyang.rpc").transaction_id() }
end
return true
`)
}

func TestCooperativeCancelLeavesOtherCallsRunning(t *testing.T) {
	backend, _, trace := testBackend(t, false)
	first := backend.Descriptor()
	yieldingDispatch(t, backend)

	type answer struct {
		result provider.Result
		err    error
	}
	slowAnswer := make(chan answer, 1)
	slowCtx, cancelSlow := context.WithCancel(context.Background())
	defer cancelSlow()
	go func() {
		result, err := backend.Call(slowCtx, provider.Request{
			Context:   provider.RequestContext{RequestID: "slow"},
			Operation: "fixture", Arguments: map[string]any{"delay": 20000, "value": "slow"},
		})
		slowAnswer <- answer{result, err}
	}()
	otherAnswer := make(chan answer, 1)
	go func() {
		result, err := backend.Call(context.Background(), provider.Request{
			Context:   provider.RequestContext{RequestID: "other", TransactionID: "plan_x"},
			Operation: "fixture", Arguments: map[string]any{"delay": 400, "value": "other"},
		})
		otherAnswer <- answer{result, err}
	}()
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	cancelSlow()

	slow := <-slowAnswer
	var failure *provider.Failure
	if !errors.As(slow.err, &failure) || failure.Code != provider.FailureCancelled || failure.Epoch != first.Epoch {
		t.Fatalf("cancelled call failure = %#v; want %s at epoch %d", slow.err, provider.FailureCancelled, first.Epoch)
	}
	if !strings.Contains(failure.Detail, "acknowledged by the kernel") {
		t.Fatalf("cancel was not acknowledged cooperatively: %s", failure.Detail)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cooperative cancel took %s; the kernel should wake the parked request at once", elapsed)
	}
	other := <-otherAnswer
	if other.err != nil {
		t.Fatalf("concurrent call failed after cancel of another: %v", other.err)
	}
	value, _ := other.result.Value.(map[string]any)
	if value["value"] != "other" || value["transaction_id"] != "plan_x" {
		t.Fatalf("concurrent call result = %#v; want value other in transaction plan_x", other.result.Value)
	}
	if got := backend.Descriptor(); got.Epoch != first.Epoch || got.ProcessID != first.ProcessID {
		t.Fatalf("cooperative cancel replaced the generation: before=%+v after=%+v", first, got)
	}
	if len(trace.snapshot()) != 1 {
		t.Fatalf("startup trace count = %d; want 1 (no restart)", len(trace.snapshot()))
	}
	var pending int
	backend.mu.Lock()
	g := backend.generation
	backend.mu.Unlock()
	if err := g.nvim.ExecLua(`return require("huyang.rpc").pending_count()`, &pending); err != nil || pending != 0 {
		t.Fatalf("kernel pending requests = %d, %v; want 0", pending, err)
	}
}

func TestCancelFallbackReplacesUnresponsiveGeneration(t *testing.T) {
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
			backend, _, trace := testBackend(t, false, shortCancelGrace)
			first := backend.Descriptor()
			blockDispatch(t, backend)
			ctx, cancel := tc.context()
			if tc.name == "cancel" {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			defer cancel()
			_, err := callTree(ctx, backend, backend.config.Root, "blocked")
			var failure *provider.Failure
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.Epoch != first.Epoch {
				t.Fatalf("failure = %#v; want %s at epoch %d", err, tc.code, first.Epoch)
			}
			if !strings.Contains(failure.Detail, "did not acknowledge cancellation") {
				t.Fatalf("fallback detail = %q", failure.Detail)
			}
			second := backend.Descriptor()
			if second.Epoch != first.Epoch+1 || second.ProcessID == first.ProcessID {
				t.Fatalf("provider did not restart with a new epoch: before=%+v after=%+v", first, second)
			}
			if _, err := callTree(context.Background(), backend, backend.config.Root, "after-restart"); err != nil {
				t.Fatalf("call after restart: %v", err)
			}
			if len(trace.snapshot()) != 2 {
				t.Fatalf("startup trace count = %d; want initial plus restart", len(trace.snapshot()))
			}
		})
	}
}

func TestProviderDeathFailsConcurrentCallsAndRestartsLazily(t *testing.T) {
	backend, _, _ := testBackend(t, false)
	first := backend.Descriptor()
	blockDispatch(t, backend)
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			_, err := callTree(context.Background(), backend, backend.config.Root, id)
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
	if _, err := callTree(context.Background(), backend, backend.config.Root, "lazy-restart"); err != nil {
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
	execLua(t, backend, `io.stderr:write(string.rep("x", 65536)); io.stderr:flush(); return true`)
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
		_, err := callTree(context.Background(), backend, backend.config.Root, "stderr-death")
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
	for name, payload := range map[string]map[string]any{
		"nil":               nil,
		"failed-no-error":   {"ok": false},
		"unencodable-value": {"ok": true, "result": map[string]any{"bad": make(chan int)}},
	} {
		_, err := decodeCompletion(7, payload)
		var failure *provider.Failure
		if !errors.As(err, &failure) {
			t.Fatalf("%s: decodeCompletion error = %T, want *provider.Failure", name, err)
		}
		if failure.Code != provider.FailureProtocol || failure.Epoch != 7 {
			t.Fatalf("%s: decodeCompletion failure = %#v, want protocol failure at epoch 7", name, failure)
		}
	}
}

func TestStructuredErrorBecomesProviderError(t *testing.T) {
	result, err := decodeCompletion(3, map[string]any{
		"ok":      false,
		"error":   map[string]any{"code": "workspace_busy", "message": "workspace_busy: plan_a holds the lease", "detail": "plan_a"},
		"touched": []any{map[string]any{"uri": "file:///a.lua", "path": "/a.lua", "changedtick": int64(4), "dirty": true, "exists": true}},
		"health":  map[string]any{"state": "healthy", "lsp_clients": int64(1), "transaction": "plan_a"},
	})
	var operation *provider.ProviderError
	if !errors.As(err, &operation) || operation.Code != "workspace_busy" || operation.Epoch != 3 ||
		err.Error() != "workspace_busy: plan_a holds the lease" || operation.Detail != "plan_a" {
		t.Fatalf("decoded error = %#v", err)
	}
	if provider.ErrorCode(err) != "workspace_busy" {
		t.Fatalf("ErrorCode = %q", provider.ErrorCode(err))
	}
	if len(result.Touched) != 1 || result.Touched[0].ChangedTick != 4 || !result.Touched[0].Dirty {
		t.Fatalf("touched = %+v", result.Touched)
	}
	if result.Health.State != provider.HealthHealthy || !strings.Contains(result.Health.Detail, "plan_a") {
		t.Fatalf("health = %+v", result.Health)
	}
}

func TestHandshakeRangeAndMethodsAreChecked(t *testing.T) {
	good := map[string]any{
		"protocol_version": int64(2), "completion_method": completionMethod, "cancellation": "cooperative",
		"methods":      []any{"handshake", "start_notify", "cancel"},
		"capabilities": []any{"execute", "navigation", "unknown_extra"},
	}
	capabilities, failure := checkHandshake(1, good)
	if failure != nil || len(capabilities) != 2 {
		t.Fatalf("good handshake: capabilities=%v failure=%v", capabilities, failure)
	}
	for name, mutate := range map[string]func(map[string]any){
		"old-protocol":   func(h map[string]any) { h["protocol_version"] = int64(1) },
		"new-protocol":   func(h map[string]any) { h["protocol_version"] = int64(3) },
		"old-method":     func(h map[string]any) { h["completion_method"] = "agent99/result" },
		"no-cancel":      func(h map[string]any) { h["methods"] = []any{"start_notify"} },
		"restart-cancel": func(h map[string]any) { h["cancellation"] = "provider_restart" },
		"no-capability":  func(h map[string]any) { h["capabilities"] = []any{"unknown"} },
	} {
		handshake := map[string]any{}
		for key, value := range good {
			handshake[key] = value
		}
		mutate(handshake)
		if _, failure := checkHandshake(1, handshake); failure == nil || failure.Code != provider.FailureIncompatible {
			t.Fatalf("%s: failure = %v; want %s", name, failure, provider.FailureIncompatible)
		}
	}
}

func TestAPILevelCheckExplainsOldNeovim(t *testing.T) {
	old := map[string]any{"version": map[string]any{"major": int64(0), "minor": int64(10), "api_level": int64(12)}}
	failure := checkAPILevel(1, old)
	if failure == nil || failure.Code != provider.FailureIncompatible || !strings.Contains(failure.Detail, "0.10") ||
		!strings.Contains(failure.Detail, "0.11") {
		t.Fatalf("old Neovim failure = %v", failure)
	}
	newer := map[string]any{"version": map[string]any{"major": int64(0), "minor": int64(14), "api_level": int64(16)}}
	if failure := checkAPILevel(1, newer); failure != nil {
		t.Fatalf("newer Neovim rejected: %v", failure)
	}
	if failure := checkAPILevel(1, "not-a-map"); failure == nil || failure.Code != provider.FailureProtocol {
		t.Fatalf("malformed metadata failure = %v", failure)
	}
}

func TestOperationErrorCarriesKernelCode(t *testing.T) {
	backend, file, _ := testBackend(t, false)
	_, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "lease"},
		Operation: "huyang_prepare", Arguments: map[string]any{
			"plan_id": "plan_lease", "plan_revision": 1,
			"files": []map[string]any{prepareFile(file, []byte(sampleSource), []byte("return 1\n"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "other-rollback"},
		Operation: "huyang_rollback", Arguments: map[string]any{"plan_id": "plan_other"},
	})
	if provider.ErrorCode(err) != "workspace_busy" || !strings.Contains(err.Error(), "plan_lease") {
		t.Fatalf("lease conflict error = %#v", err)
	}
	_, err = backend.Call(context.Background(), provider.Request{
		Context: provider.RequestContext{RequestID: "unknown"}, Operation: "no_such_tool",
	})
	if provider.ErrorCode(err) != "lua_error" || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("unknown tool error = %#v", err)
	}
	if _, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "rollback"},
		Operation: "huyang_rollback", Arguments: map[string]any{"plan_id": "plan_lease"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionPrepareReportsTouchedSnapshots(t *testing.T) {
	backend, file, _ := testBackend(t, false)
	after := []byte("return 1\n")
	result, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "prepare-touched", TransactionID: "plan_touch"},
		Operation: "huyang_prepare", Arguments: map[string]any{
			"plan_id": "plan_touch", "plan_revision": 1,
			"files": []map[string]any{prepareFile(file, []byte(sampleSource), after)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Touched) != 1 {
		t.Fatalf("touched = %+v; want the staged file", result.Touched)
	}
	touched := result.Touched[0]
	if touched.Path != file || !touched.Dirty || !touched.Exists || touched.ChangedTick == 0 ||
		touched.ContentSHA256 == "" || touched.DiskFingerprint == "" || !strings.HasPrefix(touched.URI, "file://") {
		t.Fatalf("touched snapshot = %+v", touched)
	}
	if !strings.Contains(result.Health.Detail, "plan_touch") {
		t.Fatalf("completion health does not name the lease: %+v", result.Health)
	}
	result, err = backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "rollback-touched", TransactionID: "plan_touch"},
		Operation: "huyang_rollback", Arguments: map[string]any{"plan_id": "plan_touch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Touched) != 1 || result.Touched[0].Dirty {
		t.Fatalf("rollback touched = %+v; want the restored, clean file", result.Touched)
	}
}

func TestDiagnosticEvidenceIsCarriedStructurally(t *testing.T) {
	backend, file, _ := testBackend(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := backend.Call(ctx, provider.Request{
		Context:   provider.RequestContext{RequestID: "evidence", TransactionID: "plan_evidence"},
		Operation: "huyang_diagnostic_evidence",
		Arguments: map[string]any{"files": []string{file}, "revision": "rev1", "transaction_id": "plan_evidence", "wait_ms": 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) == 0 {
		t.Fatalf("completion carried no evidence batches: %+v", result)
	}
	batch := result.Evidence[0]
	if batch.Kind == "" || batch.Document != file || batch.TransactionID != "plan_evidence" || batch.DocumentRevision != "rev1" {
		t.Fatalf("evidence batch = %+v", batch)
	}
}

func TestTransactionPrepareAcceptsMissingFileWithEmptyPreimage(t *testing.T) {
	backend, first, _ := testBackend(t, false)
	created := filepath.Join(filepath.Dir(first), "created.lua")
	_, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "transaction-create"},
		Operation: "huyang_prepare",
		Arguments: map[string]any{
			"plan_id": "plan_create", "plan_revision": 1,
			"files": []map[string]any{prepareFile(created, nil, []byte("return created\n"))},
		},
	})
	if err != nil {
		t.Fatalf("prepare missing file: %v", err)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("prepare wrote missing file to disk: %v", err)
	}
	if _, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "transaction-create-rollback"},
		Operation: "huyang_rollback", Arguments: map[string]any{"plan_id": "plan_create"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionBatchFailureRestoresEveryUnsavedBuffer(t *testing.T) {
	t.Setenv("HUYANG_TEST_FAULTS", "1")
	backend, first, _ := testBackend(t, false)
	second := filepath.Join(filepath.Dir(first), "second.lua")
	firstBytes := []byte(sampleSource)
	secondBytes := []byte("return 'second'\n")
	if err := os.WriteFile(second, secondBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	files := []map[string]any{
		prepareFile(first, firstBytes, []byte("return 1\n")),
		prepareFile(second, secondBytes, []byte("return 2\n")),
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
		want := [][]byte{firstBytes, secondBytes}[index]
		if got := bufferText(t, backend, path); got != string(want) {
			t.Fatalf("%s was not restored: %q", path, got)
		}
		disk, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(disk, want) {
			t.Fatalf("disk changed for %s: %q, %v", path, disk, readErr)
		}
	}
}

func TestFaultInjectionIsIgnoredWithoutTestFaults(t *testing.T) {
	backend, file, _ := testBackend(t, false)
	_, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "no-fault"},
		Operation: "huyang_prepare", Arguments: map[string]any{
			"plan_id": "plan_nofault", "plan_revision": 1, "fail_after": 1, "exit_provider_after": 1,
			"files": []map[string]any{prepareFile(file, []byte(sampleSource), []byte("return 1\n"))},
		},
	})
	if err != nil {
		t.Fatalf("fault hooks fired without HUYANG_TEST_FAULTS: %v", err)
	}
	if _, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "no-fault-rollback"},
		Operation: "huyang_rollback", Arguments: map[string]any{"plan_id": "plan_nofault"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionProviderDeathDropsStagedViewAndRestartsClean(t *testing.T) {
	t.Setenv("HUYANG_TEST_FAULTS", "1")
	backend, file, _ := testBackend(t, false)
	original := []byte(sampleSource)
	_, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "transaction-death"},
		Operation: "huyang_prepare", Arguments: map[string]any{
			"plan_id": "plan_death", "plan_revision": 1, "exit_provider_after": 1,
			"files": []map[string]any{prepareFile(file, original, []byte("return 99\n"))},
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
	if got := bufferText(t, backend, file); got != string(original) {
		t.Fatalf("restarted provider retained staged bytes: %q", got)
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

func TestWorkspaceResyncOperationNameMatchesBridge(t *testing.T) {
	backend, _, _ := testBackend(t, false)
	for _, operation := range []string{"workspace_resync", "huyang_workspace_resync"} {
		result, err := backend.Call(context.Background(), provider.Request{
			Context:   provider.RequestContext{RequestID: "resync-" + operation},
			Operation: operation, Arguments: map[string]any{"root": backend.config.Root},
		})
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		if value, ok := result.Value.(map[string]any); !ok || value["resynced"] == nil {
			t.Fatalf("%s result = %#v", operation, result.Value)
		}
	}
}

// blockDispatch replaces the dispatcher with one that never yields, so a
// cancel cannot be acknowledged: the fallback path has to replace the
// generation.
func blockDispatch(t *testing.T, backend *Backend) {
	t.Helper()
	execLua(t, backend, fmt.Sprintf(`
local lsp = require("huyang.lsp")
lsp.dispatch = function()
    vim.wait(%d)
    return "unexpected completion"
end
return true
`, 30000))
}
