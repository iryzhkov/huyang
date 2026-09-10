// Package embedspike contains disposable compatibility tests for the S03
// embedded-Neovim transport decision. It intentionally has no production files.
package embedspike

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neovim/go-client/nvim"
)

const (
	stderrLimit = 32 << 10
	callTimeout = 5 * time.Second
)

type tailWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > stderrLimit {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-stderrLimit:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

func (w *tailWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.buf)
}

type embedded struct {
	nvim   *nvim.Nvim
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailWriter
	done   chan error
}

func nvimExecutable(t testing.TB) string {
	t.Helper()
	if path := os.Getenv("HUYANG_NVIM"); path != "" {
		return path
	}
	path, err := exec.LookPath("nvim")
	if err != nil {
		t.Fatal("nvim is required for the embedded-provider spike")
	}
	return path
}

func startEmbedded(t testing.TB) *embedded {
	t.Helper()
	cmd := exec.Command(nvimExecutable(t), "--clean", "--embed", "--headless", "-u", "NONE", "-n")
	cmd.Env = append(os.Environ(), "NVIM_APPNAME=huyang-embed-spike")
	stderr := &tailWriter{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	client, err := nvim.New(stdout, stdin, stdin, t.Logf)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	e := &embedded{nvim: client, cmd: cmd, stdin: stdin, stderr: stderr, done: make(chan error, 1)}
	go func() {
		serveErr := client.Serve()
		waitErr := cmd.Wait()
		e.done <- errors.Join(serveErr, waitErr)
		close(e.done)
	}()
	return e
}

func (e *embedded) close(t testing.TB) {
	t.Helper()
	select {
	case <-e.done:
		_ = e.nvim.Close()
		return
	default:
	}
	_ = e.nvim.Command("qa!")
	select {
	case <-e.done:
	case <-time.After(callTimeout):
		_ = e.cmd.Process.Kill()
		<-e.done
		t.Error("embedded Neovim did not exit after qa!")
	}
	_ = e.nvim.Close()
}

func (e *embedded) kill() {
	_ = e.cmd.Process.Kill()
}

func channelID(t testing.TB, v *nvim.Nvim) int {
	t.Helper()
	info, err := v.APIInfo()
	if err != nil {
		t.Fatal(err)
	}
	id, ok := info[0].(int64)
	if !ok {
		t.Fatalf("unexpected channel ID type %T", info[0])
	}
	return int(id)
}

func TestEmbeddedHeadlessRPC(t *testing.T) {
	e := startEmbedded(t)
	defer e.close(t)

	var entered int
	if err := e.nvim.Eval("v:vim_did_enter", &entered); err != nil {
		t.Fatal(err)
	}
	if entered != 1 {
		t.Fatalf("--embed --headless did not reach VimEnter: %d", entered)
	}
	if err := e.nvim.SetClientInfo("huyang-spike", nvim.ClientVersion{Major: 0, Minor: 1}, nvim.EmbedderClientType, map[string]*nvim.ClientMethod{}, nvim.ClientAttributes{}); err != nil {
		t.Fatal(err)
	}

	requests := make(chan string, 1)
	if err := e.nvim.RegisterHandler("huyang/request", func(value string) (string, error) {
		requests <- value
		return "reply:" + value, nil
	}); err != nil {
		t.Fatal(err)
	}
	var reply string
	script := `local channel = ...
return vim.rpcrequest(channel, "huyang/request", "ping")`
	if err := e.nvim.ExecLua(script, &reply, channelID(t, e.nvim)); err != nil {
		t.Fatal(err)
	}
	if reply != "reply:ping" || <-requests != "ping" {
		t.Fatalf("unexpected inbound request result %q", reply)
	}

	notifications := make(chan string, 1)
	if err := e.nvim.RegisterHandler("huyang/notice", func(value string) {
		notifications <- value
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.nvim.ExecLua(`vim.rpcnotify(..., "huyang/notice", "ready")`, nil, channelID(t, e.nvim)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-notifications:
		if got != "ready" {
			t.Fatalf("unexpected notification %q", got)
		}
	case <-time.After(callTimeout):
		t.Fatal("unsolicited notification was not delivered")
	}
}

func TestCompletionNotificationsMayArriveOutOfOrder(t *testing.T) {
	e := startEmbedded(t)
	defer e.close(t)

	completed := make(chan string, 2)
	if err := e.nvim.RegisterHandler("huyang/result", func(id string) {
		completed <- id
	}); err != nil {
		t.Fatal(err)
	}
	script := `local channel, id, delay = ...
vim.defer_fn(function() vim.rpcnotify(channel, "huyang/result", id) end, delay)
return id`
	var ignored string
	if err := e.nvim.ExecLua(script, &ignored, channelID(t, e.nvim), "slow", 80); err != nil {
		t.Fatal(err)
	}
	if err := e.nvim.ExecLua(script, &ignored, channelID(t, e.nvim), "fast", 5); err != nil {
		t.Fatal(err)
	}
	order := make([]string, 0, 2)
	for range 2 {
		select {
		case id := <-completed:
			order = append(order, id)
		case <-time.After(callTimeout):
			t.Fatal("timed out waiting for completion notifications")
		}
	}
	if order[0] != "fast" || order[1] != "slow" {
		t.Fatalf("completion order = %q; want [fast slow]", order)
	}
}

func TestCancellationTerminatesProviderAndUnblocksCall(t *testing.T) {
	e := startEmbedded(t)
	defer e.close(t)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		var ignored any
		result <- e.nvim.ExecLua(`vim.wait(10000); return true`, &ignored)
	}()
	go func() {
		<-ctx.Done()
		e.kill()
	}()
	cancel()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("pending call unexpectedly succeeded after cancellation")
		}
	case <-time.After(callTimeout):
		t.Fatal("pending call did not unblock after provider cancellation")
	}
	select {
	case <-e.done:
	case <-time.After(callTimeout):
		t.Fatal("provider did not die after cancellation")
	}
}

