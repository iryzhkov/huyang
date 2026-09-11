// Package embed implements an owned nvim --embed --headless provider.
package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"

	"github.com/neovim/go-client/nvim"
)

const (
	startTimeout  = 20 * time.Second
	stopTimeout   = 3 * time.Second
	healthTimeout = 2 * time.Second
	stderrLimit   = 32 << 10

	protocolVersion  = 1
	completionMethod = "agent99/result"
)

var (
	instanceSeq atomic.Int64
	requestSeq  atomic.Uint64
)

var capabilities = []provider.Capability{
	provider.CapabilityExecute,
	provider.CapabilityNavigation,
	provider.CapabilityRename,
	provider.CapabilityDiagnostics,
	provider.CapabilityCodeActions,
}

// Config controls creation of an owned embedded provider.
type Config struct {
	Root        string
	InitFile    string
	RuntimePath string
	Debug       bool
	Executable  string
	Trace       func(executable string, args []string)
}

type completion struct {
	payload string
}

type generation struct {
	epoch   uint64
	nvim    *nvim.Nvim
	command *exec.Cmd
	stdin   io.WriteCloser
	channel int
	stderr  *tailBuffer
	done    chan error
	alive   atomic.Bool
}

// Backend owns a replaceable embedded Neovim generation.
type Backend struct {
	mu         sync.Mutex
	config     Config
	descriptor provider.Descriptor
	health     provider.Health
	generation *generation
	pending    map[string]chan completion
	done       chan struct{}
	rootLock   *rootLock
	closed     bool
	closeOnce  sync.Once
}

var _ provider.Provider = (*Backend)(nil)

