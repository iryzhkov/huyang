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

	// defaultCancelGrace bounds how long a cancelled call waits for the
	// kernel to acknowledge cooperative cancellation before the generation
	// is replaced.
	defaultCancelGrace = 3 * time.Second
	// probeAbandonTimeout bounds a health probe whose caller gave up: an
	// event loop that has not answered for this long is treated as dead.
	probeAbandonTimeout = 10 * time.Second
	// enterTimeout bounds the wait for v:vim_did_enter during bootstrap.
	enterTimeout = 5 * time.Second

	// Kernel protocol range accepted by this binary. Both sides ship
	// together, so the range is one version wide until a migration needs
	// more.
	protocolMin      = 2
	protocolMax      = 2
	completionMethod = "huyang/result"
	// minAPILevel is Neovim 0.11's API level. The kernel depends on
	// vim.lsp.config and vim.lsp.enable, which arrived there.
	minAPILevel = 13
)

// kernelMethods are the Lua entry points the Go side calls; the handshake
// must advertise every one of them.
var kernelMethods = []string{"start_notify", "cancel"}

var (
	instanceSeq atomic.Int64
	requestSeq  atomic.Uint64
)

var knownCapabilities = []provider.Capability{
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
	// CancelGrace overrides how long a cancelled call waits for the kernel
	// to acknowledge before the generation is replaced. Zero means the
	// default.
	CancelGrace time.Duration
	// AfterEpoch preserves generation ordering when the pool replaces a backend.
	AfterEpoch uint64
}

// DapRuntime is the outcome of the nvim-dap discovery the kernel runs during
// bootstrap. nvim-dap is not distributed with Huyang; the kernel searches the
// HUYANG_NVIM_DAP_PATH override, the running runtimepath and the host
// Neovim's plugin directories, in that order. Absence never fails bootstrap:
// the debugger tools answer with the dap_runtime_unavailable code instead.
type DapRuntime struct {
	Available bool
	Path      string
	Commit    string
	Source    string
	Searched  []string
	Detail    string
}

// String is the one-line form recorded in the healthy Health.Detail.
func (r DapRuntime) String() string {
	if !r.Available {
		detail := r.Detail
		if detail == "" {
			detail = "not found"
		}
		return "nvim-dap runtime: absent (" + detail + ")"
	}
	out := "nvim-dap runtime: " + r.Path
	if r.Path == "" {
		out = "nvim-dap runtime: loaded from " + r.Source
	}
	if r.Commit != "" {
		out += " (" + r.Commit + ")"
	}
	return out
}

