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

- Completed stage: S07 — Official MCP SDK in direct mode.
- Starting commit: `7295ccff28ba3db2b2e04b4233cd6d77f58a5e49`.
- Reconciliation before implementation confirmed `feature/huyang` clean after fetching
  origin, the reviewed base in history, S06 committed at the starting commit, and S07 as the
  first incomplete checklist stage.
- S07 exit gates are satisfied:
  - official Go SDK `v1.7.0` supplies direct stdio transport and MCP `2026-07-28` plus
    legacy negotiation;
  - schemas, structured/text results, annotations, stable errors, cancellation, and absent
    Tasks capability for a non-task direct server are covered;
  - one deterministic registry produces the frozen 17/8/13/12 profiles;
  - SDK/Inspector conformance, malformed/large requests, cancellation, concurrent request
    matching, direct native behavior, and the client matrix pass;
  - focused race, full smoke, Go test/vet, and diff checks pass.
- Exact next stage: S08 — Shared service and scheduler.

## Predecessor artifacts

S07 reconciled and consumed these committed predecessor artifacts completely:

- `docs/plans/huyang-s00-baseline.md`
- `docs/plans/huyang-s00-model-selection.md`
- `docs/plans/huyang-s01-package-boundary.md`
- `docs/plans/huyang-s02-provider-seam.md`
- `docs/plans/huyang-s03-embedded-provider-spike.md`
- `docs/plans/huyang-s04-embedded-provider.md`
- `docs/plans/huyang-s05-workspaces-revisions.md`
- `docs/plans/huyang-s06-text-core-documents.md`
- `docs/plans/fixtures/huyang-v1alpha1/contract-schema.json`
- `docs/plans/fixtures/huyang-v1alpha1/golden-results.json`
- `docs/plans/fixtures/huyang-v1alpha1/multi-provider.json`

S07 adds the committed predecessor artifact for S08:

- `docs/plans/huyang-s07-mcp-sdk-direct.md`

## S07 changes

- Replaced the handwritten MCP stdio scanner with official Go SDK `v1.7.0` transport,
  discovery, initialization, legacy negotiation, concurrent dispatch, and cancellation.
- Added one modern registry and deterministic `full|orient|edit|debug` presentation filters
  with the frozen 17/8/13/12 memberships.
- Added closed draft-2020-12 input schemas, explicit output schemas, annotations,
  structured-content plus text fallback, nested argument validation, and stable application
  envelopes.
- Wired modern direct mode to the S06 native core for project/document open, inspect/map,
  literal/regex search, exact reads, and guarded replace-range preview/apply.
- Added direct stateful idempotency replay/conflict handling; later-stage capabilities remain
  stable `capability_not_implemented` results.
- Propagated SDK cancellation context through legacy provider calls.
- Added official SDK client/race tests for catalogs, native workflow, schemas, large/malformed
  requests, cancellation, concurrent IDs, Tasks capability gating, and catalog budgets.
- Added `docs/plans/huyang-s07-mcp-sdk-direct.md`; marked only S07 complete.

## Verification

Run from `/home/igor/Work/huyang` on 2026-09-10:

- `go test -race ./internal/bridge -run 'TestModern|TestRequested|TestOfficial|TestSDK|TestDirect' -count=1`
  — exit 0: `ok agent99/internal/bridge`.
- `go test ./internal/bridge -run TestModernCatalogTokenBudgets -count=1 -v` — exit 0;
  recorded full/orient/edit/debug counts of 17/8/13/12 and 8,935/3,780/6,597/6,118
  `cl100k_base` tokens.
- MCP Inspector CLI `2.6.0 --method tools/list --strict` against direct edit-profile stdio
  — exit 0 with zero schema portability errors after remediation.
- Direct read/edit black-box task — `MATRIX PASS` from T3 `0.0.38`, Codex CLI `0.151.0`,
  Claude Code `2.1.259`, OpenCode `1.18.27`, and the official Go SDK client.
- `make smoke` — exit 0; reported `unit_edit: OK`, `unit_testrun: OK`,
  `unit_check: OK`, `unit_index: OK`, `headless: OK`, `multi-workspace: OK`,
  `debug: OK`, and `smoke: OK`.
- `go test ./...` — exit 0; bridge, provider, embed, embedspike, socket, and workspace
  packages passed; the command package has no tests.
