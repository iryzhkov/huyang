# Huyang S06 provider-independent text core and document workspaces

Captured: 2026-09-10
Stage: S06 — Provider-independent text core and document workspaces
Starting commit: `de2ef9cf10b0a3c92e624084f76c5ca035c9e324`

## Scope and result

S06 extends `internal/workspace` with a native text path that does not start or invoke Git,
Neovim, ripgrep, Tree-sitter, an LSP, a formatter, or project commands. The existing legacy
bridge and both provider backends remain unchanged.

`Open` accepts project or document-workspace options. `OpenDocument` is the one-file
convenience form. A document workspace has an exact canonical allowlist; confinement rejects
every sibling even when it shares the workspace cwd. Multiple allowlisted documents use their
common directory only as context. Project workspaces use a native `filepath.WalkDir` walk
that does not follow symlinks, skips `.git`, and enforces explicit file, byte, depth, and
match limits.

## Native text behavior

The core now provides:

- bounded orientation with explicit complete/capped/skipped coverage;
- literal-by-default and explicit-regex search with byte offsets, one-based Unicode-scalar
  display coordinates, exact match hashes, bounded before/after anchors, and document
  revisions;
- exact-byte reads that preserve BOMs, CRLF, final-newline absence, tabs, trailing spaces, and
  unrelated text;
- revision/hash/anchor guarded range previews and applies;
- revision-guarded whole-file create, replace, and delete operations;
- exact before/after bytes and SHA-256 hashes plus a compact deterministic patch summary;
- optional parser section injection behind a transport-independent `Sectioner` interface,
  with a truthful `text_only` range fallback when no parser is present;
- stable inspection data naming native capabilities and unavailable optional layers.

Environment failures recorded for inspection are bounded, stripped of control characters, and
redact inline token/password/secret arguments. Inspection never installs a tool or changes
configuration.

## Recovery boundary

Native S06 mutations use a single-file recovery record in the caller-supplied state directory.
The record is mode 0600 and durable before the canonical path changes. Replacement uses a
same-directory temporary file, file sync, rename, and directory sync. Delete uses the same
precondition and journal boundary.

`Recover` has three outcomes for an incomplete record:

- the canonical path still equals the preimage: clear the unused record;
- it equals the postimage: restore the exact preimage (or remove a newly created path);
- it equals neither: retain the record and report a conflict without overwriting the
  third-party bytes.

This is deliberately not the multi-file transaction journal promised by S12/S13. It gives the
provider-independent single-file fallback a recoverable write primitive without introducing
plan state, multi-file commit, transaction ownership, or crash-failpoint claims ahead of those
stages.

## Adversarial coverage

`internal/workspace/text_test.go` covers:

- Markdown, JSON, TOML, YAML, Dockerfile, and parserless document allowlists;
- home-directory and `/tmp` standalone files without sibling discovery;
- literal/regex search and Unicode display coordinates;
- exact BOM/CRLF/whitespace preservation and mode preservation;
- preview non-mutation, exact diffs, range anchor tampering, stale revisions, and
  create/replace/delete preconditions;
- bounded project walking and `.git` exclusion;
- optional parser sections and text-only fallback;
- sanitized environment failure evidence;
- recovery after a postimage write and preservation of a conflicting third-party write.

## Compatibility and remaining work

The compatibility bridge still exposes its existing root-oriented tools and provider-backed
semantics. S07 will generate the frozen modern MCP schemas and adapt these core operations to
`workspace_open`, `workspace_inspect`, `search`, `read`, and mutation preparation.
Epoch-bound semantic handle relocation remains S09; durable plan intent is S10; provider
staging is S11; the multi-file write-ahead journal and exhaustive kill/failpoint recovery are
S12/S13.

No install, deployment, installed-plugin update, live MCP restart, live configuration/state
mutation, push, or pull request is part of S06.