type completion struct {
	payload map[string]any
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
	dapRuntime DapRuntime
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
	if config.CancelGrace <= 0 {
		config.CancelGrace = defaultCancelGrace
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
			Epoch:        config.AfterEpoch,
			Root:         config.Root,
			Cancellation: provider.CancellationCooperative,
			Languages:    []string{"*"},
			Capabilities: append([]provider.Capability(nil), knownCapabilities...),
		},
		health: provider.Health{
			State:        provider.HealthStarting,
			ObservedAt:   time.Now(),
			Cancellation: provider.CancellationCooperative,
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
//
// The probe goroutine is bounded by the generation: it ends when the probe
// answers, when the generation dies, or when probeAbandonTimeout passes with
// no answer, in which case the unresponsive generation is terminated so the
// next call restarts it.
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
		answered := make(chan error, 1)
		go func() {
			var entered int
			err := g.nvim.Eval("v:vim_did_enter", &entered)
			if err == nil && entered != 1 {
				err = fmt.Errorf("VimEnter is incomplete: v:vim_did_enter=%d", entered)
			}
			answered <- err
		}()
		select {
		case err := <-answered:
			probed <- err
		case <-g.done:
			probed <- errors.New("generation exited during probe")
		case <-time.After(probeAbandonTimeout):
			b.terminate(g)
			probed <- errors.New("event loop did not answer the probe")
		}
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
//
// When the caller's context ends first, the kernel is asked to cancel the
// request cooperatively and the call waits up to CancelGrace for the
// completion that acknowledges it. Other in-flight calls are unaffected.
// Only a kernel that never acknowledges (work that does not yield) costs the
// generation: it is replaced, every concurrent call fails with
// provider_died and the epoch advances.
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
	completed, release, err := b.registerPending(g, id)
	if err != nil {
		return provider.Result{}, err
	}
	defer release()
	if err := b.submit(g, id, request); err != nil {
		return provider.Result{}, err
	}
	select {
	case result := <-completed:
		return decodeCompletion(g.epoch, result.payload)
	case err := <-g.done:
		return provider.Result{}, deathFailure(g, err)
	case <-ctx.Done():
		return b.abandon(g, id, ctx.Err(), completed)
	}
}

// registerPending reserves the completion slot for one request ID on the
// current generation and returns the function that frees it.
func (b *Backend) registerPending(g *generation, id string) (chan completion, func(), error) {
	key := pendingKey(g.epoch, id)
	completed := make(chan completion, 1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.generation != g || !g.alive.Load() {
		return nil, nil, &provider.Failure{
			Code: provider.FailureDied, Epoch: g.epoch, Detail: "provider changed before request submission",
		}
	}
	if _, exists := b.pending[key]; exists {
		return nil, nil, &provider.Failure{
			Code: provider.FailureProtocol, Epoch: g.epoch, Detail: "duplicate in-flight request ID " + id,
		}
	}
	b.pending[key] = completed
	release := func() {
		b.mu.Lock()
		delete(b.pending, key)
		b.mu.Unlock()
	}
	return completed, release, nil
}

// submit starts the Lua request and checks that the kernel acknowledged the
// same request ID.
func (b *Backend) submit(g *generation, id string, request provider.Request) error {
	arguments := request.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	payload := map[string]any{
		"tool":    request.Operation,
		"args":    arguments,
		"context": requestContext(request.Context, id, g.epoch),
	}
	var started string
	if err := g.nvim.ExecLua(
		`return require("huyang.rpc").start_notify(...)`,
		&started, g.channel, id, payload,
	); err != nil {
		return &provider.Failure{
			Code: provider.FailureProtocol, Epoch: g.epoch,
			Detail: "starting Lua request: " + err.Error(), Err: err,
		}
	}
	if started != id {
		return &provider.Failure{
			Code: provider.FailureProtocol, Epoch: g.epoch,
			Detail: fmt.Sprintf("Lua acknowledged request %q as %q", id, started),
		}
	}
	return nil
}

// abandon handles a caller context that ended before the kernel completed:
// cooperative cancellation first, generation replacement only when the kernel
// never acknowledges.
func (b *Backend) abandon(g *generation, id string, cause error, completed chan completion) (provider.Result, error) {
	if result, ok := b.cancelCooperatively(g, id, cause, completed); ok {
		value, err := decodeCompletion(g.epoch, result.payload)
		if err == nil {
			// The kernel finished the work before the cancel reached it;
			// the result is real and the caller may still want it.
			return value, nil
		}
		if provider.ErrorCode(err) == "provider_cancelled" {
			failure := contextFailure(cause, g.epoch)
			failure.Detail += "; acknowledged by the kernel"
			return provider.Result{}, failure
		}
		return provider.Result{}, err
	}
	b.terminate(g)
	<-g.done
	restartCtx, cancel := context.WithTimeout(context.Background(), startTimeout)
	_, restartErr := b.ensureGeneration(restartCtx)
	cancel()
	failure := contextFailure(cause, g.epoch)
	failure.Detail += "; the kernel did not acknowledge cancellation within " +
		b.config.CancelGrace.String() + ", generation replaced"
	if restartErr != nil {
		failure.Detail += "; restart failed: " + restartErr.Error()
	}
	return provider.Result{}, failure
}

// cancelCooperatively asks the kernel to cancel one request and waits up to
// CancelGrace for its completion. It reports the completion and true when
// the kernel acknowledged in time.
func (b *Backend) cancelCooperatively(g *generation, id string, cause error, completed <-chan completion) (completion, bool) {
	reason := "cancelled by the service"
	if errors.Is(cause, context.DeadlineExceeded) {
		reason = "request deadline exceeded"
	}
	go func() {
		var acknowledged bool
		_ = g.nvim.ExecLua(`return require("huyang.rpc").cancel(...)`, &acknowledged, id, reason)
	}()
	select {
	case result := <-completed:
		return result, true
	case <-g.done:
		return completion{}, false
	case <-time.After(b.config.CancelGrace):
		return completion{}, false
	}
}

// requestContext is the provider-visible request identity the kernel keeps
// per coroutine. TransactionID tells the kernel whether the call reads the
// staged view of a transaction or the canonical buffers.
func requestContext(rc provider.RequestContext, id string, epoch uint64) map[string]any {
	var deadline int64
	if !rc.Deadline.IsZero() {
		deadline = rc.Deadline.UnixMilli()
	}
	cancellation := rc.Cancellation
	if cancellation == "" {
		cancellation = provider.CancellationCooperative
	}
	return map[string]any{
		"request_id":     id,
		"actor":          rc.Actor,
		"workspace_id":   rc.WorkspaceID,
		"epoch":          epoch,
		"transaction_id": rc.TransactionID,
		"deadline_ms":    deadline,
		"cancellation":   string(cancellation),
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
			ObservedAt: time.Now(), Cancellation: provider.CancellationCooperative,
		}
		close(b.done)
		b.mu.Unlock()
		if g == nil || !g.alive.Load() {
			return
		}
		g.within(stopTimeout, func() {
			_ = g.nvim.ExecLua("pcall(function() for _, client in ipairs(vim.lsp.get_clients()) do client:stop(true) end vim.wait(2000, function() return #vim.lsp.get_clients() == 0 end, 20) end)", nil)
		})
		if b.config.Debug {
			g.within(stopTimeout, func() {
				_ = g.nvim.ExecLua(`pcall(function() require("huyang.dap").shutdown_sync() end)`, nil)
			})
		}
		g.within(stopTimeout, func() { _ = g.nvim.Command("qa!") })
		select {
		case <-g.done:
		case <-time.After(stopTimeout):
			b.terminate(g)
			<-g.done
		}
		_ = g.nvim.Close()
	})
	return nil
}

