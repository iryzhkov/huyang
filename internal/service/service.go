package service

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type serviceConfig struct {
	SocketPath       string
	StateDir         string
	HTTPAddress      string
	PprofAddress     string
	ProviderQuota    int
	ExternalJobQuota int
}

// controlHello is what an adapter says when it connects. The session is the
// agent session the adapter belongs to: the service is long-lived and shared,
// so the identity of the agent making a call can only come from the process
// the client started.
type controlHello struct {
	Profile string
	Session string `json:",omitempty"`
}

type proxyLineEvent struct {
	line       []byte
	err        error
	generation uint64
}

type huyangService struct {
	config        serviceConfig
	direct        *directWorkspaces
	unixListener  *net.UnixListener
	socketInfo    os.FileInfo
	httpListener  net.Listener
	httpServer    *http.Server
	httpToken     string
	httpTokenFile string
	pprofListener net.Listener
	pprofServer   *http.Server

	closeOnce sync.Once
}

func Main() {
	if err := runHuyang(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "huyang:", err)
		os.Exit(1)
	}
}

func runHuyang(arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: huyang <serve|mcp|trust>")
	}
	switch arguments[0] {
	case "serve":
		flags := flag.NewFlagSet("huyang serve", flag.ContinueOnError)
		flags.SetOutput(stderr)
		config := serviceConfig{}
		flags.StringVar(&config.SocketPath, "socket", defaultHuyangSocket(), "private Unix control socket")
		flags.StringVar(&config.StateDir, "state-dir", defaultHuyangStateDir(), "durable service state directory")
		flags.StringVar(&config.HTTPAddress, "http", "", "optional loopback Streamable HTTP address; a request may carry an X-Huyang-Client header so diagnostic_updates is a per-client delta across its stateless requests")
		flags.StringVar(&config.PprofAddress, "pprof", "", "optional loopback address serving net/http/pprof profiles behind the HTTP bearer token, for heap and goroutine investigation")
		flags.IntVar(&config.ProviderQuota, "provider-quota", 4, "maximum concurrent provider-backed jobs")
		flags.IntVar(&config.ExternalJobQuota, "external-job-quota", 2, "maximum concurrent external jobs")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: huyang serve [--socket PATH] [--state-dir PATH] [--http LOOPBACK:PORT] [--pprof LOOPBACK:PORT] [--provider-quota N] [--external-job-quota N]; HTTP requests may set X-Huyang-Client to receive diagnostic_updates as a per-client delta")
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer stop()
		service, err := newHuyangService(config)
		if err != nil {
			return err
		}
		defer service.Close()
		fmt.Fprintf(stderr, "huyang serve: socket=%s\n", config.SocketPath)
		if service.httpListener != nil {
			fmt.Fprintf(stderr, "huyang serve: http=%s token_file=%s\n", service.httpListener.Addr(), service.httpTokenFile)
		}
		if service.pprofListener != nil {
			fmt.Fprintf(stderr, "huyang serve: pprof=%s token_file=%s\n", service.pprofListener.Addr(), service.httpTokenFile)
		}
		return service.Serve(ctx)
	case "mcp":
		flags := flag.NewFlagSet("huyang mcp", flag.ContinueOnError)
		flags.SetOutput(stderr)
		profileName := "full"
		socketPath := defaultHuyangSocket()
		// edit is the default because it is what a coding session uses: the
		// four debugger tools are 1,654 of the full catalog's 9,035 tokens,
		// paid by every session, and the fleet spool has them called in 27 of
		// 144 sessions. A session that wants them loads the debugger profile
		// beside this one, or asks for full.
		flags.StringVar(&profileName, "profile", "edit", "fixed catalog: edit (default), full, orient, debug, debugger, or experimental")
		flags.StringVar(&socketPath, "socket", socketPath, "Huyang Unix control socket")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: huyang mcp [--profile edit|full|orient|debug|debugger|experimental] [--socket PATH]")
		}
		profile, err := modernOnlyProfile(profileName)
		if err != nil {
			return err
		}
		return proxyHuyangMCP(socketPath, profile, stdin, stdout)
	case "trust":
		return runTrust(arguments[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown subcommand %q; usage: huyang <serve|mcp|trust>", arguments[0])
	}
}

