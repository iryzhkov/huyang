package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"agent99/internal/provider"
)

var providerRequestSeq atomic.Uint64

var providerToolTimeouts = map[string]time.Duration{
	"install_language": 15 * time.Minute,
	"install_debugger": 15 * time.Minute,
	"check_project":    10 * time.Minute,
	"run_tests":        15 * time.Minute,
	"debug_launch":     5 * time.Minute,
	"debug_attach":     5 * time.Minute,
	"debug_continue":   5 * time.Minute,
	"debug_step":       5 * time.Minute,
	"debug_wait":       5 * time.Minute,
	"debug_stop":       5 * time.Minute,
}

func envSocket() string {
	if endpoint := os.Getenv("AGENT99_NVIM"); endpoint != "" {
		return endpoint
	}
	return os.Getenv("NVIM")
}

func providerCall(ses session, operation string, arguments map[string]any) (any, error) {
	if ses.Provider == nil || ses.Providers == nil {
		return nil, errors.New("no Neovim to talk to: call open_workspace(root) first, " +
			"or launch the bridge with $AGENT99_NVIM (or $NVIM) pointing at a running Neovim")
	}
	route, err := ses.Providers.Route(provider.CapabilityExecute, "")
	if err != nil {
		return nil, err
	}
	if route.Primary.Descriptor().ID != ses.Provider.Descriptor().ID {
		return nil, fmt.Errorf("analysis profile primary %q does not match routed provider %q",
			route.Primary.Descriptor().ID, ses.Provider.Descriptor().ID)
	}

	withClient := make(map[string]any, len(arguments)+1)
	for key, value := range arguments {
		withClient[key] = value
	}
	if _, ok := withClient["client"]; !ok && ses.Client != "" {
		withClient["client"] = ses.Client
	}

	timeout := 60 * time.Second
	if configured, ok := providerToolTimeouts[operation]; ok {
		timeout = configured
	}
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	result, err := route.Primary.Call(ctx, provider.Request{
		Context: provider.RequestContext{
			RequestID:    fmt.Sprintf("legacy_%d", providerRequestSeq.Add(1)),
			Actor:        ses.Client,
			Deadline:     deadline,
			Cancellation: provider.CancellationUnsupported,
		},
		Operation: operation,
		Arguments: withClient,
	})
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}