- `go vet ./...` — exit 0; no output.
- `git diff --check` — exit 0; no output.
- `git merge-base --is-ancestor 1c5302efe9ca51e1701e73df72d903f225fefe04 HEAD`
  — exit 0 before the stage.

## Exact S07 client commands

The temporary fixture was `/tmp/huyang-client-matrix/note.txt` with `alpha beta\n`.
Commands below all ran from an isolated temporary directory or this checkout:

```sh
codex exec --ephemeral --ignore-user-config --ignore-rules --skip-git-repo-check \
  --dangerously-bypass-approvals-and-sandbox -C /tmp/huyang-client-matrix \
  -c 'mcp_servers.huyang.command="/home/igor/Work/huyang/bin/agent99-bridge"' \
  -c 'mcp_servers.huyang.args=["mcp","--profile","edit"]' \
  'Use only the huyang MCP tools, never shell or built-in file tools. Open /tmp/huyang-client-matrix/note.txt as a documents workspace. Search for beta. Replace exactly beta with gamma using edit_apply and a unique idempotency_key, then read the file through huyang and verify it is exactly "alpha gamma\n". End with exactly MATRIX PASS on success.'
# exit 0; MATRIX PASS

claude -p --model haiku --strict-mcp-config \
  --mcp-config /tmp/huyang-client-matrix/claude-mcp.json \
  --allowedTools 'mcp__huyang__workspace_open,mcp__huyang__search,mcp__huyang__edit_apply,mcp__huyang__read' \
  --permission-mode bypassPermissions --max-budget-usd 0.30 \
  'Use only the huyang MCP tools. Open /tmp/huyang-client-matrix/note.txt as a documents workspace. Search for beta. Replace exactly beta with gamma using edit_apply and a unique idempotency_key, then read the file through huyang and verify it is exactly "alpha gamma\n". End with exactly MATRIX PASS on success.'
# exit 0; MATRIX PASS

opencode run --pure -m opencode/nemotron-3-ultra-free --format default \
  'Use only huyang MCP tools, never shell. Open /tmp/huyang-client-matrix/note.txt as a documents workspace. Search beta. Copy the returned hit.range object verbatim. edit_apply replace_range with operation.target.file_range equal to that exact range, content gamma, and key opencode-edit-6. Search gamma, copy its range, read with target.file_range equal to the copied range. End exactly MATRIX PASS only if verified.'
# exit 0; MATRIX PASS

t3 serve --mode desktop --host 127.0.0.1 --port 43333 \
  --base-dir /tmp/huyang-t3-matrix --no-browser \
  --auto-bootstrap-project-from-cwd /tmp/huyang-client-matrix
# T3 0.0.38 isolated UI + Claude Haiku 4.5; MATRIX PASS

npx --yes @modelcontextprotocol/inspector@2.6.0 --cli \
  --config /tmp/huyang-client-matrix/claude-mcp.json --server huyang \
  --method tools/list --strict --format json
# exit 0; zero errors
```

T3's collaborative preview reported unavailable, so the isolated T3 UI was driven through a
temporary headless Chromium profile. Both isolated processes were stopped and all temporary
matrix, T3 state, and Chromium profile directories were removed afterward.

## Decisions and risks

- SDK `v1.7.0` is the newest reviewed stable release supporting MCP `2026-07-28`;
  later visible releases were prereleases and were not selected.
- The no-argument `agent99-bridge mcp` path retains the legacy catalog. S07 adds explicit
  modern profiles; S08 owns the `huyang mcp` command and full-by-default adapter.
- Direct mode deliberately does not advertise Tasks because it has no resumable long-running
  operation. A future implementation must advertise the negotiated capability before use.
- Direct workspace and idempotency state is process-local. Persistence, disconnect/reconnect,
  restart semantics, quotas, and scheduling remain S08 work.
- Most semantic, verification, evidence, and debug capabilities remain explicit stable
  unavailable results for their later named stages.
- Codex correctly required explicit mutation approval for a destructive annotation. OpenCode
  required a capable model to preserve the complete revision range; its MCP transport and
  schema path passed unchanged.
- Client testing used only temporary files and an isolated T3 `--base-dir`. No install,
  deployment, installed-plugin update, live MCP restart, live configuration/state mutation,
  push, or pull request occurred.
- Exact next stage: S08 — Shared service and scheduler.
