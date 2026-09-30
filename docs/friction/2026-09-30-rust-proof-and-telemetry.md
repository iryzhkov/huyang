# Rust diagnostic proof and feedback telemetry, 2026-09-30

A provisional edit has applied its guarded mutation but lacks complete semantic
proof. `data.canonical_changed` describes mutation; `data.verification.confidence`
and its reasons describe diagnostics. Do not repeat the mutation to obtain proof.

## Live reproduction

On gaming-pc, disposable Git worktrees contained a Cargo package and `src/lib.rs`.
The mutation replaced `pub fn value() -> u32 { 2 }` with a declaration of a new
`linked` module and a call to `linked::next()`, then created `src/linked.rs` with
that function, using one two-operation `edit_apply`, `format=false`, `verbose=true`.

- Cold: the first call named the fresh worktree with `root`. Both operations
  applied; outcome was `provisional`, reason `push_missing_current_document_proof`.
  The following full-profile `language_server_status` reported rust_analyzer
  attached. A read-only `diagnostics(full=true)` still reported provisional proof.
- Warm: `workspace_open` followed by full-profile `language_server_status` reported
  rust_analyzer attached before the same mutation. Outcome was `ok`, with no new
  diagnostics; the next read-only diagnostics call also returned `ok`.
- Independent `cargo check` passed in both worktrees. This corroborates the fixture's
  build, not the freshness of the cold edit's LSP evidence.

Full MCP request/results remain on gaming-pc at
`/tmp/huyang-feedback-transcript.jsonl`; summaries are
`/tmp/huyang-rust-linked-{cold,warm}-raw.jsonl`. These are disposable local artifacts.
The shell's mise rust-analyzer shim failed, but the actual provider was attached and
produced findings. Its executable was Mason's
`/home/igor/.local/share/nvim/mason/packages/rust-analyzer/rust-analyzer-x86_64-unknown-linux-gnu`,
version `rust-analyzer 0.3.3041-standalone`. A shell shim failure does not establish a broken provider.

The cold result is correct under the evidence model: a server attachment or a clean
build does not establish an ordered/version-matching push for the edited revision.
No confidence, stale-revision, document-version or barrier checks were weakened.
Existing workspace tests cover missing/matching version and ordered-barrier evidence.

## Recovery and scope

The mutation receipt already includes changed paths and its exact revision. Inspect
`workspace_inspect(view="status")` for provider and pipeline state; detailed
`language_server_status` requires the full profile (`huyang mcp -profile full`). Or use `verify_run` with `stages=["diagnostics"]` and
`revision_or_transaction` equal to that receipt's revision. A later revision must be
reported separately. Read `diagnostics` for the durable inbox, without repeating edits.
Repeated unavailable/provisional replies suppress repeated recovery actions, but keep
bounded machine-readable `verification.reason_codes` so the gap remains identifiable.

Diagnostics from the whole workspace can remain unavailable because other documents
have no configured server, even after an individual Rust edit has sufficient proof.
In an earlier probe that created Cargo/TOML and unrelated generated files through
Huyang, workspace diagnostics reported `lsp_not_configured` while Rust was attached.
That workspace coverage result is distinct from the edit-specific Rust verdict.

## Telemetry and response duplication

The producer emits additive `reason_codes`, `verification_confidence`, and optional
`canonical_changed`. Only fixed diagnostic identifiers and confidence enums are
admitted; arbitrary provider text becomes `diagnostic_reason_unclassified`. Source,
paths, messages and argument values are never added to these fields. Extraction
inspects selected metadata fields, without serializing source-bearing read/search data.

The installed service's verbose cold receipt returned 2,135 UTF-8 bytes in its text
JSON and the same 2,135 bytes in structured content; the warm receipt returned
1,356 bytes in each. Both channels contained the replacement source. This duplication
was observed at the MCP adapter response boundary, not inferred from agent rendering.
The source already offers experimental `response_format="text"` or `"structured"`:
text omits structured content; structured carries a brief text summary. Default dual
output preserves the existing contract. Avoid verbose/source delivery when its detail
is unnecessary; adapters should select a single supported format when available.

Acceptance: a deterministic official-MCP test keeps mutation success and provisional
proof separate on two consecutive edits, retaining reasons on the compact second
reply. Redaction tests reject paths, prose and code-shaped unknown provider strings;
metadata extraction ignores unrelated content. Full repository checks are required
before publication.
