# Huyang S11 exclusive provider prepare

Captured: 2026-09-10
Stage: S11 — Exclusive provider prepare
Starting commit: d7c55b2cd010456a5e7bbad1a74c96007bfa2b0c

## Delivered scope

- change_plan(action="prepare") now accepts either an existing plan revision or the frozen
  inline-operation branch, validates the complete predicted change, and lazily opens the
  canonical Neovim provider.
- The workspace coordinator owns one explicit transaction lease. A second plan is refused
  with workspace_busy; provider-backed non-owner calls are refused while staged buffers
  exist. Debug/provider schemas accept the transaction ID needed for an owner-labelled call.
- The provider receives one complete exact-byte batch. The Lua kernel validates every provider
  preimage before applying any postimage, captures loaded/listed/modified/options/content
  state, and stages all postimages in buffers without saving.
- Internal prepare/rollback dispatch suppresses ordinary edit carry and intermediate verdict
  presentation. The preparation record explicitly marks disk-based checks unavailable and
  canonical_changed=false.
- change_plan(action="discard") rolls a prepared plan back, restores exact buffer preimages,
  removes buffers created only for staging, and releases the lease.
- Cancellation, a Lua apply failure, provider death, a durable PREPARING transition failure,
  and a durable READY transition failure all release the lease and leave no staged provider
  view. Embedded-provider death discards the process-local view and rollback restarts clean.
- Service shutdown closes owned plan providers. On service restart, durable PREPARING, READY,
  PROVISIONAL, or ROLLING_BACK records become FAILED because their unsaved buffers no longer
  exist; provider-backed READY receipts are not replayed as though that view survived.
- change_plan(action="apply") remains unavailable for S12. No canonical path is written by
  prepare or rollback.

## Verification coverage

Focused race coverage spans the workspace transaction coordinator, real embedded-provider Lua
batching, and the official-SDK MCP adapter:

- two-file READY preparation and exact rollback;
- non-owner lease refusal and owner-labelled provider inspection;
- first/second apply failure, cancellation, and provider death;
- PREPARING and READY persistence failures;
- service/provider restart invalidation;
- exact canonical-disk non-mutation;
- provider-backed replay invalidation while durable preview replay remains valid;
- closed-schema existing-plan prepare and discard paths.

The required commands and exact results are recorded in huyang-handoff.md.

## Boundaries and residual risks

- This is deliberately canonical-buffer-backed and exclusive. Same-workspace parallel prepare
  remains S15 after the S14 sandbox decision.
- Disk-requiring formatter, check, and test stages are reported unavailable. Repository
  transforms and verification belong to S16; authoritative diagnostic barriers belong to S17.
- Provider-native rename, move-symbol, frozen-match, and code-action operations retain S10's
  explicit later-stage conflict until their resolvers are implemented.
- Neovim represents text as lines plus uniform file-format options. A canonical byte stream
  that cannot be represented exactly by that model fails provider-preimage validation rather
  than being normalized or staged dishonestly.
- No journaled canonical apply, multi-file recovery, compensating undo, installation,
  deployment, live service/configuration mutation, push, or pull request is part of S11.
