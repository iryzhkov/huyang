# M1 resource limits — 2026-10-17

Implementation branch: `t3/m1-resource-limits`, isolated at parent `6188357`
(GitHub base `ef16bef`). This report covers only roadmap M1's resource-limit item.

## Behavior

- `--max-providers` defaults to 4. Every canonical, debug and sandbox provider,
  including a start or close in progress, consumes a reservation. Zero selects the
  bounded default; negative CLI limits are refused.
- At capacity, admission evicts the least recently used idle canonical provider.
  Selection and lease checks are guarded by the slot lock. A slow close holds only
  its victim's slot; lookups of other idle providers continue.
- Leased providers remain pinned until explicit release, even after cancellation.
  Debug ownership and sandbox/plan ownership also prevent capacity eviction.
  Restart and unhealthy-provider replacement refuse to close a leased generation;
  debug ownership survives either replacement path.
- A failed launch returns its reservation. A failed close retains it. A successful
  close returns it exactly once. Pool shutdown stops admission and joins in-flight
  starts before closing all owned providers. The existing idle reaper and plan
  expiry release the same reservations.
- Evicted workspaces retain their identity. Their replacement provider epoch
  advances. Embedded generation replacement and close now join a retiring process
  tree before admitting replacement processes.
- `--max-language-servers` defaults to 4 **per provider**: at most 16 LSP clients
  in the default service pool. The per-provider bound supplies a finite aggregate
  bound without cross-provider callback coordination. Unused slots in another
  provider do not increase this limit.
- A Lua admission hook is installed before the user's headless init. It covers
  native auto-attach and direct client creation, including reentrant starts.
  Clients used by active tool requests, outstanding requests/progress, initializing
  clients and unsaved buffers are held. Transactions, sandbox providers and debug
  providers pin their clients. Adopting a canonical provider for debugging pins its
  existing clients and future generations.
- Server admission evicts an idle LRU client and waits for its exit callback.
  Stopping clients still consume capacity. Exhaustion or an unconfirmed exit
  produces `language_server_capacity`; swallowed auto-attach errors are retained
  for the affected RPC. No transaction is discarded to make room.
- Revisiting an already-loaded buffer replays attachment for its evicted server.
  Without this, a buffer would remain permanently detached because loading it
  again does not trigger FileType.

README and `contrib/systemd/huyang.service` recommend `MemoryHigh=2G` as an initial,
host-tuned budget. This is cgroup pressure/throttling, not memory-triggered LRU
eviction or an OOM guarantee. The caps bound providers and LSP clients, not their
memory use or arbitrary helper-process counts.

## Validation

All builds/tests/vet ran through `chaldea exec --host agent-a`; every Go test had
`HUYANG_FRICTION=0`. Scratch was under `~/.cache/huyang-friction/`.
Neovim v0.11.5 was downloaded only inside jobs and checked against SHA-256
`b2f91117be5b5ea39edd7297156dc2a4a8df4add6c95a90809a8df19e7ab6f52`.

Passing checks:

- `go test ./internal/providerpool ./internal/provider/embed -count=1`
  (job `09a1ee78-893e-4332-a24a-199222e3aa35`).
- `go test -race ./internal/providerpool -run 'Test(Capacity|Reap|Holds|ConcurrentLease|IdlePrepared|IdleNever|IdlePlan|PlanExpiry)' -count=1`
  (final pool revision, job `0d27f5fa-adf2-40f5-9605-80732705d248`).
- `go test ./internal/service -run 'TestService(ProcessLimits|RejectsNegativeProcessLimits)' -count=1`
  (same job).
- `go test -race ./internal/provider/embed -run 'Test(OwnedLanguageServer|ReplacementWaits|CloseWaits|ProviderDeath|CancelFallback|RootLock)' -count=1`
  (job `cdbbbd50-9d41-43d1-8d3b-ff99481c3631`).
- `nvim --clean --headless -u NONE -l tests/unit_limits.lua`
  (final Lua revision, both native-provider jobs above).
- `go vet ./...`, clean `gofmt -l cmd internal`, and `git diff --check`.

Coverage includes LRU recency, all-held exhaustion, concurrent starts, same-workspace
coalescing, launch/close failure, slow close, cancelled leases, debug retention,
prepared-plan retention/rollback, idle reaping, generation replacement, native
Neovim client callbacks, reentrant client starts, slow/missing exit callbacks,
asynchronous attach refusal, and reattachment of previously loaded buffers.

Earlier broad runs exposed agent-a's installed Neovim 0.10 and long Unix socket
paths under the required scratch prefix. A short scratch alias also changes the
canonical paths returned by Neovim, failing two existing embedded-provider path
assertions. The final full provider-package run uses the physical cache path and
private Neovim 0.11.5 and passes. Full service/live nine-language validation and
independent reviews remain with the parent integration task.

## Integration boundary

No diagnostic ledger/evidence files, `lua/huyang/edit.lua`, probe harness,
agent guide, execution contract, shared roadmap or CI workflow were changed.
No diagnostic-file interface change is required by this implementation.

No push, PR, merge, host-wide install or installed-service restart was performed.
No fleet hosts were updated; agent-a was used only for disposable validation jobs.