// Open starts and bootstraps an embedded provider.
func Open(config Config) (*Backend, error) {
	if config.Root == "" {
		return nil, &provider.Failure{Code: provider.FailureLaunch, Detail: "provider root is empty"}
	}
	if config.Executable == "" {
		config.Executable = "nvim"
	}
	lock, err := acquireRootLock(config.Root)
	if err != nil {
		return nil, &provider.Failure{
			Code: provider.FailureLaunch, Detail: "locking workspace root: " + err.Error(), Err: err,
		}
	}
	sum := sha256.Sum256([]byte("embed\x00" + config.Root))
	b := &Backend{
		config:   config,
		pending:  make(map[string]chan completion),
		done:     make(chan struct{}),
		rootLock: lock,
		descriptor: provider.Descriptor{
			ID:           provider.ID("embed_" + hex.EncodeToString(sum[:8])),
			Backend:      "embed",
			Root:         config.Root,
			Cancellation: provider.CancellationProviderRestart,
			Languages:    []string{"*"},
			Capabilities: append([]provider.Capability(nil), capabilities...),
		},
		health: provider.Health{
			State:        provider.HealthStarting,
			ObservedAt:   time.Now(),
			Cancellation: provider.CancellationProviderRestart,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	if _, err := b.ensureGeneration(ctx); err != nil {
		lock.release()
		return nil, err
	}
	return b, nil
}

// Descriptor returns stable identity and the current generation metadata.
func (b *Backend) Descriptor() provider.Descriptor {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.descriptor
	out.Capabilities = append([]provider.Capability(nil), out.Capabilities...)
	out.Languages = append([]string(nil), out.Languages...)
	return out
}

// Health performs a bounded event-loop probe and reports classified lifecycle state.
func (b *Backend) Health(ctx context.Context) provider.Health {
	b.mu.Lock()
	g := b.generation
	health := b.health
	b.mu.Unlock()
	if g == nil || !g.alive.Load() || health.State != provider.HealthHealthy {
		return health
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, healthTimeout)
		defer cancel()
	}
	probed := make(chan error, 1)
	go func() {
		var entered int
		err := g.nvim.Eval("v:vim_did_enter", &entered)
		if err == nil && entered != 1 {
			err = fmt.Errorf("VimEnter is incomplete: v:vim_did_enter=%d", entered)
		}
		probed <- err
	}()
	select {
	case err := <-probed:
		if err == nil {
			return health
		}
		return b.failedHealth(g.epoch, provider.FailureProtocol, err.Error())
	case <-g.done:
		return b.currentHealth()
	case <-ctx.Done():
		return b.failedHealth(g.epoch, provider.FailureDeadline, ctx.Err().Error())
	}
}

// Call starts one Lua request and completes from its request-ID notification.
func (b *Backend) Call(ctx context.Context, request provider.Request) (provider.Result, error) {
	if err := ctx.Err(); err != nil {
		return provider.Result{}, contextFailure(err, 0)
	}
	g, err := b.ensureGeneration(ctx)
	if err != nil {
		return provider.Result{}, err
	}
	id := request.Context.RequestID
	if id == "" {
		id = fmt.Sprintf("embed-%d", requestSeq.Add(1))
	}
	key := pendingKey(g.epoch, id)
	completed := make(chan completion, 1)
	b.mu.Lock()
	if b.closed || b.generation != g || !g.alive.Load() {
		b.mu.Unlock()
		return provider.Result{}, &provider.Failure{
			Code: provider.FailureDied, Epoch: g.epoch, Detail: "provider changed before request submission",
		}
	}
	if _, exists := b.pending[key]; exists {
		b.mu.Unlock()
		return provider.Result{}, &provider.Failure{
			Code: provider.FailureProtocol, Epoch: g.epoch, Detail: "duplicate in-flight request ID " + id,
		}
	}
	b.pending[key] = completed
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, key)
		b.mu.Unlock()
	}()

	payload := map[string]any{"tool": request.Operation, "args": request.Arguments}
	var started string
	if err := g.nvim.ExecLua(
		`return require("huyang.rpc").start_notify(...)`,
		&started, g.channel, id, payload,
	); err != nil {
		return provider.Result{}, &provider.Failure{
			Code: provider.FailureProtocol, Epoch: g.epoch,
			Detail: "starting Lua request: " + err.Error(), Err: err,
		}
	}
	if started != id {
		return provider.Result{}, &provider.Failure{
			Code: provider.FailureProtocol, Epoch: g.epoch,
			Detail: fmt.Sprintf("Lua acknowledged request %q as %q", id, started),
		}
	}

	select {
	case result := <-completed:
		return decodeCompletion(g.epoch, result.payload)
	case err := <-g.done:
		return provider.Result{}, deathFailure(g, err)
	case <-ctx.Done():
		cause := ctx.Err()
		b.terminate(g)
		<-g.done
		restartCtx, cancel := context.WithTimeout(context.Background(), startTimeout)
		_, restartErr := b.ensureGeneration(restartCtx)
		cancel()
		failure := contextFailure(cause, g.epoch)
		if restartErr != nil {
			failure.Detail += "; restart failed: " + restartErr.Error()
		}
		return provider.Result{}, failure
	}
}