func newHuyangService(config serviceConfig) (*huyangService, error) {
	if config.SocketPath == "" || config.StateDir == "" {
		return nil, errors.New("socket and state directory are required")
	}
	if config.ProviderQuota < 1 || config.ExternalJobQuota < 1 {
		return nil, errors.New("provider and external-job quotas must be positive")
	}
	if err := mcpapi.ValidateRegistry(); err != nil {
		return nil, err
	}
	stateDir, err := filepath.Abs(config.StateDir)
	if err != nil {
		return nil, err
	}
	socketPath, err := filepath.Abs(config.SocketPath)
	if err != nil {
		return nil, err
	}
	config.StateDir = stateDir
	config.SocketPath = socketPath
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	direct := newDirectWorkspacesWithQuotas(stateDir, config.ProviderQuota, config.ExternalJobQuota)
	if direct.loadErr != nil {
		return nil, direct.loadErr
	}
	listener, socketInfo, err := listenPrivateUnix(socketPath)
	if err != nil {
		return nil, err
	}
	service := &huyangService{
		config: config, direct: direct, unixListener: listener, socketInfo: socketInfo,
	}
	direct.handlers.ProviderPool().StartReaper(providerpool.DefaultReapInterval)
	direct.startSweeper(registrySweepInterval)
	pruneFrictionSpool(time.Now())
	if config.HTTPAddress != "" {
		if err := service.prepareHTTP(); err != nil {
			service.Close()
			return nil, err
		}
	}
	if config.PprofAddress != "" {
		if err := service.preparePprof(); err != nil {
			service.Close()
			return nil, err
		}
	}
	return service, nil
}

func (s *huyangService) Serve(ctx context.Context) error {
	errorsOut := make(chan error, 2)
	go func() { errorsOut <- s.serveUnix(ctx) }()
	if s.httpListener != nil {
		go func() {
			err := s.httpServer.Serve(s.httpListener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			errorsOut <- err
		}()
	}
	if s.pprofListener != nil {
		go func() {
			err := s.pprofServer.Serve(s.pprofListener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			errorsOut <- err
		}()
	}
	select {
	case <-ctx.Done():
		s.Close()
		return nil
	case err := <-errorsOut:
		s.Close()
		return err
	}
}

func (s *huyangService) Close() {
	s.closeOnce.Do(func() {
		if s.httpServer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.httpServer.Shutdown(ctx)
			cancel()
		}
		if s.httpListener != nil {
			_ = s.httpListener.Close()
		}
		if s.pprofServer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.pprofServer.Shutdown(ctx)
			cancel()
		}
		if s.pprofListener != nil {
			_ = s.pprofListener.Close()
		}
		if s.unixListener != nil {
			_ = s.unixListener.Close()
		}
		if current, err := os.Lstat(s.config.SocketPath); err == nil && s.socketInfo != nil && os.SameFile(current, s.socketInfo) {
			_ = os.Remove(s.config.SocketPath)
		}
		s.direct.closeProviders()
	})
}

func (s *huyangService) serveUnix(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.unixListener.Close()
	}()
	for {
		connection, err := s.unixListener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.serveUnixConnection(ctx, connection)
	}
}

func (s *huyangService) serveUnixConnection(ctx context.Context, connection *net.UnixConn) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	line, err := reader.ReadBytes('\n')
	if err != nil || len(line) > 4096 {
		return
	}
	var hello controlHello
	if json.Unmarshal(line, &hello) != nil {
		return
	}
	profile, err := modernOnlyProfile(hello.Profile)
	if err != nil {
		return
	}
	transport := &mcp.IOTransport{
		Reader: structReadCloser{Reader: reader, Closer: connection},
		Writer: connection,
	}
	_ = newSDKServer(profile, s.direct, hello.Session).Run(ctx, transport)
}

// proxyMessage is the part of a JSON-RPC frame the proxy inspects.
type proxyMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

func decodeProxyMessage(line []byte) proxyMessage {
	var value proxyMessage
	_ = json.Unmarshal(line, &value)
	return value
}

func proxyIDKey(id json.RawMessage) string {
	value := strings.TrimSpace(string(id))
	if value == "" || value == "null" {
		return ""
	}
	return value
}

// mcpProxy is the byte proxy behind huyang mcp: it forwards newline-delimited
// JSON-RPC frames between the adapter's stdio and the service socket, and
// reconnects across a service restart by replaying the initialize handshake
// and every request still outstanding.
type mcpProxy struct {
	socketPath string
	profile    mcpapi.Profile
	stdout     io.Writer

	connection *net.UnixConn
	generation uint64
	// backendEvents carries frames read from the service; done stops the
	// reader goroutines once the proxy loop returns.
	backendEvents chan proxyLineEvent
	done          chan struct{}

	initializeRequest       []byte
	initializedNotification []byte
	initializeID            string
	clientInitialized       bool
	outstanding             map[string][]byte
}

