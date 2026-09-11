# Huyang S03 embedded-provider decision record

Captured: 2026-09-10
Stage: S03 — Embedded provider decision spike
Starting commit: `943dde3705d855f82b4d3e682fd1e2fbbe49f475`

## Decision

Use `github.com/neovim/go-client/nvim` v1.2.1, tag commit
`37f6413db894a93ee7aa43ceafe771a8c13e9222`, as Huyang's pinned MessagePack-RPC
implementation. S04 should own the Neovim child process and connect its stdin/stdout through
`nvim.New`; it should not use `nvim.NewChildProcess` as the production lifecycle boundary.

This is a bounded choice. The client correctly multiplexes concurrent calls, services inbound
requests while a Go call awaits its response, accepts unsolicited notifications, and survives
responses/completion notifications arriving in a different order from submission. The low-level
constructor leaves process lifetime, stderr draining, bounded stderr retention, parent-death
behavior, health, cancellation and restart policy with Huyang, matching the provider boundary
established in S02.

No custom MessagePack layer is justified by the spike.

## Compatibility exercise

The hermetic test-only spike package started an executable selected by `HUYANG_NVIM` with:

```text
--clean --embed --headless -u NONE -n
```

The complete suite passed against these binaries:

| Neovim | Source used for the run | Result |
| --- | --- | --- |
| 0.11.4 | official `nvim-linux-x86_64.tar.gz` release archive, temporary extraction | pass |
| 0.12.1 | official `nvim-linux-x86_64.tar.gz` release archive, temporary extraction | pass |
| 0.12.5 | host `/usr/bin/nvim` | pass |

The tests prove:

- `v:vim_did_enter == 1` without attaching a UI;
- `nvim_set_client_info` accepts Huyang as an `embedder`;
- a registered Go handler answers a blocking Neovim `rpcrequest`;
- registered handlers receive unsolicited `rpcnotify` messages;
- two timer-driven completion notifications may arrive fast-before-slow;
- terminating the provider on cancellation unblocks an in-flight call;
- unexpected provider death unblocks multiple in-flight calls;
- stderr is drained concurrently and retained in a 32 KiB tail.

The package contains tests only. It is disposable evidence, not an embedded backend.

## Cancellation and lifecycle finding

v1.2.1 documents `Nvim` as safe for concurrent use, but its request methods do not accept a
`context.Context`. `NewChildProcess` accepts a process context, starts `Serve` internally,
and does not expose a stderr sink or the child command. That convenience constructor is too
opaque for Huyang's provider contract.

S04 must therefore:

1. start `nvim --embed --headless` itself;
2. attach `nvim.New(stdout, stdin, stdin, logf)` and run `Serve`;
3. drain stderr from process start into a bounded tail;
4. use request-specific Lua cancellation when the kernel can acknowledge it;
5. otherwise terminate the provider, fail every in-flight request explicitly, increment the
   provider epoch, and restart before accepting more work.

Provider termination is a truthful bounded fallback for cancellation, not proof that an
individual Lua coroutine was cancelled in place.

## Measurements

Each startup median is seven fresh processes. Each warm-call median is 31 calls against one
warm process. Socket calls intentionally include the current reference backend's
`nvim --server --remote-expr` helper-process cost. RSS is one Linux `VmRSS` sample after
warmup, so it is an observation rather than a budget.

| Neovim | Embed startup | Socket startup | Embed warm call | Socket warm call | Embed RSS | Socket RSS |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0.11.4 | 6.871 ms | 7.529 ms | 0.096 ms | 2.870 ms | 13,640 KiB | 13,052 KiB |
| 0.12.1 | 6.984 ms | 8.033 ms | 0.100 ms | 2.983 ms | 13,800 KiB | 13,180 KiB |
| 0.12.5 | 7.632 ms | 8.501 ms | 0.094 ms | 3.661 ms | 13,608 KiB | 12,572 KiB |

The direct transport overhead is comfortably below the provisional 10 ms p95 budget in these
bounded local samples, while eliminating roughly 2.8–3.6 ms of helper-process overhead per
trivial warm call. Embed RSS was 588–1,036 KiB higher in the three single samples. No stderr
was emitted during ordinary calls; the explicit 64 KiB stderr flood was drained without
blocking and retention stayed at 32 KiB.

## Reproduction

The spike source (formerly `internal/provider/embedspike`) was removed after this decision
record was committed; the commands below are kept as a description of how it was run.

Run the normal host test:

```sh
go test ./internal/provider/embedspike -count=1 -v
```

Run a released binary and the bounded comparison:

```sh
HUYANG_NVIM=/path/to/nvim go test ./internal/provider/embedspike -count=1 -v
HUYANG_NVIM=/path/to/nvim HUYANG_SPIKE_MEASURE=1 \
  go test ./internal/provider/embedspike -run TestMeasureTransports -count=1 -v
```

Release archives were extracted only under `/tmp`; no downloaded binary or measurement
output is tracked, installed or deployed.

## S04 constraints and residual risks

- Keep the S02 socket backend selectable as the parity oracle and rollback path.
- Register handlers before bootstrap code can send requests or notifications.
- Dispatch completion by request ID; submission order is not completion order.
- Bound stderr independently from RPC stdout and include its tail in startup/death evidence.
- Treat any `Serve` return as provider death and fail all pending requests exactly once.
- Do not claim in-place cancellation until the Lua kernel acknowledges a request-specific
  cancellation protocol.
- The tests exercise the supported 0.11–0.12 range but do not load user configuration, LSP,
  DAP, or the agent99 Lua runtime. Those parity and fault tests belong only to S04.