// within runs one RPC exchange and returns when it finishes, when the
// generation dies, or when the timeout passes. A call that outlives the
// timeout keeps its goroutine only until the generation is gone: Close and
// terminate both end the connection, which fails the outstanding request.
func (g *generation) within(timeout time.Duration, fn func()) {
	finished := make(chan struct{})
	go func() {
		fn()
		close(finished)
	}()
	select {
	case <-finished:
	case <-g.done:
	case <-time.After(timeout):
	}
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
	if err := client.RegisterHandler(completionMethod, func(id string, payload map[string]any) {
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
		Cancellation: provider.CancellationCooperative,
	}
	go b.serve(g)
	bootstrapped, err := b.bootstrap(ctx, g)
	if err != nil {
		b.terminate(g)
		<-g.done
		return nil, err
	}
	b.descriptor.Capabilities = bootstrapped.capabilities
	b.dapRuntime = bootstrapped.dapRuntime
	b.health = provider.Health{
		State: provider.HealthHealthy, Epoch: epoch, ObservedAt: time.Now(),
		Detail: b.dapRuntime.String(), Cancellation: provider.CancellationCooperative,
	}
	return g, nil
}

// DapRuntime reports the nvim-dap discovery outcome of the current
// generation. Before the first bootstrap it is the zero value (absent).
func (b *Backend) DapRuntime() DapRuntime {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dapRuntime
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
			Cancellation: provider.CancellationCooperative,
		}
	}
	b.mu.Unlock()
}

// bootstrapOutcome carries one bootstrap attempt across the goroutine boundary
// so the caller can race it against generation death and context end.
type bootstrapOutcome struct {
	result  bootstrapResult
	failure *provider.Failure
}