func proxyHuyangMCP(socketPath string, profile mcpapi.Profile, stdin io.Reader, stdout io.Writer) error {
	proxy := &mcpProxy{
		socketPath: socketPath, profile: profile, stdout: stdout,
		backendEvents: make(chan proxyLineEvent, 32), done: make(chan struct{}),
		outstanding: make(map[string][]byte),
	}
	defer close(proxy.done)
	clientEvents := make(chan proxyLineEvent, 32)
	go readProxyLines(stdin, 0, clientEvents, proxy.done)
	if err := proxy.connect(false); err != nil {
		return err
	}
	defer func() {
		if proxy.connection != nil {
			_ = proxy.connection.Close()
		}
	}()
	for {
		select {
		case event := <-clientEvents:
			if event.err != nil {
				if errors.Is(event.err, io.EOF) {
					return nil
				}
				return event.err
			}
			if err := proxy.forwardClient(event.line); err != nil {
				return err
			}
		case event := <-proxy.backendEvents:
			if err := proxy.forwardBackend(event); err != nil {
				return err
			}
		}
	}
}

// forwardClient records what a later reconnect must replay and writes the
// frame to the service, reconnecting once when the write fails.
func (p *mcpProxy) forwardClient(line []byte) error {
	value := decodeProxyMessage(line)
	key := proxyIDKey(value.ID)
	if value.Method == "initialize" {
		p.initializeRequest = append([]byte(nil), line...)
		p.initializeID = key
	}
	if value.Method == "notifications/initialized" || value.Method == "initialized" {
		p.initializedNotification = append([]byte(nil), line...)
	}
	if key != "" && value.Method != "" {
		p.outstanding[key] = append([]byte(nil), line...)
	}
	if _, err := p.connection.Write(line); err != nil {
		_ = p.connection.Close()
		if err := p.connect(true); err != nil {
			return fmt.Errorf("reconnect service after write failure: %w", err)
		}
	}
	return nil
}

// forwardBackend writes a service frame to the adapter, settles the request
// it answers, and reconnects when the service connection dropped. Frames
// from a superseded connection are ignored.
func (p *mcpProxy) forwardBackend(event proxyLineEvent) error {
	if event.generation != p.generation {
		return nil
	}
	if event.err != nil {
		_ = p.connection.Close()
		if err := p.connect(true); err != nil {
			return fmt.Errorf("reconnect service after disconnect: %w", err)
		}
		return nil
	}
	value := decodeProxyMessage(event.line)
	if key := proxyIDKey(value.ID); key != "" {
		delete(p.outstanding, key)
		if key == p.initializeID {
			p.clientInitialized = true
		}
	}
	_, err := p.stdout.Write(event.line)
	return err
}

// connect dials the service. A restart retries for up to 30 seconds so the
// adapter survives the service being replaced underneath it.
func (p *mcpProxy) connect(restarting bool) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := p.dial(restarting)
		if err == nil {
			return nil
		}
		if !restarting || time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// dial makes one connection attempt: it identifies the profile, replays the
// initialize handshake after a restart, resends every outstanding request,
// and starts the reader for the new connection generation.
func (p *mcpProxy) dial(restarting bool) error {
	candidate, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: p.socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("connect service at %s: %w", p.socketPath, err)
	}
	p.connection = candidate
	fail := func(err error) error {
		_ = candidate.Close()
		return err
	}
	if err := json.NewEncoder(candidate).Encode(controlHello{Profile: string(p.profile), Session: clientSessionID()}); err != nil {
		return fail(fmt.Errorf("identify service connection: %w", err))
	}
	reader := bufio.NewReader(candidate)
	if restarting && p.clientInitialized && len(p.initializeRequest) > 0 {
		if err := p.reinitialize(candidate, reader); err != nil {
			return fail(err)
		}
	}
	for _, line := range p.outstanding {
		if _, err := candidate.Write(line); err != nil {
			return fail(fmt.Errorf("replay outstanding service request: %w", err))
		}
	}
	p.generation++
	go readProxyLines(reader, p.generation, p.backendEvents, p.done)
	return nil
}

// reinitialize repeats the client's initialize request on a fresh
// connection, waits for its response, and resends the initialized
// notification, so the service session matches what the client believes.
func (p *mcpProxy) reinitialize(candidate *net.UnixConn, reader *bufio.Reader) error {
	if _, err := candidate.Write(p.initializeRequest); err != nil {
		return fmt.Errorf("send service reinitialize request: %w", err)
	}
	_ = candidate.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("reinitialize service connection: %w", err)
		}
		if proxyIDKey(decodeProxyMessage(line).ID) == p.initializeID {
			break
		}
	}
	_ = candidate.SetReadDeadline(time.Time{})
	if len(p.initializedNotification) > 0 {
		if _, err := candidate.Write(p.initializedNotification); err != nil {
			return fmt.Errorf("send service initialized notification: %w", err)
		}
	}
	return nil
}

