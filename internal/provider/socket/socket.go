// Package socket implements the reference Neovim --listen provider.
package socket

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"agent99/internal/provider"
)

const (
	startTimeout      = 20 * time.Second
	stopTimeout       = 3 * time.Second
	callTimeout       = 60 * time.Second
	pollInterval      = 150 * time.Millisecond
	remoteExprTimeout = 20 * time.Second
)

var (
	instanceSeq  atomic.Int64
	toolTimeouts = map[string]time.Duration{
		"install_language": 15 * time.Minute,
		"install_debugger": 15 * time.Minute,
		"check_project":    10 * time.Minute,
		"run_tests":        15 * time.Minute,
		"debug_launch":     5 * time.Minute,
		"debug_attach":     5 * time.Minute,
		"debug_continue":   5 * time.Minute,
		"debug_step":       5 * time.Minute,
		"debug_wait":       5 * time.Minute,
		"debug_stop":       5 * time.Minute,
	}
)

var debugStopLua = strings.Join([]string{
	`(function() pcall(function()`,
	`require("agent99.dap").shutdown_sync() end) return "" end)()`,
}, " ")

var saveLua = strings.Join([]string{
	`(function() local ok, r = pcall(function()`,
	`return require("agent99.lsp").save_all() end)`,
	`if not ok then return tostring(r) end`,
	`return table.concat(r, "; ") end)()`,
}, " ")

// Config controls creation of an owned reference provider.
type Config struct {
	Root       string
	InitFile   string
	Debug      bool
	Executable string
}

// Backend is the socket/start-poll reference implementation.
type Backend struct {
	descriptor provider.Descriptor
	command    *exec.Cmd
	stderr     *tailBuffer
	done       chan struct{}
	owned      bool
	debug      bool
	closeOnce  sync.Once
	closeErr   error
}

var _ provider.Provider = (*Backend)(nil)

// Open starts an owned Neovim reference provider.
func Open(config Config) (*Backend, error) {
	executable := config.Executable
	if executable == "" {
		executable = "nvim"
	}
	dir, err := runtimeDir()
	if err != nil {
		return nil, err
	}
	endpoint := filepath.Join(dir, fmt.Sprintf("%s-%d-%d.sock",
		rootPrefix(config.Root), instanceSeq.Add(1), os.Getpid()))
	_ = os.Remove(endpoint)

	args := []string{"--headless", "--listen", endpoint,
		"--cmd", "set noswapfile shadafile=NONE"}
	if config.InitFile != "" {
		args = append(args, "--clean", "-u", config.InitFile)
	}
	command := exec.Command(executable, args...)
	command.Dir = config.Root
	tail := &tailBuffer{}
	command.Stderr = tail
	command.Stdout = tail
	command.Stdin = nil
	setDeathSignal(command)
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("starting nvim: %v", err)
	}

	backend := &Backend{
		descriptor: descriptor(config.Root, endpoint, command.Process.Pid),
		command:    command,
		stderr:     tail,
		done:       make(chan struct{}),
		owned:      true,
		debug:      config.Debug,
	}
	go func() {
		_ = command.Wait()
		_ = os.Remove(endpoint)
		close(backend.done)
	}()

	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-backend.done:
			_ = os.Remove(endpoint)
			return nil, fmt.Errorf("nvim exited during startup: %s", tail.String())
		default:
		}
		if Alive(endpoint) {
			return backend, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = command.Process.Kill()
	<-backend.done
	_ = os.Remove(endpoint)
	detail := tail.String()
	if detail == "" {
		detail = "the agent99 plugin never answered on the socket (is it on the runtimepath?)"
	}
	return nil, fmt.Errorf("nvim did not come up within %s: %s", startTimeout, detail)
}

// Attach represents an existing editor-owned socket through the same provider seam.
func Attach(root, endpoint string) *Backend {
	return &Backend{
		descriptor: descriptor(root, endpoint, 0),
		done:       make(chan struct{}),
	}
}

func descriptor(root, endpoint string, pid int) provider.Descriptor {
	sum := sha256.Sum256([]byte("socket\x00" + root))
	return provider.Descriptor{
		ID:        provider.ID("socket_" + hex.EncodeToString(sum[:8])),
		Backend:   "socket",
		Root:      root,
		Endpoint:  endpoint,
		ProcessID:    pid,
		Cancellation: provider.CancellationUnsupported,
		Capabilities: []provider.Capability{
			provider.CapabilityExecute,
			provider.CapabilityNavigation,
			provider.CapabilityRename,
			provider.CapabilityDiagnostics,
			provider.CapabilityCodeActions,
		},
	}
}

// Descriptor returns stable identity plus compatibility process metadata.
func (b *Backend) Descriptor() provider.Descriptor {
	out := b.descriptor
	out.Capabilities = append([]provider.Capability(nil), out.Capabilities...)
	return out
}

// Health reports whether the legacy RPC entry point answers.
func (b *Backend) Health(context.Context) provider.Health {
	health := provider.Health{
		State:        provider.HealthFailed,
		ObservedAt:   time.Now(),
		Cancellation: provider.CancellationUnsupported,
	}
	select {
	case <-b.done:
		health.State = provider.HealthClosed
		health.Detail = b.stderrText()
		return health
	default:
	}
	if Alive(b.descriptor.Endpoint) {
		health.State = provider.HealthHealthy
		return health
	}
	health.Detail = "Neovim socket did not answer"
	return health
}