// bootstrap follows the documented embed sequence: client info, API level
// check, VimEnter, shipped runtime, kernel handshake. It returns the
// capabilities the kernel advertised and the nvim-dap discovery outcome.
func (b *Backend) bootstrap(ctx context.Context, g *generation) (bootstrapResult, error) {
	finished := make(chan bootstrapOutcome, 1)
	go func() { finished <- b.runBootstrap(g) }()
	select {
	case outcome := <-finished:
		if outcome.failure != nil {
			return bootstrapResult{}, outcome.failure
		}
		return outcome.result, nil
	case err := <-g.done:
		return bootstrapResult{}, deathFailure(g, err)
	case <-ctx.Done():
		return bootstrapResult{}, contextFailure(ctx.Err(), g.epoch)
	}
}

// runBootstrap performs the embed sequence on the RPC channel: connect to the
// kernel, prepend the shipped runtime, handshake, then discover nvim-dap.
func (b *Backend) runBootstrap(g *generation) bootstrapOutcome {
	if failure := b.connectKernel(g); failure != nil {
		return bootstrapOutcome{failure: failure}
	}
	// Neovim's byte-compile cache names each entry after the full path of the
	// file it caches, with the separators escaped, so a kernel loaded from a
	// deep checkout - an unattended worker's run directory is around two
	// hundred characters - asks the filesystem for a file name past its limit
	// and the bootstrap dies with ENAMETOOLONG. The cache is enabled by the
	// user's own configuration, which this embedded instance inherits when it
	// is not started clean, and nothing here needs it: the kernel is loaded
	// once per generation.
	if err := g.nvim.ExecLua(`if vim.loader then pcall(vim.loader.disable) end`, nil); err != nil {
		return bootstrapOutcome{failure: bootstrapFailure(g, provider.FailureBootstrap, "disabling the Lua byte-compile cache: "+err.Error(), err)}
	}
	if b.config.RuntimePath != "" {
		if err := g.nvim.ExecLua(`vim.opt.runtimepath:prepend(...)`, nil, b.config.RuntimePath); err != nil {
			return bootstrapOutcome{failure: bootstrapFailure(g, provider.FailureBootstrap, "prepending shipped runtime: "+err.Error(), err)}
		}
	}
	var handshake map[string]any
	if err := g.nvim.ExecLua(`return require("huyang.rpc").handshake()`, &handshake); err != nil {
		return bootstrapOutcome{failure: bootstrapFailure(g, provider.FailureBootstrap, "loading huyang kernel: "+err.Error(), err)}
	}
	capabilities, failure := checkHandshake(g.epoch, handshake)
	if failure != nil {
		return bootstrapOutcome{failure: failure}
	}
	// nvim-dap is discovered by the kernel after the Huyang runtime is
	// on the runtimepath. A missing plugin is a normal outcome recorded
	// in the handshake, never a bootstrap failure.
	var discovered map[string]any
	dapRuntime := DapRuntime{}
	if err := g.nvim.ExecLua(dapDiscoveryLua, &discovered); err != nil {
		dapRuntime.Detail = "nvim-dap discovery failed: " + err.Error()
	} else {
		dapRuntime = decodeDapRuntime(discovered)
	}
	return bootstrapOutcome{result: bootstrapResult{capabilities: capabilities, dapRuntime: dapRuntime}}
}