// readProxyLines forwards newline-delimited frames to events until the reader
// fails or done closes. Selecting on done lets the goroutine exit once the
// proxy loop has returned instead of blocking forever on a full channel.
func readProxyLines(reader io.Reader, generation uint64, events chan<- proxyLineEvent, done <-chan struct{}) {
	buffered := bufio.NewReader(reader)
	send := func(event proxyLineEvent) bool {
		select {
		case events <- event:
			return true
		case <-done:
			return false
		}
	}
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 {
			if !send(proxyLineEvent{line: line, generation: generation}) {
				return
			}
		}
		if err != nil {
			send(proxyLineEvent{err: err, generation: generation})
			return
		}
	}
}

type structReadCloser struct {
	io.Reader
	io.Closer
}

func (s *huyangService) prepareHTTP() error {
	if err := requireLoopbackAddress(s.config.HTTPAddress); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", s.config.HTTPAddress)
	if err != nil {
		return err
	}
	token, tokenFile, err := loadOrCreateHTTPToken(s.config.StateDir)
	if err != nil {
		listener.Close()
		return err
	}
	s.httpListener = listener
	s.httpToken = token
	s.httpTokenFile = tokenFile
	mux := http.NewServeMux()
	profiles := map[string]mcpapi.Profile{
		"/mcp": mcpapi.ProfileFull, "/mcp/orient": mcpapi.ProfileOrient,
		"/mcp/edit": mcpapi.ProfileEdit, "/mcp/debug": mcpapi.ProfileDebug,
	}
	for route, profile := range profiles {
		// An HTTP client announces no session; its calls are spooled under
		// the service process the way they always were.
		server := newSDKServer(profile, s.direct, "")
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
		mux.Handle(route, requireBearer(token, handler))
	}
	s.httpServer = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return nil
}

// preparePprof exposes net/http/pprof on a loopback listener behind the same
// bearer token as the Streamable HTTP transport, so a heap or goroutine
// profile can be taken from the live service without exposing it to other
// local users.
func (s *huyangService) preparePprof() error {
	if err := requireLoopbackAddress(s.config.PprofAddress); err != nil {
		return err
	}
	token, tokenFile, err := loadOrCreateHTTPToken(s.config.StateDir)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", s.config.PprofAddress)
	if err != nil {
		return err
	}
	if s.httpToken == "" {
		s.httpToken = token
		s.httpTokenFile = tokenFile
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	s.pprofListener = listener
	s.pprofServer = &http.Server{Handler: requireBearer(token, mux), ReadHeaderTimeout: 5 * time.Second}
	return nil
}

func requireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid HTTP address: %w", err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("HTTP address must use a numeric loopback host, got %q", host)
	}
	return nil
}

func requireBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			response.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func loadOrCreateHTTPToken(stateDir string) (string, string, error) {
	path := filepath.Join(stateDir, "http-token")
	if content, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(content))
		if len(token) < 32 {
			return "", "", errors.New("HTTP token file contains an invalid token")
		}
		return token, path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(random)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return token, path, nil
}

func listenPrivateUnix(path string) (*net.UnixListener, os.FileInfo, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("refusing to replace non-socket %s", path)
		}
		connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			connection.Close()
			return nil, nil, fmt.Errorf("service already listening at %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		listener.Close()
		return nil, nil, err
	}
	return listener, info, nil
}

func modernOnlyProfile(name string) (mcpapi.Profile, error) {
	profile := mcpapi.Profile(name)
	switch profile {
	case mcpapi.ProfileFull, mcpapi.ProfileOrient, mcpapi.ProfileEdit, mcpapi.ProfileDebug:
		return profile, nil
	case mcpapi.ProfileExperimental:
		// The frozen surface plus whatever is being designed. A caller asks
		// for it by name and knows what it is getting.
		return profile, nil
	default:
		return "", fmt.Errorf("unknown MCP profile %q", name)
	}
}

func defaultHuyangSocket() string {
	if value := os.Getenv("HUYANG_SOCKET"); value != "" {
		return value
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = filepath.Join(os.TempDir(), "huyang-"+strconv.Itoa(os.Getuid()))
	}
	return filepath.Join(base, "huyang", "control.sock")
}

func defaultHuyangStateDir() string {
	if value := os.Getenv("HUYANG_STATE_DIR"); value != "" {
		return value
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			base = filepath.Join(home, ".local", "state")
		} else {
			base = os.TempDir()
		}
	}
	return filepath.Join(base, "huyang")
}
