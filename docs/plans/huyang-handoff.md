# Huyang implementation handoff

## Authority and invariant

- Repository: `https://github.com/iryzhkov/agent99`
- Standalone checkout: `/home/igor/Work/huyang`
- Branch: `feature/huyang`
- Reviewed base: `1c5302efe9ca51e1701e73df72d903f225fefe04`
- Base ancestry check passed with
  `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`.
- No install, deployment, installed-plugin update, live MCP restart, live
  configuration/state mutation, push, or pull request is authorized.

## Current checkpoint

- Completed stage: S10 — Transaction intent, validation and preview.
- Starting commit: `9dac2aa6d3fb0243ff6da4405459ec378f1a3154`.
- Reconciliation fetched origin and confirmed a clean `feature/huyang` branch, the reviewed
  base in branch history, S09H committed with its focused tests/artifact, and S10 as exactly
  the first incomplete checklist stage.
- S10 exit gates are satisfied: create/edit/inspect/preview/discard lifecycle records are
  durable; all operation kinds normalize through one schema and safe topological ordering;
  `delete_symbol` predicts exact deletion; complete stale vectors report every conflict;
  retries are durable and idempotent; previews survive service restart deterministically;
  canonical bytes and provider buffers remain untouched.
- Only S10 is newly marked complete in the committed checklist.
- Exact next stage: S11 — Exclusive provider prepare.

## Predecessor and stage artifacts

These tracked artifacts were read completely before implementation:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/huyang-s01-package-boundary.md`
- `docs/plans/huyang-s02-provider-seam.md`
- `docs/plans/huyang-s03-embedded-provider-spike.md`
- `docs/plans/huyang-s04-embedded-provider.md`
- `docs/plans/huyang-s05-workspaces-revisions.md`
- `docs/plans/huyang-s06-text-core-documents.md`
- `docs/plans/huyang-s07-mcp-sdk-direct.md`
- `docs/plans/huyang-s08-service-scheduler.md`
- `docs/plans/huyang-s09-semantic-handles.md`
- `docs/plans/huyang-s09h-git-provenance.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

S10 adds:

- `docs/plans/huyang-s10-transaction-intent.md`

## S10 changes

- Added versioned, durable per-workspace plan state with opaque IDs, monotonic revisions,
  stable operation IDs, OPEN/PREVIEWED/DISCARDED states, and create/edit/inspect/preview/discard events.
- Normalized the frozen twelve-kind operation union. Opaque handles and human symbol locators
  become durable range locators; `delete_symbol` becomes an exact deletion.
- Added explicit and derived dependency ordering, including descending same-document range
  edits and safe create/edit/move/delete sequencing.
- Added in-memory predicted preview for native range/symbol and file operations. Full-vector
  validation accumulates every stale conflict and emits no partial diff on failure.
- Added deterministic preview revisions, exact-byte per-file diffs, service-restart restore,
  and durable idempotent retry behavior without touching canonical bytes or provider buffers.
- Expanded the official-SDK `change_plan` schema into closed action branches and implemented
  create/edit/preview/inspect/discard. Prepare/apply remain explicit later-stage outcomes; no
  S11 lease or provider staging was introduced.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/workspace ./internal/bridge -count=1` — exit 0:
  `ok agent99/internal/workspace` and `ok agent99/internal/bridge`.
- The focused race run covers deterministic persisted preview, safe operation ordering,
  delete-symbol normalization, every-conflict stale validation, lifecycle persistence,
  dependency refusal, official-SDK plan actions, service restart, and durable idempotency.
- `make smoke` — exit 0; built both binaries and reported `unit_edit: OK`,
  `unit_testrun: OK`, `unit_check: OK`, `unit_index: OK`, `headless: OK`,
  `multi-workspace: OK`, `debug: OK`, and `smoke: OK`.
- `go test ./...` — exit 0; all command, bridge, provider, and workspace packages passed.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0 before final documentation; rerun after this handoff update
  and before commit.

## Decisions and risks

- Plan IDs are random service capabilities; plan revisions are monotonic and required for
  edit, preview, inspect, and discard so callers cannot act on a different intent revision.
- Persisted normalized range locators let previews survive service restart without pretending
  that S09's in-memory opaque handle registry itself survived.
- Preview ordering uses explicit dependencies plus safe derived filesystem/range constraints.
  Independent operations retain intent order; cycles fail closed.
- Full-vector validation is atomic in result semantics: conflicts from every operation are
  returned together and no partial diff is published. Simulation exists only in memory.
- Provider-native rename, move-symbol, frozen-match, and code-action intent can be recorded,
  but preview reports `operation_requires_later_stage` until their named resolvers ship.
- Plan state files contain declared intent and predicted exact bytes. They are mode 0600,
  versioned, durably replaced, and local service state; migration remains a future concern.
- Existing language-server diagnostics on unrelated Python test harness code remain
  pre-existing; changed Go files have no errors or warnings and all build/vet/test gates pass.
- No install, deployment, installed-plugin update, live MCP restart, live
  configuration/state mutation, push, or pull request occurred.
- Exact next stage: S11 — Exclusive provider prepare.