// connectKernel identifies Huyang to Neovim, records the RPC channel, checks
// the API level and waits for VimEnter so init-scheduled provider setup has
// had its event-loop turn.
func (b *Backend) connectKernel(g *generation) *provider.Failure {
	if err := g.nvim.SetClientInfo(
		"huyang", nvim.ClientVersion{Major: 0, Minor: 1}, nvim.EmbedderClientType,
		map[string]*nvim.ClientMethod{}, nvim.ClientAttributes{},
	); err != nil {
		return bootstrapFailure(g, provider.FailureBootstrap, "setting embedder client info: "+err.Error(), err)
	}
	info, err := g.nvim.APIInfo()
	if err != nil {
		return bootstrapFailure(g, provider.FailureBootstrap, "reading Neovim API info: "+err.Error(), err)
	}
	if len(info) < 2 {
		return bootstrapFailure(g, provider.FailureProtocol, fmt.Sprintf("nvim_get_api_info returned %d elements", len(info)), nil)
	}
	channel, ok := info[0].(int64)
	if !ok {
		return bootstrapFailure(g, provider.FailureProtocol, fmt.Sprintf("unexpected channel ID type %T", info[0]), nil)
	}
	g.channel = int(channel)
	if failure := checkAPILevel(g.epoch, info[1]); failure != nil {
		return failure
	}
	// --headless reaches VimEnter without a UI, but not necessarily
	// before the first RPC request is answered. Waiting here also gives
	// VimEnter-scheduled provider setup (language servers enabled by the
	// init) its event-loop turn before the first semantic call.
	var entered bool
	if err := g.nvim.ExecLua(
		`return vim.wait(..., function() return vim.v.vim_did_enter == 1 end, 10)`,
		&entered, enterTimeout.Milliseconds(),
	); err != nil || !entered {
		return bootstrapFailure(g, provider.FailureBootstrap,
			fmt.Sprintf("headless startup did not reach VimEnter within %s: %v", enterTimeout, err), err)
	}
	return nil
}

func bootstrapFailure(g *generation, code provider.FailureCode, detail string, err error) *provider.Failure {
	return &provider.Failure{Code: code, Epoch: g.epoch, Detail: detail, Err: err}
}

// bootstrapResult is what a successful bootstrap learned from the kernel.
type bootstrapResult struct {
	capabilities []provider.Capability
	dapRuntime   DapRuntime
}

// dapDiscoveryLua asks the kernel to locate nvim-dap. A kernel that cannot
// even load its debugger module reports that as an absent runtime rather
// than raising, so the semantic provider still comes up.
const dapDiscoveryLua = `
local ok, report = pcall(function() return require("huyang.dap").discover_runtime() end)
if ok and type(report) == "table" then return report end
return { available = false, detail = "huyang.dap failed to load: " .. tostring(report) }
`

func decodeDapRuntime(report map[string]any) DapRuntime {
	out := DapRuntime{}
	if report == nil {
		out.Detail = "nvim-dap discovery returned nothing"
		return out
	}
	out.Available, _ = report["available"].(bool)
	out.Path, _ = report["path"].(string)
	out.Commit, _ = report["commit"].(string)
	out.Source, _ = report["source"].(string)
	out.Detail, _ = report["detail"].(string)
	out.Searched = stringList(report["searched"])
	return out
}

// checkAPILevel accepts any Neovim whose API level is at least minAPILevel.
func checkAPILevel(epoch uint64, metadata any) *provider.Failure {
	meta, ok := metadata.(map[string]any)
	if !ok {
		return &provider.Failure{Code: provider.FailureProtocol, Epoch: epoch,
			Detail: fmt.Sprintf("unexpected API metadata type %T", metadata)}
	}
	version, _ := meta["version"].(map[string]any)
	level, ok := integer(version["api_level"])
	if !ok {
		return &provider.Failure{Code: provider.FailureIncompatible, Epoch: epoch,
			Detail: fmt.Sprintf("Neovim API info has no api_level (version %v)", version)}
	}
	if level < minAPILevel {
		return &provider.Failure{Code: provider.FailureIncompatible, Epoch: epoch,
			Detail: fmt.Sprintf("Neovim %v.%v (API level %d) is too old: Huyang needs API level %d (Neovim 0.11) or newer",
				version["major"], version["minor"], level, minAPILevel)}
	}
	return nil
}

