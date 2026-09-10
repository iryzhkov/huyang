# Huyang implementation handoff

## Authority and invariant

- Repository: `https://github.com/iryzhkov/agent99`
- Standalone checkout: `/home/igor/Work/huyang`
- Branch: `feature/huyang`
- Reviewed base: `1c5302efe9ca51e1701e73df72d903f225fefe04`
- Base ancestry check: passed with
  `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`.
- Deployment boundary: no install, deployment, installed-plugin update, live MCP restart, live
  configuration/state mutation, push, or pull request is authorized.
- Dependency protocol: clean branch, committed checklist in the implementation plan, and this
  handoff. Exactly one ungated successor is queued after a successful stage.

## Current checkpoint

- Completed stage: S08 — Shared service and scheduler.
- Starting commit: `ed7171cb8ede35c5ad931077c69fd6f2c218e6e7`.
- Reconciliation before implementation confirmed `feature/huyang` clean after fetching
  origin, the reviewed base in history, S07 committed at the starting commit, and S08 as the
  first incomplete checklist stage.
- S08 exit gates are satisfied:
  - `huyang serve` owns a durable modern workspace/idempotency registry through a private
    Unix control socket and optional authenticated loopback stateless HTTP;
  - `huyang mcp --profile full|orient|edit|debug` is a state-free proxy, defaults to
    `full`, and the fixed HTTP routes expose the frozen 17/8/13/12 catalogs;
  - persisted workspace identity, state sequence, provider epoch, and completed stateful
    receipts survive service restart;
  - explicit scheduler classes serialize same-workspace canonical/provider work, keep
    workspace lanes independent, and enforce cancellable provider/external-job quotas;
  - official SDK tests prove adapter reconnect, service restart, durable replay, provider
    epoch restart, fixed routes, loopback/auth safety, and ambiguous-route elimination;
  - focused race, full smoke, Go test/vet, and diff checks pass.
- Exact next stage: S09 — Semantic handles.

## Predecessor artifacts

S08 reconciled and consumed these committed predecessor artifacts completely:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/huyang-s01-package-boundary.md`
- `docs/plans/huyang-s02-provider-seam.md`
- `docs/plans/huyang-s03-embedded-provider-spike.md`
- `docs/plans/huyang-s04-embedded-provider.md`
- `docs/plans/huyang-s05-workspaces-revisions.md`
- `docs/plans/huyang-s06-text-core-documents.md`
- `docs/plans/huyang-s07-mcp-sdk-direct.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

S08 adds the committed predecessor artifact for S09:

- `docs/plans/huyang-s08-service-scheduler.md`

## S08 changes

- Added `cmd/huyang` and built `bin/huyang` alongside the unchanged
  `bin/agent99-bridge` compatibility binary.
- Added `huyang serve` with a mode-0600 Unix control socket and concurrent official-SDK
  sessions sharing one service registry.
- Added the thin `huyang mcp` adapter, full-by-default fixed profiles, and a bounded control
  handshake that carries no workspace ownership.
- Added optional numeric-loopback-only Streamable HTTP on `/mcp`, `/mcp/orient`,
  `/mcp/edit`, and `/mcp/debug`, protected by a durable mode-0600 bearer credential.
- Added a versioned, fsync-and-rename registry for workspace definitions, explicit IDs,
  provider epochs, state sequences, and completed idempotency receipts.
- Made repeated canonical workspace opens reuse identity and made concurrent duplicate
  stateful requests wait for the first durable result.
- Added explicit `pure_read`, `provider_read`, `canonical_write`,
  `sandbox_write`, and `external_job` scheduler classes with configurable quotas.
- Added service, adapter, restart, retry, HTTP, safety, scheduler, and restored-identity tests;
  marked only S08 complete.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0:
  `ok agent99/internal/workspace` and `ok agent99/internal/bridge`.
- The focused S08 cases inside that race run cover scheduler serialization/separation,
  provider-quota cancellation, adapter reconnect, daemon restart, durable replay, provider
  epoch persistence, full-default stdio proxying, all four HTTP catalogs, bearer enforcement,
  loopback restriction, and safe socket-path handling.
- `make smoke` — exit 0; built both `bin/agent99-bridge` and `bin/huyang`, then
  reported `unit_edit: OK`, `unit_testrun: OK`, `unit_check: OK`,
  `unit_index: OK`, `headless: OK`, `multi-workspace: OK`, `debug: OK`, and
  `smoke: OK`.
- `go test ./...` — exit 0; both command packages and all bridge, provider, embed,
  embedspike, socket, and workspace packages passed.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0 after the final documentation update; no output.
- `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`
  — exit 0 before the stage.

## S08 service interfaces

The new command surfaces are:

```sh
huyang serve [--socket PATH] [--state-dir PATH] [--http 127.0.0.1:PORT] \
  [--provider-quota N] [--external-job-quota N]
huyang mcp [--profile full|orient|edit|debug] [--socket PATH]
```

The Unix socket is the portable local adapter boundary. HTTP is off by default, accepts only
numeric loopback binds, and writes its bearer credential to `<state-dir>/http-token`.
The official SDK client tests exercise both boundaries; no service was installed or deployed.

## Decisions and risks

- The stdio adapter deliberately proxies newline-delimited MCP bytes rather than owning an SDK
  server or registry; disconnect/reconnect therefore cannot discard shared correctness state.
- The service handshake selects only one frozen modern profile. Legacy remains on the existing
  `agent99-bridge` path and no initialization field or mid-session catalog change is used.
- `workspace_open` is safe to retry because canonical project roots and exact sorted document
  allowlists reuse an existing ID. All subsequent routing remains explicit by workspace ID.
- A completed stateful result is durable before concurrent duplicates are released. A registry
  write failure is surfaced on the result and never silently advertised as persisted.
- S08 persists provider epoch transitions and supplies bounded provider scheduling, but modern
  semantic calls remain the S07 stable unavailable handlers until S09 introduces semantic
  handles. S08 does not claim provider-backed semantic coverage.
- `sandbox_write` is intentionally one workspace lane in S08; transaction-specific parallel
  sandboxes belong to S15.
- Pure native reads can run beside a write because the workspace core provides its own coherent
  locking; canonical/provider mutations remain serialized per workspace.
- No install, deployment, installed-plugin update, live MCP restart, live configuration/state
  mutation, push, or pull request occurred.
- Exact next stage: S09 — Semantic handles.
