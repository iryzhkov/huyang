# Huyang S16 repository formatting and verification pipeline

Captured: 2026-09-10
Stage: S16 — Repository formatting and verification pipeline
Starting commit: `5c08a65d383d7393c9e25125837d04edf2836834`

## Scope and result

S16 adds the repository-command layer on top of S15's exact isolated sandbox. Project
declarations come from `.huyang.toml`; command execution is disabled until the resolved
workspace root is covered by an explicit root in the user policy at
`$XDG_CONFIG_HOME/huyang/config.toml` (or the corresponding default XDG path).
`workspace_inspect` returns the effective policy, its sources, trust decision, and effective
resource ceilings.

Preparation remains byte-preserving by default. Operations may select `exact`,
`syntax_anchor`, or `formatter` indentation. Syntax anchoring requires a semantic-node handle,
preserves the node's existing leading whitespace, and refuses mixed/ambiguous indentation.
Formatter mode explicitly requests the configured transform. A trusted checked-in
`format.mode = "transform"` is the other visible way to authorize it. Discovery alone never
runs a command.

The supported project policy is versioned:

- `[format]` selects `mode = "off" | "check" | "transform"` and
  `scope = "declared" | "whole_repository"`.
- `[format.gate]` and `[format.transform]` each declare a command argv and
  `declared_writes`.
- `[[check]]` and `[[tests]]` declare command argv entries and any declared writes.
- `[resource]` declares timeout, output, snapshot-byte, and changed-file ceilings.
- User `[trust].roots` authorizes repository commands. User resource values can only tighten
  project/default ceilings.

Commands receive a sanitized environment, run with the sandbox tree as cwd, and carry
`HUYANG_SANDBOX=1`. No shell is added implicitly: shell behavior exists only when the
configured argv explicitly names a shell.

## Pipeline and guarantees

Preparation applies the declared operations, runs an explicitly authorized formatter transform,
then the configured non-mutating format gate, parser checks, static checks, and full tests against
the exact sandbox bytes. Diagnostics remain explicitly unavailable until S17. Affected-test
selection remains explicitly unavailable until S18; `test_scope = "full"` runs configured test
commands.

Every command is bracketed by bounded whole-tree snapshots. The resulting file/mode/content
changes become structured `tool_delta` entries. Declared-file formatter scope replaces configured
write globs with the exact operation file set. Whole-repository transforms may change only their
configured write set. Accepted transform changes are promoted into the prepared write request,
including files outside the original operation set, so the prepared revision, commit journal, and
canonical apply use exactly the bytes shown in `tool_delta`. The prepared revision hashes every
prepared postimage.

Any undeclared write, non-mutating-stage write, unsupported filesystem object, snapshot/change
quota breach, cancellation, timeout, non-zero command, protected Go string/comment literal
rewrite, or second-pass formatter change fails preparation and restores the exact pre-command
sandbox tree. Canonical bytes are never command cwd or rollback targets. Formatter scope is
reported as either the declared file list or whole repository.

`verify_run` now implements the frozen `revision_or_transaction` schema and runs selected
`format_gate`, `parser`, `diagnostics`, `check`, and `tests` stages against a live prepared
sandbox or an exact temporary snapshot of the current canonical revision. Stale canonical
revisions conflict instead of verifying different bytes. Its stateful idempotency path replays
the result without duplicate work.

## Verification coverage

Focused workspace tests cover layered trust, resource tightening, exact transform deltas,
line-ending classification, undeclared create/modify rollback, protected literal rollback,
formatter nondeterminism, mutating format-gate rejection, parser failure against prepared bytes,
timeouts, and exact syntax-anchor indentation/refusal.

Official SDK/provider coverage proves that trusted project policy is visible, an operation-level
formatter request transforms prepared bytes, configured check/test commands observe those bytes,
`verify_run` is idempotent, whole-repository tool changes are retained in `tool_delta`, and apply
commits both declared-operation and formatter-produced postimages.

## Decisions and residual risks

- The in-memory rollback snapshot defaults to 64 MiB and refuses larger trees before launching a
  command. This bounds recovery memory; repositories needing a larger verified command surface
  must explicitly configure a higher project ceiling that user policy may still tighten.
- Parser coverage in S16 is exact UTF-8 plus built-in Go and JSON parsing. Other parser/provider
  coverage is reported by later authoritative evidence work rather than overstated.
- Protected-literal comparison is currently syntax-aware for Go formatter transforms. Other
  languages still receive exact `tool_delta`, scope, idempotency, and rollback enforcement, but
  do not claim syntax-aware literal protection.
- `scope = "declared"` is enforced at file granularity. The complete within-file delta is exposed;
  a future language adapter may tighten this to a parser-declared syntactic envelope.
- Detailed durable evidence paging, producer provenance, authoritative diagnostic barriers, and
  culprit attribution are S17. Impact graphs, affected-test selection, and project variants are
  S18.
- No installation, deployment, installed-plugin update, live MCP restart, live configuration or
  state mutation, push, or pull request is part of S16.
