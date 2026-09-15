# Provider lifetime

The service checks its canonical provider pool every minute. A provider with no
outstanding leases closes after 15 minutes of inactivity, or after one minute
when its root no longer exists. Permission and I/O errors do not establish that
a root was removed. Slots remain in the pool so a concurrent lookup cannot open
an orphaned backend through a detached slot.

Canonical, Debug, Existing, Restart and Resync return a release function.
Callers defer it until their last use, even on error; release is idempotent.
Background language-server warm-up owns a separate lease acquired before the
goroutine starts. Inactivity starts at release, and the reaper skips every slot
with outstanding leases. Context cancellation is not a release: a caller can
still be blocked in transport submission or unwinding after its deadline.

Debug use pins a provider against idle reaping, including an existing canonical
provider subsequently used for debugging. This is deliberately conservative:
the pool does not infer debugger session termination from inactivity. Explicit
restart and service shutdown retain their existing forced-close semantics.
Sandbox stagers retain their separate plan lifetime.

Reopening preserves the workspace ID and advances the provider epoch past the
previous backend's last epoch. Handles and warm-up bookkeeping therefore see a
new generation. Reaping limits retention of idle processes; it is not a hard
limit on the number of simultaneously active or debug providers.

Validation:

- `go test -race ./internal/providerpool ./internal/provider/embed ./internal/handlers ./internal/service`
- `go test -tags live -race ./internal/providerpool -run TestReapRealNeovimAndReopen`
  checks real Neovim termination, recreation and a successful replacement call.
  It requires Neovim 0.11 or newer.
