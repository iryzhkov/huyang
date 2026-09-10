# Huyang S00 baseline and contract-freeze report

Captured: 2026-09-10
Stage: S00 — Baseline and contract freeze

## Repository baseline

- Upstream: `https://github.com/iryzhkov/agent99.git`
- Branch: `feature/huyang`
- Exact reviewed base: `1c5302efe9ca51e1701e73df72d903f225fefe04`
- Fetch result: `origin/HEAD` resolved to the same commit at capture time.
- Starting tree: clean.
- Source checkout: `/home/igor/Work/huyang`; the deployed/local plugin checkout was not used.
- Plan source SHA-256 before the bootstrap-only wording amendment:
  `d815e01e65f2924e4cfb61bec1839035bf990ca3c81bceea5640051dbb83d9b3`.
- Tool contract SHA-256:
  `f9fe88034eb733bf8ab0d49058cb459fbf53f4191a3d3f13266d8869fd59910d`.

The base commit is the immutable fixture for all existing structured-file, multi-workspace,
client-scoping, importer-closure, stale-edit, formatting, parser-verdict, undo, and delayed-
diagnostic tests. S00 changes documentation and contract fixtures only.

## Launch-time implementation observations

| Item | Observed value |
| --- | --- |
| Go | `go1.27.0-X:nodwarf5 linux/amd64` |
| Neovim | `NVIM v0.12.5` |
| Codex CLI | `0.151.0` |
| Claude Code | `2.1.259` |
| OpenCode | `1.18.27` |
| Current negotiated MCP revision | `2025-06-18` |
| Current ordinary catalog | 38 tools, 49,726-byte one-line `tools/list` response |
| Current debug catalog | 51 tools, 60,266-byte one-line `tools/list` response |
| Initialize latency, single local sample | 3.700 ms |
| `tools/list` latency, single warm local sample | 0.589 ms |

The latency values are environment observations, not thresholds or statistically useful
benchmarks. T3 Code exposes no standalone version command on this host, so its version is
not guessed; S07 must record it from the client integration under test.

## Frozen v1alpha1 catalog and budgets

The authoritative surface is the 17-tool `full` catalog in
`huyang-tools-v1alpha1.md`: 13 orientation/editing tools and four debugger tools. Fixed
profiles contain 17/8/13/12 tools for `full`, `orient`, `edit`, and `debug`.
`fixtures/huyang-v1alpha1/contract-schema.json` freezes names, profile membership, action
unions, operation kinds, outcomes, and shared coverage/target invariants.
`golden-results.json` freezes representative structured and compact fallback shapes.
`multi-provider.json` freezes competing, complementary, fallback, conflicting, and broken
provider behavior.

Response budgets remain validation budgets until measured in production:

- normal read summary: at most 8 KiB and 120 lines unless detail is requested;
- successful mutation/prepare summary: at most 12 KiB and 160 lines;
- primary errors, affected targets, and missing coverage are never truncated;
- detailed evidence may be paged only at semantic boundaries;
- direct embedded overhead p95: at most 10 ms excluding tool work;
- warm document open p95 without LSP: at most 300 ms;
- warm semantic reads: no more than 15% slower than the socket baseline;
- journal overhead: at most 5% of ordinary prepare-plus-apply time.

Schema token measurement is frozen as a required S07 conformance output. S00 cannot report a
truthful tokenizer count for executable modern schemas because those schemas do not exist yet;
the fixture instead freezes the exact catalog/schema decisions to generate. S07 must report
the tokenizer name/version, per-tool count, per-profile total, bytes, and lines, and compare
them with this 49,726-byte current-catalog baseline. This explicit deferred measurement avoids
inventing precision from prose.

## Contract validation result

The reviewed contract has one obvious first tool for each acceptance workflow, stable fixed
profiles, one shared mutation-operation union, explicit revision preconditions, deterministic
compact rendering, typed result lineage, evidence-bound diagnostic claims, and safe debugger
evaluation policy. The model-selection exercise found no ambiguity requiring a semantic
contract amendment. The only reviewed-plan amendments are the user-approved serial launch and
session mechanics.

No production code or architecture changed in S00.
