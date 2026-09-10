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
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type serviceConfig struct {
	SocketPath       string
	StateDir         string
	HTTPAddress      string
	ProviderQuota    int
	ExternalJobQuota int
}

type controlHello struct {
	Profile string
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
		flags.IntVar(&config.ProviderQuota, "provider-quota", 4, "maximum concurrent provider-backed jobs")
		flags.IntVar(&config.ExternalJobQuota, "external-job-quota", 2, "maximum concurrent external jobs")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: huyang serve [--socket PATH] [--state-dir PATH] [--http LOOPBACK:PORT]")
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
	if err := validateModernRegistry(); err != nil {
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

func proxyHuyangMCP(socketPath string, profile mcpProfile, stdin io.Reader, stdout io.Writer) error {
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("connect service at %s: %w", socketPath, err)
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(controlHello{Profile: string(profile)}); err != nil {
		return err
	}
	readDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(stdout, connection)
		readDone <- copyErr
	}()
	_, writeErr := io.Copy(connection, stdin)
	_ = connection.CloseWrite()
	readErr := <-readDone
	if writeErr != nil {
		return writeErr
	}
	return readErr
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
	profiles := map[string]mcpProfile{
		"/mcp": profileFull, "/mcp/orient": profileOrient,
		"/mcp/edit": profileEdit, "/mcp/debug": profileDebug,
	}
	for route, profile := range profiles {
		server := newSDKServer(profile, s.direct)
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
		mux.Handle(route, requireBearer(token, handler))
	}
	s.httpServer = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
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

func modernOnlyProfile(name string) (mcpProfile, error) {
	profile := mcpProfile(name)
	switch profile {
	case profileFull, profileOrient, profileEdit, profileDebug:
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