// Save writes every modified named buffer through the current generation.
func (b *Backend) Save(ctx context.Context) error {
	g, err := b.ensureGeneration(ctx)
	if err != nil {
		return err
	}
	finished := make(chan error, 1)
	go func() {
		var messages []string
		err := g.nvim.ExecLua(`return require("huyang.lsp").save_all()`, &messages)
		if err == nil && len(messages) != 0 {
			err = errors.New(strings.Join(messages, "; "))
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		if err != nil {
			return &provider.Failure{Code: provider.FailureProtocol, Epoch: g.epoch, Detail: "saving buffers: " + err.Error(), Err: err}
		}
		return nil
	case err := <-g.done:
		return deathFailure(g, err)
	case <-ctx.Done():
		b.terminate(g)
		return contextFailure(ctx.Err(), g.epoch)
	}
}

// Close gracefully stops the current generation and permanently closes the backend.
func (b *Backend) Close(context.Context) error {
	b.closeOnce.Do(func() {
		defer b.rootLock.release()
		b.mu.Lock()
		b.closed = true
		g := b.generation
		b.health = provider.Health{
			State: provider.HealthClosed, Epoch: b.descriptor.Epoch,
			ObservedAt: time.Now(), Cancellation: provider.CancellationProviderRestart,
		}
		close(b.done)
		b.mu.Unlock()
		if g == nil || !g.alive.Load() {
			return
		}
		lspFinished := make(chan struct{})
		go func() {
			_ = g.nvim.ExecLua("pcall(function() for _, client in ipairs(vim.lsp.get_clients()) do client:stop(true) end vim.wait(2000, function() return #vim.lsp.get_clients() == 0 end, 20) end)", nil)
			close(lspFinished)
		}()
		select {
		case <-lspFinished:
		case <-time.After(stopTimeout):
		}
		if b.config.Debug {
			finished := make(chan struct{})
			go func() {
				_ = g.nvim.ExecLua(`pcall(function() require("huyang.dap").shutdown_sync() end)`, nil)
				close(finished)
			}()
			select {
			case <-finished:
			case <-time.After(stopTimeout):
			}
		}
		finished := make(chan error, 1)
		go func() { finished <- g.nvim.Command("qa!") }()
		select {
		case <-g.done:
		case <-finished:
			select {
			case <-g.done:
			case <-time.After(stopTimeout):
				b.terminate(g)
				<-g.done
			}
		case <-time.After(stopTimeout):
			b.terminate(g)
			<-g.done
		}
		_ = g.nvim.Close()
	})
	return nil
}

// Done closes only when the backend is permanently closed; generations may restart.
func (b *Backend) Done() <-chan struct{} { return b.done }

func (b *Backend) ensureGeneration(ctx context.Context) (*generation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, &provider.Failure{
			Code: provider.FailureDied, Epoch: b.descriptor.Epoch, Detail: "provider is closed",
		}
	}
	if g := b.generation; g != nil && g.alive.Load() {
		return g, nil
	}
	return b.startGenerationLocked(ctx)
}

func (b *Backend) startGenerationLocked(ctx context.Context) (*generation, error) {
	select {
	case <-ctx.Done():
		return nil, contextFailure(ctx.Err(), b.descriptor.Epoch)
	default:
	}
	epoch := b.descriptor.Epoch + 1
	args := []string{"--embed", "--headless", "--cmd", "set noswapfile shadafile=NONE"}
	if b.config.InitFile != "" {
		args = append(args, "--clean", "-u", b.config.InitFile)
	}
	if b.config.Trace != nil {
		b.config.Trace(b.config.Executable, append([]string(nil), args...))
	}
	command := exec.Command(b.config.Executable, args...)
	command.Dir = b.config.Root
	setDeathSignal(command)
	stderr := &tailBuffer{}
	command.Stderr = stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, launchFailure(epoch, "opening Neovim stdin", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, launchFailure(epoch, "opening Neovim stdout", err)
	}
	if err := command.Start(); err != nil {
		return nil, launchFailure(epoch, "starting nvim", err)
	}
	client, err := nvim.New(stdout, stdin, stdin, nil)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, launchFailure(epoch, "creating embedded RPC client", err)
	}
	g := &generation{
		epoch: epoch, nvim: client, command: command, stdin: stdin,
		stderr: stderr, done: make(chan error, 1),
	}
	if err := client.RegisterHandler(completionMethod, func(id, payload string) {
		b.deliver(g.epoch, id, payload)
	}); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, &provider.Failure{Code: provider.FailureBootstrap, Epoch: epoch, Detail: "registering completion handler: " + err.Error(), Err: err}
	}
	g.alive.Store(true)
	b.generation = g
	b.descriptor.Epoch = epoch
	b.descriptor.ProcessID = command.Process.Pid
	b.health = provider.Health{
		State: provider.HealthStarting, Epoch: epoch, ObservedAt: time.Now(),
		Cancellation: provider.CancellationProviderRestart,
	}
	go b.serve(g)
	if err := b.bootstrap(ctx, g); err != nil {
		b.terminate(g)
		<-g.done
		return nil, err
	}
	b.health = provider.Health{
		State: provider.HealthHealthy, Epoch: epoch, ObservedAt: time.Now(),
		Cancellation: provider.CancellationProviderRestart,
	}
	return g, nil
}