// checkHandshake validates the kernel's protocol range, completion method,
// cancellation mode and the entry points the Go side calls, and returns the
// capabilities it advertised.
func checkHandshake(epoch uint64, handshake map[string]any) ([]provider.Capability, *provider.Failure) {
	incompatible := func(format string, args ...any) ([]provider.Capability, *provider.Failure) {
		return nil, &provider.Failure{Code: provider.FailureIncompatible, Epoch: epoch, Detail: fmt.Sprintf(format, args...)}
	}
	version, ok := integer(handshake["protocol_version"])
	if !ok || version < protocolMin || version > protocolMax {
		return incompatible("kernel protocol version %v is outside the accepted range %d-%d",
			handshake["protocol_version"], protocolMin, protocolMax)
	}
	if handshake["completion_method"] != completionMethod {
		return incompatible("kernel completion method %v, want %s", handshake["completion_method"], completionMethod)
	}
	if handshake["cancellation"] != string(provider.CancellationCooperative) {
		return incompatible("kernel cancellation %v, want %s", handshake["cancellation"], provider.CancellationCooperative)
	}
	methods := stringList(handshake["methods"])
	for _, method := range kernelMethods {
		if !containsString(methods, method) {
			return incompatible("kernel handshake does not advertise method %s (has %v)", method, methods)
		}
	}
	var capabilities []provider.Capability
	for _, name := range stringList(handshake["capabilities"]) {
		for _, known := range knownCapabilities {
			if provider.Capability(name) == known {
				capabilities = append(capabilities, known)
			}
		}
	}
	if len(capabilities) == 0 {
		return incompatible("kernel handshake advertises no known capabilities (has %v)", handshake["capabilities"])
	}
	return capabilities, nil
}

func (b *Backend) deliver(epoch uint64, id string, payload map[string]any) {
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
		Epoch: epoch, ObservedAt: time.Now(), Cancellation: provider.CancellationCooperative,
	}
}

func pendingKey(epoch uint64, id string) string {
	return fmt.Sprintf("%d\x00%s", epoch, id)
}

// completionPayload is the kernel's completion table, carried natively over
// MessagePack and decoded here through JSON so the result value keeps the
// shape callers relied on (float64 numbers, map[string]any objects).
type completionPayload struct {
	OK       bool                        `json:"ok"`
	Result   any                         `json:"result"`
	Error    *completionError            `json:"error"`
	Touched  []provider.DocumentSnapshot `json:"touched"`
	Evidence []provider.EvidenceBatch    `json:"evidence"`
	Health   *completionHealth           `json:"health"`
}

type completionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail"`
}

type completionHealth struct {
	State       string `json:"state"`
	LSPClients  int    `json:"lsp_clients"`
	Transaction string `json:"transaction"`
}

func decodeCompletion(epoch uint64, payload map[string]any) (provider.Result, error) {
	protocol := func(detail string, err error) (provider.Result, error) {
		return provider.Result{}, &provider.Failure{
			Code: provider.FailureProtocol, Epoch: epoch, Detail: detail, Err: err,
		}
	}
	if payload == nil {
		return protocol("empty completion notification", nil)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return protocol("unencodable completion notification: "+err.Error(), err)
	}
	var response completionPayload
	if err := json.Unmarshal(encoded, &response); err != nil {
		return protocol("unparseable completion notification: "+err.Error(), err)
	}
	result := provider.Result{
		Touched:  response.Touched,
		Evidence: response.Evidence,
	}
	if response.Health != nil {
		detail := fmt.Sprintf("%d language server clients", response.Health.LSPClients)
		if response.Health.Transaction != "" {
			detail += "; transaction " + response.Health.Transaction + " holds the lease"
		}
		result.Health = provider.Health{
			State: provider.HealthState(response.Health.State), Detail: detail,
			Epoch: epoch, ObservedAt: time.Now(), Cancellation: provider.CancellationCooperative,
		}
	}
	if !response.OK {
		if response.Error == nil {
			return protocol("failed completion carries no error", nil)
		}
		operation := &provider.ProviderError{
			Code: response.Error.Code, Message: response.Error.Message, Epoch: epoch,
		}
		if response.Error.Detail != nil {
			operation.Detail = fmt.Sprint(response.Error.Detail)
		}
		if operation.Code == "" {
			operation.Code = "lua_error"
		}
		return result, operation
	}
	result.Value = response.Result
	return result, nil
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

func stringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
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
