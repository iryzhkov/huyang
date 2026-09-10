# Huyang S08 shared service and scheduler

## Scope and result

S08 starts from `ed7171cb8ede35c5ad931077c69fd6f2c218e6e7`. It adds a standalone
`huyang` command without changing the existing `agent99-bridge` compatibility entry point.

`huyang serve` owns one long-lived modern workspace registry and listens on a private Unix
control socket. `huyang mcp --profile full|orient|edit|debug` is a byte proxy with a bounded
profile handshake; it defaults to `full` and owns no workspace, replay, or provider state.
Closing or replacing an adapter therefore leaves the service registry intact.

Optional Streamable HTTP is enabled only by `huyang serve --http <numeric-loopback:port>`.
The fixed routes `/mcp`, `/mcp/orient`, `/mcp/edit`, and `/mcp/debug` expose the frozen
17/8/13/12 catalogs. HTTP is stateless at the MCP transport boundary but uses the same
service-owned workspace registry. A persistent 256-bit bearer credential is created mode 0600
inside the service state directory; non-loopback binds and unauthenticated requests are
refused.

## Durable registry and retry boundary

The versioned registry records canonical project/document workspace definitions, stable random
workspace IDs, provider epoch, workspace state sequence, and completed stateful-request
receipts. It is written through a mode-0600 temporary file, file sync, rename, and directory
sync. Service restart reconstructs the native workspace with the same ID and last recorded
epoch/state sequence.

Repeated `workspace_open` calls for the same canonical project or exact document allowlist
reuse one workspace ID. Every later modern call still requires that explicit ID; no transport
identity, current directory, root inference, or sticky route participates in correctness.

Stateful retries are keyed by workspace, tool, and caller idempotency key. A concurrent duplicate
waits for the first call and receives its result, while different arguments under the same key
return `idempotency_key_reused`. A completed result is not released to a waiting adapter until
its receipt is included in the durable registry. Read-only calls carry no replay state and may
be repeated normally.

## Scheduler and quotas

Every modern operation is assigned one explicit class:

- `pure_read`: native inspection, search, read, evidence, and other non-mutating queries;
- `provider_read`: provider-backed debugger state calls;
- `canonical_write`: immediate canonical edits;
- `sandbox_write`: future plan preparation, serialized until S15 adds distinct sandboxes;
- `external_job`: checks and tests.

Canonical/provider work is deterministic per workspace while separate workspace lanes remain
independent. Provider-backed and external-job classes also acquire configurable service-wide
quotas (`--provider-quota` and `--external-job-quota`) with context cancellation while
queued. `workspace_inspect` reports the class roster and configured quotas. The registry has
an explicit provider-epoch synchronization hook; advancing an epoch also advances and persists
the workspace state sequence.

## Compatibility and safety

The existing `agent99-bridge mcp` direct/legacy behavior remains available and its direct
temporary state semantics are unchanged. The new service refuses to replace a non-socket at
its configured Unix path, removes only a stale socket, sets the live socket mode to 0600, and
removes it on shutdown only when it is still the inode the service created.

Modern service calls still use the S07 provider-independent text core. Semantic handles,
provider-backed modern reads, plans, verification, evidence, and debugging remain their
existing stable unavailable results until their named stages; S08 does not absorb S09 work.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0.
- `make smoke` — exit 0; both binaries built and all unit, headless, multi-workspace, and
  debugger suites ended in `smoke: OK`.
- `go test ./...` — exit 0; command packages have no tests and all bridge, provider,
  embed, embedspike, socket, and workspace packages passed.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — run after the final documentation update and recorded in the handoff.