// Call runs one semantic operation through the legacy start/poll protocol.
func (b *Backend) Call(ctx context.Context, request provider.Request) (provider.Result, error) {
	if err := ctx.Err(); err != nil {
		return provider.Result{}, err
	}
	endpoint := b.descriptor.Endpoint
	if endpoint == "" {
		return provider.Result{}, errors.New("no Neovim to talk to: call open_workspace(root) first, " +
			"or launch the bridge with $AGENT99_NVIM (or $NVIM) pointing at a running Neovim")
	}
	payload, err := json.Marshal(map[string]any{
		"tool": request.Operation,
		"args": request.Arguments,
	})
	if err != nil {
		return provider.Result{}, err
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	id, err := remoteExpr(endpoint, fmt.Sprintf("v:lua.Agent99RpcStart('%s')", encoded))
	if err != nil {
		return provider.Result{}, err
	}
	id = strings.TrimSpace(id)

	timeout := callTimeout
	if configured, ok := toolTimeouts[request.Operation]; ok {
		timeout = configured
	}
	deadline := time.Now().Add(timeout)
	if !request.Context.Deadline.IsZero() && request.Context.Deadline.Before(deadline) {
		deadline = request.Context.Deadline
	}
	for time.Now().Before(deadline) {
		out, err := remoteExpr(endpoint, fmt.Sprintf("v:lua.Agent99RpcPoll('%s')", id))
		if err != nil {
			return provider.Result{}, err
		}
		var response struct {
			Pending bool   `json:"pending"`
			OK      bool   `json:"ok"`
			Result  any    `json:"result"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal([]byte(out), &response); err != nil {
			return provider.Result{}, fmt.Errorf("unparseable response from nvim: %v", err)
		}
		if response.Pending {
			time.Sleep(pollInterval)
			continue
		}
		if !response.OK {
			return provider.Result{}, errors.New(response.Error)
		}
		return provider.Result{Value: response.Result}, nil
	}
	return provider.Result{}, fmt.Errorf("timed out after %s waiting for the Neovim tool result", timeout)
}

// Save writes every modified named buffer.
func (b *Backend) Save(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.descriptor.Endpoint == "" {
		return nil
	}
	out, err := remoteExpr(b.descriptor.Endpoint, "luaeval('"+saveLua+"')")
	if err != nil {
		return err
	}
	if message := strings.TrimSpace(out); message != "" {
		return fmt.Errorf("saving buffers: %s", message)
	}
	return nil
}

// Close gracefully stops an owned provider and then forces it after a bound.
func (b *Backend) Close(context.Context) error {
	if !b.owned {
		return nil
	}
	b.closeOnce.Do(func() {
		endpoint := b.descriptor.Endpoint
		defer os.Remove(endpoint)
		select {
		case <-b.done:
			return
		default:
		}
		asked := make(chan struct{})
		go func() {
			defer close(asked)
			if b.debug {
				_, _ = remoteExpr(endpoint, "luaeval('"+debugStopLua+"')")
			}
			_, _ = remoteExpr(endpoint, "execute('qa!')")
		}()
		kill := func() {
			if b.command != nil && b.command.Process != nil {
				_ = b.command.Process.Kill()
			}
			<-b.done
		}
		select {
		case <-b.done:
		case <-asked:
			select {
			case <-b.done:
			case <-time.After(stopTimeout):
				kill()
			}
		case <-time.After(remoteExprTimeout):
			kill()
		}
	})
	return b.closeErr
}

// Done closes when an owned provider exits.
func (b *Backend) Done() <-chan struct{} { return b.done }

// Alive reports whether the plugin RPC entry point answers at an endpoint.
func Alive(endpoint string) bool {
	if _, err := os.Stat(endpoint); err != nil {
		return false
	}
	out, err := remoteExpr(endpoint, "luaeval('type(Agent99RpcStart)')")
	return err == nil && strings.TrimSpace(out) == "function"
}

// FindForeign reports an answering provider for root owned by another bridge.
func FindForeign(root string) (string, int) {
	dir, err := runtimeDir()
	if err != nil {
		return "", 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0
	}
	prefix := rootPrefix(root) + "-"
	self := os.Getpid()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".sock") {
			continue
		}
		pid, ok := ownerPID(name)
		if !ok || pid == self || !processAlive(pid) {
			continue
		}
		endpoint := filepath.Join(dir, name)
		if Alive(endpoint) {
			return endpoint, pid
		}
	}
	return "", 0
}

// SweepStale removes endpoints whose owning bridge process is gone.
func SweepStale() {
	dir, err := runtimeDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, entry := range entries {
		pid, ok := ownerPID(entry.Name())
		if !ok || pid == self || processAlive(pid) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

func ownerPID(name string) (int, bool) {
	if !strings.HasSuffix(name, ".sock") {
		return 0, false
	}
	base := strings.TrimSuffix(name, ".sock")
	dash := strings.LastIndexByte(base, '-')
	if dash < 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(base[dash+1:])
	return pid, err == nil
}

func runtimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "agent99")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func rootPrefix(root string) string {
	sum := sha1.Sum([]byte(root))
	return hex.EncodeToString(sum[:6])
}

func remoteExpr(endpoint, expression string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteExprTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "nvim", "--server", endpoint, "--remote-expr", expression)
	var output, stderr strings.Builder
	command.Stdout = &output
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("nvim RPC failed: no answer from %s within %s. The request "+
				"was not cancelled and is still running in that Neovim; a language server "+
				"answering its first request on a big project takes longer than this, so "+
				"retry before treating the instance as wedged",
				endpoint, remoteExprTimeout)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(output.String())
		}
		if strings.Contains(detail, "Agent99Rpc") {
			detail += " (is the agent99 plugin on the runtimepath of that Neovim?)"
		}
		return "", fmt.Errorf("nvim RPC failed: %s", detail)
	}
	return output.String(), nil
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func (b *Backend) stderrText() string {
	if b.stderr == nil {
		return ""
	}
	return b.stderr.String()
}

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
