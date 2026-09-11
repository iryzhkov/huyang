package bridge

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

type controlHello struct {
	Profile string
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

func HuyangMain() {
	if err := runHuyang(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "huyang:", err)
		os.Exit(1)
	}
}

func runHuyang(arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: huyang <serve|mcp>")
	}
	switch arguments[0] {
	case "serve":
		flags := flag.NewFlagSet("huyang serve", flag.ContinueOnError)
		flags.SetOutput(stderr)
		config := serviceConfig{}
		flags.StringVar(&config.SocketPath, "socket", defaultHuyangSocket(), "private Unix control socket")
		flags.StringVar(&config.StateDir, "state-dir", defaultHuyangStateDir(), "durable service state directory")
		flags.StringVar(&config.HTTPAddress, "http", "", "optional loopback Streamable HTTP address")
		flags.StringVar(&config.PprofAddress, "pprof", "", "optional loopback address serving net/http/pprof profiles behind the HTTP bearer token, for heap and goroutine investigation")
		flags.IntVar(&config.ProviderQuota, "provider-quota", 4, "maximum concurrent provider-backed jobs")
		flags.IntVar(&config.ExternalJobQuota, "external-job-quota", 2, "maximum concurrent external jobs")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: huyang serve [--socket PATH] [--state-dir PATH] [--http LOOPBACK:PORT] [--pprof LOOPBACK:PORT] [--provider-quota N] [--external-job-quota N]")
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
		flags.StringVar(&profileName, "profile", "full", "fixed catalog: full, orient, edit, or debug")
		flags.StringVar(&socketPath, "socket", socketPath, "Huyang Unix control socket")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: huyang mcp [--profile full|orient|edit|debug] [--socket PATH]")
		}
		profile, err := modernOnlyProfile(profileName)
		if err != nil {
			return err
		}
		return proxyHuyangMCP(socketPath, profile, stdin, stdout)
	default:
		return fmt.Errorf("unknown subcommand %q; usage: huyang <serve|mcp>", arguments[0])
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
	_ = newSDKServer(profile, s.direct).Run(ctx, transport)
}

func proxyHuyangMCP(socketPath string, profile mcpapi.Profile, stdin io.Reader, stdout io.Writer) error {
	type envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}

	done := make(chan struct{})
	defer close(done)
	clientEvents := make(chan proxyLineEvent, 32)
	go readProxyLines(stdin, 0, clientEvents, done)
	backendEvents := make(chan proxyLineEvent, 32)

	var connection *net.UnixConn
	var generation uint64
	var initializeRequest, initializedNotification []byte
	var initializeID string
	clientInitialized := false
	outstanding := make(map[string][]byte)

	message := func(line []byte) envelope {
		var value envelope
		_ = json.Unmarshal(line, &value)
		return value
	}
	idKey := func(id json.RawMessage) string {
		value := strings.TrimSpace(string(id))
		if value == "" || value == "null" {
			return ""
		}
		return value
	}

	connect := func(restarting bool) error {
		deadline := time.Now().Add(30 * time.Second)
		for {
			err := func() error {
				candidate, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
				if err != nil {
					return fmt.Errorf("connect service at %s: %w", socketPath, err)
				}
				connection = candidate
				fail := func(err error) error {
					_ = candidate.Close()
					return err
				}
				if err := json.NewEncoder(candidate).Encode(controlHello{Profile: string(profile)}); err != nil {
					return fail(fmt.Errorf("identify service connection: %w", err))
				}
				reader := bufio.NewReader(candidate)
				if restarting && clientInitialized && len(initializeRequest) > 0 {
					if _, err := candidate.Write(initializeRequest); err != nil {
						return fail(fmt.Errorf("send service reinitialize request: %w", err))
					}
					_ = candidate.SetReadDeadline(time.Now().Add(10 * time.Second))
					for {
						line, err := reader.ReadBytes('\n')
						if err != nil {
							return fail(fmt.Errorf("reinitialize service connection: %w", err))
						}
						if idKey(message(line).ID) == initializeID {
							break
						}
					}
					_ = candidate.SetReadDeadline(time.Time{})
					if len(initializedNotification) > 0 {
						if _, err := candidate.Write(initializedNotification); err != nil {
							return fail(fmt.Errorf("send service initialized notification: %w", err))
						}
					}
				}
				for _, line := range outstanding {
					if _, err := candidate.Write(line); err != nil {
						return fail(fmt.Errorf("replay outstanding service request: %w", err))
					}
				}
				generation++
				go readProxyLines(reader, generation, backendEvents, done)
				return nil
			}()
			if err == nil {
				return nil
			}
			if !restarting || time.Now().After(deadline) {
				return err
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	if err := connect(false); err != nil {
		return err
	}
	defer func() {
		if connection != nil {
			_ = connection.Close()
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
			value := message(event.line)
			key := idKey(value.ID)
			if value.Method == "initialize" {
				initializeRequest = append([]byte(nil), event.line...)
				initializeID = key
			}
			if value.Method == "notifications/initialized" || value.Method == "initialized" {
				initializedNotification = append([]byte(nil), event.line...)
			}
			if key != "" && value.Method != "" {
				outstanding[key] = append([]byte(nil), event.line...)
			}
			if _, err := connection.Write(event.line); err != nil {
				_ = connection.Close()
				if err := connect(true); err != nil {
					return fmt.Errorf("reconnect service after write failure: %w", err)
				}
			}
		case event := <-backendEvents:
			if event.generation != generation {
				continue
			}
			if event.err != nil {
				_ = connection.Close()
				if err := connect(true); err != nil {
					return fmt.Errorf("reconnect service after disconnect: %w", err)
				}
				continue
			}
			value := message(event.line)
			key := idKey(value.ID)
			if key != "" {
				delete(outstanding, key)
				if key == initializeID {
					clientInitialized = true
				}
			}
			if _, err := stdout.Write(event.line); err != nil {
				return err
			}
		}
	}
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
		server := newSDKServer(profile, s.direct)
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
