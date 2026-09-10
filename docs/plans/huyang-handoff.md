# Huyang implementation handoff

## Authority and invariant

- Repository: https://github.com/iryzhkov/agent99
- Standalone checkout: /home/igor/Work/huyang
- Branch: feature/huyang
- Reviewed base: 1c5302efe9ca51e1701e73df72d903f225fefe04
- Base ancestry check passed with git merge-base --is-ancestor.
- No install, deployment, installed-plugin update, live MCP restart, live
  configuration/state mutation, push, or pull request is authorized.

## Current checkpoint

- Completed stage: S11 — Exclusive provider prepare.
- Starting commit: d7c55b2cd010456a5e7bbad1a74c96007bfa2b0c.
- Reconciliation fetched origin and confirmed a clean feature/huyang branch, the reviewed
  base in branch history, S10 committed with its focused tests/artifact, and S11 as exactly
  the first incomplete checklist stage.
- S11 exit gates are satisfied: one transaction lease protects each workspace; complete
  validated plans stage in unsaved canonical provider buffers; non-owner provider calls are
  refused; internal prepare results suppress intermediate diagnostic carry; cancellation,
  apply/state-transition failure, provider death, discard, shutdown, and restart leave no
  stale staged view; exact provider preimages are restored when the provider survives; and
  canonical disk bytes never change.
- Disk-dependent checks are explicitly unavailable in this buffer-backed stage.
- Only S11 is newly marked complete in the committed checklist.
- Exact next stage: S12 — Journaled commit.

## Predecessor and stage artifacts

These tracked artifacts were read completely before implementation:

- docs/plans/huyang-s00-baseline.md
- docs/plans/huyang-s00-model-selection.md
- docs/plans/huyang-s01-package-boundary.md
- docs/plans/huyang-s02-provider-seam.md
- docs/plans/huyang-s03-embedded-provider-spike.md
- docs/plans/huyang-s04-embedded-provider.md
- docs/plans/huyang-s05-workspaces-revisions.md
- docs/plans/huyang-s06-text-core-documents.md
- docs/plans/huyang-s07-mcp-sdk-direct.md
- docs/plans/huyang-s08-service-scheduler.md
- docs/plans/huyang-s09-semantic-handles.md
- docs/plans/huyang-s09h-git-provenance.md
- docs/plans/huyang-s10-transaction-intent.md
- docs/plans/fixtures/huyang-v1alpha1/contract-schema.json
- docs/plans/fixtures/huyang-v1alpha1/golden-results.json
- docs/plans/fixtures/huyang-v1alpha1/multi-provider.json

S11 adds:

- docs/plans/huyang-s11-exclusive-prepare.md

## S11 changes

- Added PREPARING, FAILED, PROVISIONAL, READY, ROLLING_BACK, and ROLLED_BACK plan states,
  durable preparation metadata, prepared revision IDs, and restart reconciliation.
- Added an exclusive workspace transaction lease plus owner-labelled provider access checks.
  Concurrent prepare of another plan and non-owner provider-backed calls return workspace_busy.
- Added a provider staging adapter and lazy provider lifecycle to the modern service. Initial
  modern workspace epoch 1 matches the first provider generation; unhealthy replacement
  advances the logical workspace epoch.
- Added internal Lua prepare/rollback/status operations. They validate every preimage first,
  capture exact buffer state, stage one complete batch unsaved, suppress ordinary diagnostic
  carry, and restore or discard buffers atomically on failure/rollback.
- Implemented existing-plan and inline change_plan prepare. Discard rolls prepared state back;
  apply remains the explicit S12 unavailable outcome.
- Service shutdown closes its owned providers. Restart invalidates ephemeral prepared states
  and does not replay a durable READY receipt after the provider buffers have vanished.
- Added focused coordinator, embedded-provider, official-SDK, cancellation, provider-death,
  replay, transition-failure, lease, rollback, and disk-nonmutation tests.

## Verification

Run from /home/igor/Work/huyang on 2026-09-10:

- go test -race ./internal/workspace ./internal/provider/embed ./internal/bridge -count=1
  — exit 0: all three packages passed.
- The focused race run covers two-file READY preparation/rollback, first/second partial apply
  failure, cancellation, real embedded-provider death and clean restart, durable PREPARING and
  READY transition failures, prepared-state restart invalidation, replay invalidation,
  non-owner refusal, owner-labelled inspection, and exact disk nonmutation.
- make smoke — exit 0 after the final Lua provider-death path; built both binaries and ended
  unit_edit, unit_testrun, unit_check, unit_index, headless, multi-workspace, debug, and smoke
  with OK.
- go test ./... — exit 0 before the final handoff update; rerun after this update and before
  commit.
- go vet ./... — exit 0 before the final handoff update; rerun after this update and before
  commit.
- git diff --check — exit 0 before the final handoff update; rerun after this update and
  before commit.

## Decisions and risks

- The lease belongs to the durable plan ID, not an MCP connection or actor label.
- Initial modern workspaces start at logical provider epoch 1 so lazy first-provider startup
  does not invalidate revision-bound intent. Replacing an unhealthy provider advances the
  logical workspace epoch and invalidates stale targets.
- A successful prepare keeps the lease until discard or the future S12 apply. Retried prepared
  calls return the current READY/PROVISIONAL record; distinct concurrent preparation is refused.
- Prepared revisions bind the predicted revision to the logical provider epoch.
- Provider-backed receipt replay is intentionally invalidated across service restart because
  unsaved buffers are ephemeral. Durable preview receipts remain replayable.
- Disk-based formatter/check/test execution remains unavailable and is not run against old
  canonical bytes. Repository verification and authoritative diagnostic evidence remain S16
  and S17.
- Neovim cannot represent every arbitrary mixed-newline byte stream. Provider preimage mismatch
  fails closed rather than normalizing bytes.
- Provider-native rename, move-symbol, frozen-match, and code-action intent still reports the
  S10 later-stage conflict; S11 does not absorb their resolvers.
- Existing unrelated Python language-server diagnostics remain pre-existing; all Go build,
  race, test, vet, smoke, and diff gates pass.
- No journaled apply, canonical mutation, install, deployment, installed-plugin update, live
  MCP restart, live configuration/state mutation, push, or pull request occurred.
- Exact next stage: S12 — Journaled commit.
