package bridge

import (
	"agent99/internal/provider"
	socketprovider "agent99/internal/provider/socket"
)

type providerOpenConfig struct {
	Root     string
	InitFile string
	Debug    bool
}

type providerFactory interface {
	Open(providerOpenConfig) (provider.Provider, error)
	Attach(root, endpoint string) provider.Provider
	FindForeign(root string) (endpoint string, processID int)
	SweepStale()
}

type socketProviderFactory struct{}

func (socketProviderFactory) Open(config providerOpenConfig) (provider.Provider, error) {
	return socketprovider.Open(socketprovider.Config{
		Root:     config.Root,
		InitFile: config.InitFile,
		Debug:    config.Debug,
	})
}

func (socketProviderFactory) Attach(root, endpoint string) provider.Provider {
	return socketprovider.Attach(root, endpoint)
}

func (socketProviderFactory) FindForeign(root string) (string, int) {
	return socketprovider.FindForeign(root)
}

func (socketProviderFactory) SweepStale() {
	socketprovider.SweepStale()
}

var referenceProviders providerFactory = socketProviderFactory{}