func TestProviderDeathUnblocksConcurrentCalls(t *testing.T) {
	e := startEmbedded(t)
	defer e.close(t)

	results := make(chan error, 2)
	for range 2 {
		go func() {
			var ignored any
			results <- e.nvim.ExecLua(`vim.wait(10000); return true`, &ignored)
		}()
	}
	time.Sleep(25 * time.Millisecond)
	e.kill()
	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("call unexpectedly succeeded after provider death")
			}
		case <-time.After(callTimeout):
			t.Fatal("call remained blocked after provider death")
		}
	}
}

func TestStderrCaptureIsBounded(t *testing.T) {
	e := startEmbedded(t)
	defer e.close(t)

	var ignored any
	if err := e.nvim.ExecLua(`io.stderr:write(string.rep("x", 65536)); io.stderr:flush(); return true`, &ignored); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for e.stderr.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.stderr.Len() == 0 {
		t.Fatal("embedded stderr was not captured")
	}
	if e.stderr.Len() > stderrLimit {
		t.Fatalf("stderr capture grew to %d bytes; limit %d", e.stderr.Len(), stderrLimit)
	}
}

func TestSupportedVersion(t *testing.T) {
	e := startEmbedded(t)
	defer e.close(t)

	var version map[string]any
	if err := e.nvim.ExecLua(`return vim.version()`, &version); err != nil {
		t.Fatal(err)
	}
	major := int(version["major"].(int64))
	minor := int(version["minor"].(int64))
	if major != 0 || (minor != 11 && minor != 12) {
		t.Fatalf("Neovim %d.%d is outside the supported spike range 0.11-0.12", major, minor)
	}
	t.Logf("exercised Neovim %d.%d.%d via %s", major, minor, int(version["patch"].(int64)), nvimExecutable(t))
}

type measurements struct {
	Version            string  `json:"version"`
	Iterations         int     `json:"iterations"`
	EmbedStartupMS     float64 `json:"embed_startup_ms_median"`
	SocketStartupMS    float64 `json:"socket_startup_ms_median"`
	EmbedWarmCallMS    float64 `json:"embed_warm_call_ms_median"`
	SocketWarmCallMS   float64 `json:"socket_warm_call_ms_median"`
	EmbedRSSKiB        int     `json:"embed_rss_kib"`
	SocketRSSKiB       int     `json:"socket_rss_kib"`
	StderrCaptureBytes int     `json:"stderr_capture_limit_bytes"`
}

