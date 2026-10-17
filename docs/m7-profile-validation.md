# M7 core profile validation and generated artifacts

Source preparation is based on accepted M6 commit
`d2766d04fd8ee55827fc9795cd4b30fdbb3a8d53`, atop merged M5 main `dc90bf5`,
including M2 client paths/listings, M3 immutable wire correlation/capabilities, the M6
diagnostic contract, generated M6 catalogs and registry-derived transport assertions.
Only the M7 commit is replayed above that base; the integration owner later cherry-picks
it onto actual merged M6 main and runs runtime checks on Chaldea. No build, test,
fixture generation, installation or service restart was run by the M7 source worker.

## Chaldea job payload: generate and measure

Run from the exact source snapshot's repository root. Use the fleet's pinned private
Neovim environment for service checks, disable friction, and choose a short canonical
disk scratch path (not host `/tmp`). The following is the command payload to submit
through Chaldea; it is not a request to run it on the workstation.

```sh
set -eu
umask 022
m7_scratch="$HOME/.cache/huyang-friction/m7-core"
mkdir -p "$m7_scratch/tmp" "$m7_scratch/artifacts/catalogs"
m7_scratch="$(cd "$m7_scratch" && pwd -P)"
export TMPDIR="$m7_scratch/tmp"
export HUYANG_FRICTION=0
# Set this to the exact snapshot SHA supplied to the job (jobs may lack .git).
: "${HUYANG_CATALOG_SOURCE_REVISION:?set exact source commit SHA}"
export HUYANG_CATALOG_SOURCE_REVISION

go test ./internal/mcpapi -count=1 -run '^TestFrozenCatalogIsUnchanged$' \
  -args -update -catalog-fixture-dir "$m7_scratch/artifacts/catalogs"

go test ./internal/mcpapi -count=1 \
  -run '^(TestEditIsExactFullAlias|TestLeanSchemasAreClosedOverrides|TestLeanValidationRefusesFullOnlyArguments)$'

go test ./internal/service -count=1 -v \
  -run '^(TestProfileDiscoveryAndEditAliasOverUnix|TestLeanProtocolValidationAndGuardedLifecycle|TestLoopbackHTTPUsesFixedProfilesAndBearerCredential|TestHuyangMCPAdapterDefaultsToFullAndProxiesService|TestModernCatalogTokenBudgets)$' \
  -args -profile-budget-output "$m7_scratch/artifacts/catalog-token-budget.json"
```

Retain logs on failure. Have the job harness archive the regular files under
`artifacts/`, even if the token ceiling fails. The generator writes these six files:

- `catalog-full.json`
- `catalog-edit.json` (identical to full)
- `catalog-orient.json`
- `catalog-lean.json`
- `catalog-experimental.json`
- `contract-schema.json` (M7 names, rules, actual per-profile input schemas and output schema)

The seventh artifact, `catalog-token-budget.json`, measures the actual SDK tools/list
tools array, compact JSON including annotations, for all five profiles, using
cl100k_base. It includes the supplied source SHA, total bytes/tokens and per-tool tokens.
The lean ceiling is 800 tokens; no local estimate is acceptance evidence. Optional
`symbol_find` is omitted to leave room for the mutation guard fields.

Use the returned bytes for guarded Huyang import into
`docs/plans/fixtures/huyang-v1alpha1/`, including the token artifact. Read each existing
destination through Huyang and overwrite with its returned revision_id; create the new
lean/token files normally. Do not invent, reformat or copy frozen bytes manually.
The fixture generator now replaces the old hand-maintained contract summaries with
actual schemas; that deliberate diff needs review.

## After integration and guarded import

Submit a fresh exact-snapshot Chaldea validation with the same scratch/friction/Neovim
setup. Include the normal fleet build checks and these commands:

```sh
go build ./cmd/huyang
go vet ./...
test -z "$(gofmt -l cmd internal)"
go test ./...
go test -race ./internal/service ./internal/handlers
```

The first phase intentionally selects the generator instead of the full mcpapi suite:
the checked-in frozen catalogs were already stale after M4–M6. The full suite remains
strict and will fail until the generated artifacts are imported. This is source
preparation, not a fixture waiver. A failed build from earlier M4–M6 dangling references
must be reported separately; do not conceal it by weakening checks or regenerating
guessed data.

## Review and integration boundaries

The implementation leaves mutation handlers, journal/revision machinery, diagnostics,
provider/LSP resource caps, and M5 trusted preparation gates untouched. The schemas
and server validation path remain paired with M2's client-path changes; empty-query
lean searches retain M2's bounded listing branch. Read-only defaults must never default
a relative mutation. M3 captures original wire arguments before aliases, normalization,
validation and root injection; its capability middleware and refusal start times remain
unchanged.

Keep `orient` (read-only discovery) and `experimental` (prepared selectors, invariants,
response controls). The execution tools were removed in M6, but those remaining
experimental features are outside M7's deletion scope. `docs/execution-contract.md`
was deleted in M6; its surviving diagnostic guarantees remain in
`docs/diagnostics-contract.md`. The live tool contract and change log are
`docs/plans/huyang-tools-v1alpha1.md`.

The separate harness worker owns OpenCode 2.0.30 V2 per-request routing:
provider ID `ollama` selects `huyang_lean`, others `huyang_full`, codemode false.
Those connections use `--profile lean|full` or the exact
`/mcp/lean|full` routes implemented here. Core code does not change harness repositories
or installed configuration. JSON envelopes remain current; M8 plain replies are separate.