func (b *Backend) serve(g *generation) {
	serveErr := g.nvim.Serve()
	if g.alive.Swap(false) && g.command.Process != nil {
		_ = g.command.Process.Kill()
	}
	waitErr := g.command.Wait()
	err := errors.Join(serveErr, waitErr)
	g.done <- err
	close(g.done)
	b.mu.Lock()
	if b.generation == g && !b.closed {
		detail := "embedded Neovim exited"
		if err != nil {
			detail += ": " + err.Error()
		}
		if tail := g.stderr.String(); tail != "" {
			detail += "; stderr: " + tail
		}
		b.health = provider.Health{
			State: provider.HealthFailed, FailureCode: string(provider.FailureDied),
			Detail: detail, Epoch: g.epoch, ObservedAt: time.Now(),
			Cancellation: provider.CancellationProviderRestart,
		}
	}
	b.mu.Unlock()
}

func (b *Backend) bootstrap(ctx context.Context, g *generation) error {
	type outcome struct {
		failure *provider.Failure
	}
	finished := make(chan outcome, 1)
	go func() {
		fail := func(code provider.FailureCode, detail string, err error) outcome {
			return outcome{failure: &provider.Failure{Code: code, Epoch: g.epoch, Detail: detail, Err: err}}
		}
		if err := g.nvim.SetClientInfo(
			"huyang", nvim.ClientVersion{Major: 0, Minor: 1}, nvim.EmbedderClientType,
			map[string]*nvim.ClientMethod{}, nvim.ClientAttributes{},
		); err != nil {
			finished <- fail(provider.FailureBootstrap, "setting embedder client info: "+err.Error(), err)
			return
		}
		info, err := g.nvim.APIInfo()
		if err != nil {
			finished <- fail(provider.FailureBootstrap, "reading Neovim API info: "+err.Error(), err)
			return
		}
		channel, ok := info[0].(int64)
		if !ok {
			finished <- fail(provider.FailureProtocol, fmt.Sprintf("unexpected channel ID type %T", info[0]), nil)
			return
		}
		g.channel = int(channel)
		var version map[string]any
		if err := g.nvim.ExecLua(`return vim.version()`, &version); err != nil {
			finished <- fail(provider.FailureBootstrap, "reading Neovim version: "+err.Error(), err)
			return
		}
		major, majorOK := integer(version["major"])
		minor, minorOK := integer(version["minor"])
		if !majorOK || !minorOK || major != 0 || (minor != 11 && minor != 12) {
			finished <- fail(provider.FailureIncompatible,
				fmt.Sprintf("Neovim version %v.%v is outside supported range 0.11-0.12", version["major"], version["minor"]), nil)
			return
		}
		var entered int
		if err := g.nvim.Eval("v:vim_did_enter", &entered); err != nil || entered != 1 {
			finished <- fail(provider.FailureBootstrap,
				fmt.Sprintf("headless startup did not reach VimEnter (value %d): %v", entered, err), err)
			return
		}
		if b.config.RuntimePath != "" {
			if err := g.nvim.ExecLua(`vim.opt.runtimepath:prepend(...)`, nil, b.config.RuntimePath); err != nil {
				finished <- fail(provider.FailureBootstrap, "prepending shipped runtime: "+err.Error(), err)
				return
			}
		}
		var handshake map[string]any
		if err := g.nvim.ExecLua(`return require("huyang.rpc").handshake()`, &handshake); err != nil {
			finished <- fail(provider.FailureBootstrap, "loading agent99 kernel: "+err.Error(), err)
			return
		}
		versionValue, ok := integer(handshake["protocol_version"])
		if !ok || versionValue != protocolVersion || handshake["completion_method"] != completionMethod {
			finished <- fail(provider.FailureIncompatible,
				fmt.Sprintf("kernel handshake mismatch: protocol=%v completion=%v", handshake["protocol_version"], handshake["completion_method"]), nil)
			return
		}
		// The socket oracle necessarily spends at least one 100 ms readiness
		// interval before returning. Give VimEnter-scheduled provider setup the
		// same bounded event-loop turn so an immediate first semantic call sees
		// the language servers enabled by the user's init.
		if err := g.nvim.ExecLua(`vim.wait(100)`, nil); err != nil {
			finished <- fail(provider.FailureBootstrap, "settling provider startup: "+err.Error(), err)
			return
		}
		finished <- outcome{}
	}()
	select {
	case result := <-finished:
		if result.failure != nil {
			return result.failure
		}
		return nil
	case err := <-g.done:
		return deathFailure(g, err)
	case <-ctx.Done():
		return contextFailure(ctx.Err(), g.epoch)
	}
}