func median(values []time.Duration) float64 {
	slices := append([]time.Duration(nil), values...)
	for i := 1; i < len(slices); i++ {
		for j := i; j > 0 && slices[j] < slices[j-1]; j-- {
			slices[j], slices[j-1] = slices[j-1], slices[j]
		}
	}
	return float64(slices[len(slices)/2].Microseconds()) / 1000
}

func rssKiB(pid int) int {
	if runtime.GOOS != "linux" {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			value, _ := strconv.Atoi(fields[1])
			return value
		}
	}
	return 0
}

func startSocket(t testing.TB) (*exec.Cmd, string, *tailWriter, time.Duration) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "nvim.sock")
	stderr := &tailWriter{}
	cmd := exec.Command(nvimExecutable(t), "--clean", "--headless", "--listen", socket, "-u", "NONE", "-n")
	cmd.Env = append(os.Environ(), "NVIM_APPNAME=huyang-socket-spike")
	cmd.Stdout, cmd.Stderr = stderr, stderr
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(callTimeout)
	for {
		probe := exec.Command(nvimExecutable(t), "--server", socket, "--remote-expr", "v:vim_did_enter")
		if output, err := probe.Output(); err == nil && bytes.Equal(bytes.TrimSpace(output), []byte("1")) {
			return cmd, socket, stderr, time.Since(started)
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("socket Neovim did not start: %s", stderr.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func stopSocket(cmd *exec.Cmd, socket string) {
	quit := exec.Command(cmd.Path, "--server", socket, "--remote-expr", "execute('qa!')")
	_ = quit.Run()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(callTimeout):
		_ = cmd.Process.Kill()
		<-done
	}
}

func TestMeasureTransports(t *testing.T) {
	if os.Getenv("HUYANG_SPIKE_MEASURE") != "1" {
		t.Skip("set HUYANG_SPIKE_MEASURE=1 for the bounded S03 measurement")
	}
	const iterations = 7
	embedStarts := make([]time.Duration, 0, iterations)
	socketStarts := make([]time.Duration, 0, iterations)
	for range iterations {
		started := time.Now()
		e := startEmbedded(t)
		var entered int
		if err := e.nvim.Eval("v:vim_did_enter", &entered); err != nil {
			t.Fatal(err)
		}
		embedStarts = append(embedStarts, time.Since(started))
		e.close(t)

		cmd, socket, _, elapsed := startSocket(t)
		socketStarts = append(socketStarts, elapsed)
		stopSocket(cmd, socket)
	}

	e := startEmbedded(t)
	defer e.close(t)
	cmd, socket, _, _ := startSocket(t)
	defer stopSocket(cmd, socket)

	const calls = 31
	embedCalls := make([]time.Duration, 0, calls)
	socketCalls := make([]time.Duration, 0, calls)
	for range calls {
		started := time.Now()
		var value int
		if err := e.nvim.Eval("1+1", &value); err != nil || value != 2 {
			t.Fatalf("embedded warm call: value=%d err=%v", value, err)
		}
		embedCalls = append(embedCalls, time.Since(started))

		started = time.Now()
		call := exec.Command(nvimExecutable(t), "--server", socket, "--remote-expr", "1+1")
		if output, err := call.Output(); err != nil || !bytes.Equal(bytes.TrimSpace(output), []byte("2")) {
			t.Fatalf("socket warm call: output=%q err=%v", output, err)
		}
		socketCalls = append(socketCalls, time.Since(started))
	}

	var version string
	if err := e.nvim.ExecLua(`return tostring(vim.version())`, &version); err != nil {
		t.Fatal(err)
	}
	result := measurements{
		Version:            version,
		Iterations:         iterations,
		EmbedStartupMS:     median(embedStarts),
		SocketStartupMS:    median(socketStarts),
		EmbedWarmCallMS:    median(embedCalls),
		SocketWarmCallMS:   median(socketCalls),
		EmbedRSSKiB:        rssKiB(e.cmd.Process.Pid),
		SocketRSSKiB:       rssKiB(cmd.Process.Pid),
		StderrCaptureBytes: stderrLimit,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}
