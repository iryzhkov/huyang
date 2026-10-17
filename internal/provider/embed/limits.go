package embed

import (
	"context"
	"fmt"
)

// DebugActive observes only the current generation; it never starts or replaces
// one. Session/trace ownership dies with that generation. Query failures retain
// the provider until activity can be established safely.
func (b *Backend) DebugActive(ctx context.Context) (bool, error) {
	b.mu.Lock()
	g := b.generation
	b.mu.Unlock()
	if g == nil || !g.alive.Load() {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	type result struct {
		active bool
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var active bool
		err := g.nvim.ExecLua(`local dap = package.loaded["huyang.dap"]; return dap ~= nil and dap.active()`, &active)
		done <- result{active, err}
	}()
	select {
	case result := <-done:
		return result.active, result.err
	case <-ctx.Done():
		return false, ctx.Err()
	case <-g.done:
		return false, fmt.Errorf("provider exited while checking debug activity")
	}
}
