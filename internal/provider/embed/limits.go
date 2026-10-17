package embed

import (
	"context"
	"fmt"
)

// HoldLanguageServers pins clients when a canonical provider is adopted by a
// debugger. The setting also follows automatic generation replacement.
func (b *Backend) HoldLanguageServers(ctx context.Context) error {
	b.mu.Lock()
	b.config.PinLanguageServers = true
	limit := b.config.MaxLanguageServers
	g := b.generation
	b.mu.Unlock()
	if g == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- g.nvim.ExecLua(fmt.Sprintf("require('huyang.limits').setup(%d, true)", limit), nil)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-g.done:
		return fmt.Errorf("provider exited while pinning language servers")
	}
}
