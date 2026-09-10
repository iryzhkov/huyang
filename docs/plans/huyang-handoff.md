# Huyang implementation handoff

## Authority and invariant

- Repository: `https://github.com/iryzhkov/agent99`
- Standalone checkout: `/home/igor/Work/huyang`
- Branch: `feature/huyang`
- Reviewed base: `1c5302efe9ca51e1701e73df72d903f225fefe04`
- Base ancestry check: passed; `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`
- Deployment boundary: no install, deployment, live MCP restart, live configuration/state
  mutation, push, or pull request is authorized.
- Dependency protocol: clean branch, committed checklist in the implementation plan, and this
  handoff. Exactly one ungated successor is queued after a successful stage.

## Current checkpoint

- Selected stage: S01 — Mechanical package boundary
- Starting commit: `d375d70842f5519cfe1862f0d34fd7401848203f`
- Intended exit:
  - bridge internals live behind an `internal/` package boundary;
  - the compatibility binary is built from a small command entry point under `cmd/`;
  - package ownership for the MCP adapter, workspace core, and provider transport is recorded
    without introducing a provider interface or changing tool names, schemas, responses, or
    runtime behavior;
  - the S00 contract fixtures remain byte-identical;
  - package-boundary checks, `make smoke`, `go test ./...`, `go vet ./...`, and
    `git diff --check` pass;
  - one S01 commit exists and the worktree is clean.
- Status: complete pending the final S01 commit and clean-tree confirmation.
- Next stage after successful S01: S02 — Provider seam with socket reference backend

## Predecessor artifacts

S01 reconciled and consumed these S00 artifacts completely:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

The S00 commit is `d375d70842f5519cfe1862f0d34fd7401848203f`; its required gates reconcile
with the clean starting tree and current history.

## S01 changes

- Moved the complete Go bridge runtime and its same-package tests from `bridge/` to
  `internal/bridge/`, changing only the package declaration needed to make it importable.
- Exported the existing process dispatcher as `bridge.Main` and added the small
  `cmd/agent99-bridge` entry point. The built artifact and subcommands remain
  `bin/agent99-bridge <agent|mcp|tool>`.
- Updated the Makefile build path without changing the output path.
- Added `docs/plans/huyang-s01-package-boundary.md`, fixing ownership and dependency direction
  for the adapter, workspace core, provider transport, and Lua semantic kernel.
- Added no provider interface, service behavior, workspace identity, transaction behavior,
  schema, response, dependency, Lua, installation, or deployment change.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test ./internal/bridge ./cmd/agent99-bridge` — exit 0;
  `ok agent99/internal/bridge 0.008s`; the command package compiled and had no tests.
- `make build` — exit 0; built `bin/agent99-bridge` from `./cmd/agent99-bridge`.
- `go list -f '{{.ImportPath}} {{.Name}}' ./cmd/agent99-bridge ./internal/bridge` — exit 0;
  reported `agent99/cmd/agent99-bridge main` and `agent99/internal/bridge bridge`.
- `git diff --exit-code d375d70842f5519cfe1862f0d34fd7401848203f --
  docs/plans/fixtures/huyang-v1alpha1 docs/plans/huyang-tools-v1alpha1.md
  docs/plans/huyang-s00-baseline.md docs/plans/huyang-s00-model-selection.md` — exit 0; the
  frozen S00 contract, report, exercise, and fixtures are byte-identical.
- `make smoke` — exit 0; ended with `debug: OK` and `smoke: OK`. The shell also printed
  its expected background-job notice `Aborted (core dumped) echo "smoke: OK"` from the
  deliberate SIGKILL cleanup case; the suite itself returned success.
- `go test ./...` — exit 0; `ok agent99/internal/bridge (cached)`; the command package
  compiled and had no tests.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0; no output.

The final commit and clean-tree checks must be performed after this handoff is saved. The next
task must reconcile them rather than trusting this sentence.

## Decisions and risks

- The compatibility command remains named `agent99-bridge`; renaming it to `huyang` would be
  successor behavior and is outside this mechanical stage.
- `internal/bridge` deliberately remains one package in S01. Splitting interdependent files
  before the S02 provider contract would mix mechanical movement with a semantic redesign.
- `bridge.Main` is the sole exported compatibility hook. It preserves the existing
  `os.Args`, stdio, environment, exit-code, and subcommand behavior behind the thin command.
- The ownership record fixes dependency direction for S02 but does not claim the socket
  assumption has already been removed.
- Existing S00 fixture paths containing `bridge/...` are frozen example values and were not
  rewritten to follow the source move.
- The user-approved single-host serial bootstrap remains in effect. No install, deployment,
  live MCP/config/state mutation, push, or pull request occurred.
- Exact next stage: S02 — Provider seam with socket reference backend.
