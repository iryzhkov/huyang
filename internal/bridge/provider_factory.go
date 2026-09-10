package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent99/internal/provider"
	embedprovider "agent99/internal/provider/embed"
	socketprovider "agent99/internal/provider/socket"
)

type providerOpenConfig struct {
	Root        string
	InitFile    string
	RuntimePath string
	Debug       bool
}

type providerFactory interface {
	Open(providerOpenConfig) (provider.Provider, error)
	Attach(root, endpoint string) provider.Provider
	FindForeign(root string) (endpoint string, processID int)
	SweepStale()
}

type configuredProviderFactory struct {
	backend string
}

func (f configuredProviderFactory) Open(config providerOpenConfig) (provider.Provider, error) {
	switch f.backend {
	case "", "socket":
		return socketprovider.Open(socketprovider.Config{
			Root: config.Root, InitFile: config.InitFile, Debug: config.Debug,
		})
	case "embed":
		return embedprovider.Open(embedprovider.Config{
			Root: config.Root, InitFile: config.InitFile,
			RuntimePath: config.RuntimePath, Debug: config.Debug,
		})
	default:
		return nil, fmt.Errorf(
			"unknown AGENT99_PROVIDER_BACKEND %q (want socket or embed)", f.backend,
		)
	}
}

// Existing editor attachments always use the socket transport, independently
// from the backend selected for newly owned headless workspaces.
func (configuredProviderFactory) Attach(root, endpoint string) provider.Provider {
	return socketprovider.Attach(root, endpoint)
}

func (f configuredProviderFactory) FindForeign(root string) (string, int) {
	if f.backend == "embed" {
		return embedprovider.FindForeign(root)
	}
	return socketprovider.FindForeign(root)
}

func (configuredProviderFactory) SweepStale() {
	socketprovider.SweepStale()
}

func shippedRuntimePath() string {
	if configured := strings.TrimSpace(os.Getenv("AGENT99_RUNTIME_PATH")); configured != "" {
		return configured
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		candidates = append(candidates, dir, filepath.Dir(dir))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "lua", "agent99", "rpc.lua")); err == nil {
			return candidate
		}
	}
	return ""
}

var referenceProviders providerFactory = configuredProviderFactory{
	backend: strings.TrimSpace(os.Getenv("AGENT99_PROVIDER_BACKEND")),
}
