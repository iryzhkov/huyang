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

- Completed stage: S06 — Provider-independent text core and document workspaces.
- Starting commit: `de2ef9cf10b0a3c92e624084f76c5ca035c9e324`.
- Completion commit: the `feature/huyang` commit containing this handoff.
- Predecessor reconciliation:
  - S05 is committed at `de2ef9cf10b0a3c92e624084f76c5ca035c9e324`;
  - `feature/huyang` was clean after fetching origin;
  - the reviewed base remains an ancestor;
  - the committed checklist, S00–S05 records, frozen contract fixtures, workspace/provider
    code, and recorded S05 gates agree that S06 was the first incomplete stage.
- S06 exit gates achieved:
  - document workspaces retain exact file allowlists and never walk or index siblings;
  - the provider-independent core performs bounded native orientation, literal/regex search,
    exact-byte reads, guarded previews/edits, exact diffs, and single-file recovery;
  - Markdown, JSON, TOML, YAML, Dockerfile, and parserless fixtures work under home-like and
    `/tmp` locations with optional layers absent;
  - optional parser sections plug in behind a core interface; absent/broken parsing retains
    range handles and reports `text_only` coverage;
  - workspace inspection reports stable native capabilities, unavailable optional layers,
    limits, allowlists, and bounded/sanitized environment failures without mutation;
  - focused race tests, the default full smoke matrix, embedded workspace smoke, all Go tests,
    vet, and whitespace checks pass.
- Checklist: only S06 was marked complete in this stage.
- Exact next stage: S07 — Official MCP SDK in direct mode.

## Predecessor artifacts

S06 reconciled and consumed these committed predecessor artifacts completely:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/huyang-s01-package-boundary.md`
- `docs/plans/huyang-s02-provider-seam.md`
- `docs/plans/huyang-s03-embedded-provider-spike.md`
- `docs/plans/huyang-s04-embedded-provider.md`
- `docs/plans/huyang-s05-workspaces-revisions.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

S06 adds the committed predecessor artifact for S07:

- `docs/plans/huyang-s06-text-core-documents.md`

## S06 changes

- Added configurable `Open` and one-file `OpenDocument` core entry points with project and
  exact-allowlist document workspace behavior.
- Added bounded native `filepath.WalkDir` orientation with `.git` exclusion, no symlink
  traversal, and file/byte/depth/match caps with explicit coverage.
- Added literal-default and explicit-regex text search with exact byte spans, Unicode-scalar
  display positions, document revisions, expected-content hashes, and bounded anchors.
- Added exact-byte reads and revision/hash/anchor guarded range previews/applies.
- Added revision-guarded native create/replace/delete, exact before/after diff evidence, and
  mode-preserving same-directory writes.
- Added caller-owned single-file recovery records that restore only a known postimage, clear
  an unapplied preimage, and preserve/report third-party conflicts.
- Added optional parser-section injection with a full-document range fallback and precise
  `parser_sections|text_only` coverage.
- Added stable workspace inspection for native/optional capabilities and sanitized provider or
  environment failures.
- Added `internal/workspace/text_test.go` and
  `docs/plans/huyang-s06-text-core-documents.md`; marked only S06 complete.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/workspace -count=1` — exit 0:
  `ok agent99/internal/workspace`.
- `go test ./...` — exit 0; bridge, provider, embed, embedspike, socket, and workspace
  packages passed; the command package has no tests.
- `make smoke` — exit 0 on the default socket backend; reported `unit_edit: OK`,
  `unit_testrun: OK`, `unit_check: OK`, `unit_index: OK`, `headless: OK`,
  `multi-workspace: OK`, `debug: OK`, and `smoke: OK`.
- `AGENT99_PROVIDER_BACKEND=embed bash tests/smoke.sh headless:workspace` — exit 0;
  reported `headless (workspace): OK` and `smoke (headless:workspace): OK`.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0; no output.
- `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`
  — exit 0.

## Decisions and risks

- The native path is a core API, not a modern MCP catalog. S07 owns the official SDK,
  generated frozen schemas, structured/text envelopes, annotations, negotiation, and direct
  adapter exposure.
- Exact allowlists are enforced by the same path-confinement function used by reads,
  snapshots, mutations, and recovery; cwd/root context never grants sibling access.
- Reads and edits reject binary, symlink, directory, special, over-budget, and non-allowlisted
  targets rather than following or coercing them.
- Parser integration is an injected `Sectioner`; S06 does not add a second parser runtime or
  make native text behavior depend on Neovim.
- S06 recovery is intentionally single-file and caller-state-directory scoped. It does not
  claim the durable plan lifecycle, multi-file commit protocol, kill-9 failpoint guarantee,
  or compensating undo reserved for S10–S13.
- The compact patch string is explanatory; exact before/after bytes and hashes are
  authoritative.
- Project walking skips only `.git` intrinsically. Repository ignore/config policy remains a
  later adapter/configuration concern; hard file/byte/depth/match caps prevent unbounded work.
- No install, deployment, live MCP/config/state mutation, push, or pull request occurred.
- Exact next stage: S07 — Official MCP SDK in direct mode.
