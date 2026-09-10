# S04 embedded provider production path

## Scope and result

S04 adds a production provider that owns a single `nvim --embed --headless` child and
uses the pinned `github.com/neovim/go-client/nvim` transport selected in S03. It remains
behind `AGENT99_PROVIDER_BACKEND=embed`; the default and rollback backend is
`AGENT99_PROVIDER_BACKEND=socket` (or an unset selector). An explicit
`AGENT99_NVIM` attachment always uses the socket backend.

The provider performs a version and capability handshake before becoming healthy. The Lua
kernel reports protocol version 1, the `agent99/result` completion method, restart-based
cancellation, and its supported methods. Completions carry the caller's request ID, so
concurrent results are dispatched by identity rather than submission order.

## Lifecycle contract

- Each child generation has a monotonically increasing provider epoch.
- Context cancellation or deadline expiry terminates the whole generation, fails every
  in-flight request, and completes a synchronous restart before the cancelled call returns.
  The provider advertises `provider_restart` cancellation because the pinned client has no
  context-aware per-request RPC API.
- Unexpected child death fails all in-flight calls. A later call starts a new generation and
  advances the epoch.
- Health reports backend, PID, epoch, and a stable failure code.
- Launch, bootstrap, compatibility, death, cancellation, deadline, and protocol failures are
  classified through `provider.Failure`.
- Child stderr is drained continuously and retains only the newest 32 KiB for failure
  evidence.
- On Linux a per-root `flock` under the runtime directory prevents two embedded providers
  from owning the same workspace. Lock files intentionally remain after unlock so a pathname
  cannot be rebound to a different inode while another process still holds the old lock.

The bridge exposes `provider_backend` and `provider_epoch` in the workspace-open result
while retaining the socket and PID fields used by the socket path.

## Bootstrap and runtime boundary

The bridge resolves the shipped Lua runtime from `AGENT99_RUNTIME_PATH`, the executable
location, or the standalone checkout. Bootstrap prepends that path, waits for `VimEnter`,
sets Neovim embedder client information, checks the supported Neovim range, and validates the
kernel handshake.

A production startup trace is exercised by tests. The only child argv is
`nvim --embed --headless`; the embedded backend does not start `nvim --server`,
`--remote-expr`, or `--listen` polling subprocesses.

## Tests

`internal/provider/embed/embed_test.go` covers bootstrap, health, trace evidence,
out-of-order completion dispatch, malformed completion classification, cancellation and
deadline restarts, epoch changes, concurrent failure on provider death, lazy recovery,
bounded stderr, launch classification, and root-lock exclusion/release.

The existing headless, multi-workspace, debug, and injected provider-fault smoke suites run
unchanged at their semantic boundary against both selectors. Their driver accepts either the
socket descriptor or the embedded backend descriptor. Smoke cleanup now terminates its owned
PID directly; invoking a fresh remote-expression client after a deliberately killed socket
server caused libuv to abort and was not provider behavior.

## Compatibility and rollback

S03 established transport compatibility on Neovim 0.11.4, 0.12.1, and host 0.12.5. S04's
production suite ran on host Neovim 0.12.5. The socket implementation remains selectable,
covered by the same smoke suite, and continues to advertise unsupported cancellation.

No deployment, installed-plugin update, live MCP restart, live state/configuration mutation,
push, or pull request is part of this stage.
