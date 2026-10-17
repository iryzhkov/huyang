package service

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	"github.com/iryzhkov/huyang/internal/providerpool"
)

type limitFactory struct{ config providerpool.OpenConfig }

func (f *limitFactory) Open(c providerpool.OpenConfig) (provider.Provider, error) {
	f.config = c
	return &limitBackend{}, nil
}

type limitBackend struct{}

func (*limitBackend) Descriptor() provider.Descriptor { return provider.Descriptor{} }
func (*limitBackend) Health(context.Context) provider.Health {
	return provider.Health{State: provider.HealthHealthy}
}
func (*limitBackend) Call(context.Context, provider.Request) (provider.Result, error) {
	return provider.Result{}, nil
}
func (*limitBackend) Close(context.Context) error { return nil }
func (*limitBackend) Done() <-chan struct{}       { return nil }

func TestServiceProcessLimits(t *testing.T) {
	var config serviceConfig
	flags := newServeFlags(&config, io.Discard)
	if config.MaxProviders != providerpool.DefaultMaxProviders || config.MaxLanguageServers != providerpool.DefaultMaxLanguageServers {
		t.Fatalf("defaults: %+v", config)
	}
	if err := flags.Parse([]string{"--max-providers=1", "--max-language-servers=2"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config.SocketPath = filepath.Join(dir, "control.sock")
	config.StateDir = filepath.Join(dir, "state")
	factory := &limitFactory{}
	original := providerpool.DefaultFactory
	providerpool.DefaultFactory = factory
	defer func() { providerpool.DefaultFactory = original }()
	service, err := newHuyangService(config)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	pool := service.direct.handlers.ProviderPool()
	backend, err := pool.Open("one")
	if err != nil {
		t.Fatal(err)
	}
	if factory.config.MaxLanguageServers != 2 || !factory.config.PinLanguageServers {
		t.Fatalf("factory config=%+v", factory.config)
	}
	if _, err := pool.Open("two"); !errors.Is(err, providerpool.ErrProviderCapacity) {
		t.Fatalf("service cap not wired: %v", err)
	}
	if err := backend.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Open("two"); err != nil {
		t.Fatal(err)
	}
}
func TestServiceRejectsNegativeProcessLimits(t *testing.T) {
	for _, limits := range []serviceConfig{
		{MaxProviders: -1}, {MaxLanguageServers: -1},
	} {
		limits.SocketPath = "unused"
		limits.StateDir = t.TempDir()
		limits.ProviderQuota = 4
		if _, err := newHuyangService(limits); err == nil {
			t.Fatal("negative process limit accepted")
		}
	}
}
