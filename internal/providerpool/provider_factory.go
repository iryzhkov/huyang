package providerpool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iryzhkov/huyang/internal/provider"
	embedprovider "github.com/iryzhkov/huyang/internal/provider/embed"
)

type OpenConfig struct {
	Root        string
	InitFile    string
	RuntimePath string
	Debug       bool
	AfterEpoch  uint64
}

type Factory interface {
	Open(OpenConfig) (provider.Provider, error)
}

// ConfiguredFactory opens the embedded Neovim backend, the only
// backend that ships. The backend name is still read from the environment so
// that an explicit "embed" keeps working and anything else fails loudly.
type ConfiguredFactory struct {
	Backend string
}

func (f ConfiguredFactory) Open(config OpenConfig) (provider.Provider, error) {
	switch f.Backend {
	case "", "embed":
		return embedprovider.Open(embedprovider.Config{
			Root: config.Root, InitFile: config.InitFile,
			RuntimePath: config.RuntimePath, Debug: config.Debug, AfterEpoch: config.AfterEpoch,
		})
	default:
		return nil, fmt.Errorf(
			"unknown HUYANG_PROVIDER_BACKEND %q (want embed)", f.Backend,
		)
	}
}

func ShippedRuntimePath() string {
	for _, name := range []string{"HUYANG_RUNTIME_PATH", "AGENT99_RUNTIME_PATH"} {
		if configured := strings.TrimSpace(os.Getenv(name)); configured != "" {
			return configured
		}
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
		if _, err := os.Stat(filepath.Join(candidate, "lua", "huyang", "rpc.lua")); err == nil {
			return candidate
		}
	}
	return ""
}

func providerBackend() string {
	for _, name := range []string{"HUYANG_PROVIDER_BACKEND", "AGENT99_PROVIDER_BACKEND"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

var DefaultFactory Factory = ConfiguredFactory{Backend: providerBackend()}

func huyangHeadlessInit() string {
	if value := os.Getenv("HUYANG_HEADLESS_INIT"); value != "" {
		return value
	}
	return os.Getenv("AGENT99_HEADLESS_INIT")
}