func (b *Backend) deliver(epoch uint64, id, payload string) {
	key := pendingKey(epoch, id)
	b.mu.Lock()
	completed := b.pending[key]
	b.mu.Unlock()
	if completed == nil {
		return
	}
	select {
	case completed <- completion{payload: payload}:
	default:
	}
}

func (b *Backend) terminate(g *generation) {
	if g != nil && g.alive.Swap(false) && g.command.Process != nil {
		_ = g.command.Process.Kill()
	}
}

func (b *Backend) currentHealth() provider.Health {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.health
}

func (b *Backend) failedHealth(epoch uint64, code provider.FailureCode, detail string) provider.Health {
	return provider.Health{
		State: provider.HealthFailed, FailureCode: string(code), Detail: detail,
		Epoch: epoch, ObservedAt: time.Now(), Cancellation: provider.CancellationProviderRestart,
	}
}

func pendingKey(epoch uint64, id string) string {
	return fmt.Sprintf("%d\x00%s", epoch, id)
}

func decodeCompletion(epoch uint64, payload string) (provider.Result, error) {
	var response struct {
		OK     bool   `json:"ok"`
		Result any    `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		return provider.Result{}, &provider.Failure{
			Code: provider.FailureProtocol, Epoch: epoch,
			Detail: "unparseable completion notification: " + err.Error(), Err: err,
		}
	}
	if !response.OK {
		return provider.Result{}, errors.New(response.Error)
	}
	return provider.Result{Value: response.Result}, nil
}

func contextFailure(err error, epoch uint64) *provider.Failure {
	code := provider.FailureCancelled
	if errors.Is(err, context.DeadlineExceeded) {
		code = provider.FailureDeadline
	}
	return &provider.Failure{Code: code, Epoch: epoch, Detail: err.Error(), Err: err}
}

func deathFailure(g *generation, err error) *provider.Failure {
	detail := "embedded Neovim exited"
	if err != nil {
		detail += ": " + err.Error()
	}
	if tail := g.stderr.String(); tail != "" {
		detail += "; stderr: " + tail
	}
	return &provider.Failure{Code: provider.FailureDied, Epoch: g.epoch, Detail: detail, Err: err}
}

func launchFailure(epoch uint64, action string, err error) *provider.Failure {
	return &provider.Failure{
		Code: provider.FailureLaunch, Epoch: epoch,
		Detail: action + ": " + err.Error(), Err: err,
	}
}

func integer(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case uint64:
		return int(value), true
	case float64:
		return int(value), value == float64(int(value))
	default:
		return 0, false
	}
}

type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > stderrLimit {
		t.b = append([]byte(nil), t.b[len(t.b)-stderrLimit:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}

func (t *tailBuffer) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.b)
}
