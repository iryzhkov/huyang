# Huyang S07 official MCP SDK and direct-mode report

## Scope and dependency review

S07 started from `7295ccff28ba3db2b2e04b4233cd6d77f58a5e49`.
The official `github.com/modelcontextprotocol/go-sdk` release list and release notes were
reviewed on 2026-09-10. Version `v1.7.0` is the newest stable release, was published
2026-07-27, supports MCP `2026-07-28`, and retains the older protocol revisions used by
the legacy adapter. The next releases visible from the module proxy were prereleases, so the
implementation pins `v1.7.0`.

The handwritten JSON-lines scanner is gone. Both the unchanged legacy catalog and the modern
direct profiles now run through the SDK's stdio transport, discovery/initialization,
negotiation, request matching, and cancellation plumbing. The direct process advertises no
Tasks extension: it has no resumable long-running operation, so Tasks remain capability
gated rather than being accepted without negotiation. S08 owns the shared service and
scheduler.

## Registry and direct behavior

One registry generates the frozen profiles:

| Profile | Tools |
| --- | ---: |
| `full` | 17 |
| `orient` | 8 |
| `edit` | 13 |
| `debug` | 12 |

All tools have closed draft-2020-12 input schemas, an explicit structured output schema,
MCP annotations, structured content, and equivalent text fallback. Low-level SDK handlers
validate required, unknown, typed, enum, array, nested-object, and `oneOf` arguments.
Application failures use stable Huyang envelopes; malformed protocol arguments remain MCP
errors. Stateful direct calls require an idempotency key and replay the same result for the
same canonical request while rejecting key reuse with different arguments.

The direct native path implements document/project open, bounded inspect/map, literal/regex
search, exact reads, and guarded replace-range preview/apply. Capabilities reserved for
later stages remain discoverable in their frozen profiles and return
`capability_not_implemented` rather than silently changing the catalog.

## Catalog budget

Measured by `TestModernCatalogTokenBudgets` using compact JSON for each SDK tool object and
`cl100k_base` from `github.com/pkoukk/tiktoken-go v0.1.8`:

| Profile | Bytes | Pretty lines | Tokens |
| --- | ---: | ---: | ---: |
| `full` | 40,758 | 2,805 | 8,935 |
| `orient` | 17,297 | 1,200 | 3,780 |
| `edit` | 30,161 | 2,074 | 6,597 |
| `debug` | 27,894 | 1,931 | 6,118 |

The full modern catalog is 8,968 bytes (18.0%) below the S00 ordinary legacy
`tools/list` baseline of 49,726 bytes. Full-profile per-tool measurements:

| Tool | Bytes | Tokens |
| --- | ---: | ---: |
| change_plan | 1,865 | 412 |
| code_actions | 3,287 | 723 |
| debug_breakpoints | 3,485 | 769 |
| debug_control | 3,513 | 778 |
| debug_inspect | 1,800 | 393 |
| debug_session | 1,795 | 398 |
| diagnostics | 1,693 | 367 |
| edit_apply | 3,919 | 856 |
| evidence_get | 1,767 | 387 |
| navigate | 3,439 | 749 |
| read | 3,353 | 739 |
| revision_diff | 1,809 | 390 |
| search | 1,771 | 386 |
| symbol_find | 1,755 | 379 |
| verify_run | 1,979 | 436 |
| workspace_inspect | 1,717 | 374 |
| workspace_open | 1,793 | 397 |

## Conformance and client matrix

The official Go SDK in-memory client negotiated `2026-07-28` against every profile and
exercised listing, structured/text results, native read/edit, malformed and 1 MiB requests,
32 concurrent calls matched to their request/workspace, cancellation, schema determinism,
and idempotent replay/conflict behavior.

MCP Inspector CLI `2.6.0` ran `tools/list --strict` over stdio against the edit profile.
It reported zero portability errors. The initial 13 warnings identified an unconstrained
`data` output field; that field was made an explicit open object before final verification.

The same isolated document task—open `note.txt`, search `beta`, guarded replace with
`gamma`, search/read the result, and report `MATRIX PASS`—passed through:

| Client path | Observed version | Result |
| --- | --- | --- |
| T3 MCP integration, Claude provider | T3 `0.0.38` | PASS |
| Codex CLI / T3 Codex provider path | Codex CLI `0.151.0` | PASS |
| Claude Code | `2.1.259` | PASS |
| OpenCode | `1.18.27` | PASS |
| Official Go SDK client | Go SDK `v1.7.0` | PASS |
| Official MCP Inspector CLI | `2.6.0` | PASS |

The T3 run used a separate `--base-dir /tmp/huyang-t3-matrix`, a temporary project, and
project-local MCP configuration. No deployed T3, Codex, Claude, OpenCode, Neovim plugin,
MCP service, or user configuration/state was changed. Codex required its explicit
unattended bypass for a tool correctly annotated destructive. OpenCode's local default
model mishandled nested ranges; rerunning the identical client path with its available
Nemotron model passed, confirming the client transport and generated schema work.

## Risks carried to S08

- Direct mode is intentionally process-local; its workspace registry and idempotency cache do
  not survive adapter exit. S08 adds the service boundary and durable scheduling.
- Most semantic, verification, evidence, and debug handlers intentionally remain stable
  `unavailable` stubs until their named stages.
- Tasks are not advertised because direct S07 has no task-capable operation. A future task
  implementation must add the negotiated capability before accepting task requests.
- The catalog budget measures SDK tool objects, not the surrounding JSON-RPC envelope, using
  the recorded tokenizer/version so later stages can reproduce the comparison.
