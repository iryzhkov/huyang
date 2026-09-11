package service

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestServiceExposesPprofBehindBearerOnLoopback(t *testing.T) {
	stateDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "control.sock")
	if _, err := newHuyangService(serviceConfig{
		SocketPath: socketPath, StateDir: stateDir, PprofAddress: "0.0.0.0:0",
		ProviderQuota: 1, ExternalJobQuota: 1,
	}); err == nil {
		t.Fatal("non-loopback pprof address was accepted")
	}
	service, err := newHuyangService(serviceConfig{
		SocketPath: socketPath, StateDir: stateDir, PprofAddress: "127.0.0.1:0",
		ProviderQuota: 1, ExternalJobQuota: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Serve(ctx) }()
	defer func() {
		cancel()
		service.Close()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	}()
	url := "http://" + service.pprofListener.Addr().String() + "/debug/pprof/heap?debug=1"
	unauthenticated, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pprof status = %d", unauthenticated.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+service.httpToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated pprof status = %d", response.StatusCode)
	}
}
