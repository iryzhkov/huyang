# Huyang transactional semantic workspace: implementation plan

Status: reviewed architecture; implementation approved through the temporary serial bootstrap chain
Prepared: 2026-09-10
Planning brief: `/home/igor/Work/agent99-transactional-semantic-workspace-brief.md`
Tool contract: `docs/plans/huyang-tools-v1alpha1.md`
Authoritative repository inspected: `https://github.com/iryzhkov/agent99` `main` at `947f5b1`
Installed dogfood checkout observed at `f9a372e`; it is not the planning baseline and was not modified
Verification: full `make smoke` passed locally at `4bec1da`; upstream records it green
again at `947f5b1`

## Project identity

The standalone service is **Huyang**. Agent99 remains the Neovim plugin for an inline
coding agent and is also the source of the first reusable Lua semantic kernel. Huyang is
the workshop rather than the worker: it gives coding agents semantic knowledge, precise
tools, isolated construction space, verification and durable records while leaving the
agent responsible for designing and building the change.

## Executive decision

Build Huyang as a long-lived local Go service with two explicit boundaries:

1. A transport-independent semantic-workspace core owns workspace identity,
   concurrency, transactions, journals, verification, history, and provider lifecycle.
2. A Neovim provider owns editor state, Tree-sitter, LSP and DAP. Each provider is a
   child started as `nvim --embed --headless` and controlled over its stdin/stdout
   MessagePack-RPC channel.

Expose the service through both Streamable HTTP and a small stdio adapter. Use the
official MCP Go SDK rather than extending the current handwritten MCP loop. Serve MCP
revision `2026-07-28` and retain negotiated legacy support for the clients that have not
adopted it yet.

Do not put transaction ownership in Lua. Lua should perform semantic editor operations
and report evidence; Go should own durable state and policy. In particular, transaction
IDs, workspace epochs, client-independent ownership tokens, write-ahead journals,
sandbox directories, command execution policy, and commit/rollback belong in Go.

The initial transaction implementation should be exclusive and buffer-backed: one active
writer per canonical workspace, all declared edits staged unsaved in its Neovim provider,
with other same-workspace calls either queued or rejected explicitly. The target
implementation should add transaction sandboxes and a provider per sandbox. That is the
first design that permits genuinely parallel same-workspace writers, repository-native
formatting and tests before commit, and an uncommitted change set invisible to other
clients.

This is an evolution, not a rewrite. Keep the existing query and mutation tools as
compatibility wrappers while the new primitives prove themselves.

## Findings from the current implementation

### Current process and RPC topology

At `947f5b1`, the standalone path is already an MCP server, but the Neovim provider is a
headless socket server:

```text
MCP client
  -> agent99-bridge mcp (handwritten newline JSON-RPC, one request at a time)
     -> nvim --headless --listen <unix-socket>
        -> agent99 Lua modules
     -> a new `nvim --server ... --remote-expr` process for start and every poll
```

Relevant paths:

- `bridge/mcp.go` implements protocol negotiation, schemas, the serial stdin scanner and
  tool dispatch.
- `bridge/headless.go` owns the root-to-Neovim registry, socket naming, process startup,
  idle reaping, shutdown and cross-process duplicate-root refusal.
- `bridge/nvim.go` base64-encodes JSON, starts a Lua coroutine through
  `Agent99RpcStart`, then repeatedly launches `nvim --server --remote-expr` to poll it.
- `lua/agent99/rpc.lua` owns the pending coroutine table and JSON result encoding.
- `bridge/routing.go` infers a workspace from path arguments or a sticky root.
- `bridge/client.go` and `lua/agent99/client.lua`, added immediately before this plan,
  scope undo, baselines and diagnostic disclosure by a best-available client identity.

The latest code correctly refuses a second Neovim over the same tree, including from a
different bridge process. It cannot hand that other process's socket to the caller, so
parallel MCP processes contend rather than share a workspace.

### Current semantic core

- `lua/agent99/core.lua` loads long-lived buffers, compares a disk fingerprint
  (`mtime.ns/size`), reloads clean buffers after external changes, refuses disk/unsaved
  conflicts, writes buffers and relays filesystem changes to LSP servers.
- `lua/agent99/index.lua` merges Tree-sitter and LSP document symbols. Its cache is keyed
  by buffer changedtick. `find_symbol` resolves by ranked name path and sometimes a
  declaration line.
- `lua/agent99/edit.lua` contains mutation, import organization, opt-in LSP formatting,
  formatter damage checks, diagnostic deltas, diagnostic-version observation, progress
  vetoes, rename, file operations and symbol movement.
- `lua/agent99/edits.lua` is a guarded, per-client undo ledger. A tool's multi-file
  entries share one group, but it is still an undo stack, not a transaction journal.
- `lua/agent99/closure.lua`, also newer than the installed checkout, computes a bounded
  two-hop importer closure and asks those buffers for diagnostics. It is the right seed
  for impact analysis, but not yet a durable dependency graph.
- `lua/agent99/install.lua` and `lua/agent99/testrun.lua` discover commands, run checks
  and tests, compact output and maintain per-client baselines.
- `bridge/friction.go` records useful dogfood telemetry. Its `workspace_rev` is a cached
  Git revision, not a mutable workspace or document revision.

### Current strengths to preserve

- Small semantic reads (`workspace_tree`, `workspace_map`, `skim`, annotated `grep`,
  `find_symbol`) save substantial model context.
- Mutation targets are guarded by symbol or expected text, and stale targets are refused
  or explicitly relocated.
- Diagnostic replies distinguish new, pre-existing, fixed, late, provisional and
  unchecked results instead of treating silence as clean.
- Multi-file undo grouping, external-file resynchronization and LSP file-operation
  notifications already encode valuable failure knowledge.
- Test/check output is compact and candid about empty runs, timeouts and incomplete
  coverage.
- Tree-sitter byte-column targeting now preserves declarations that share a line, and
  post-edit parser checks prevent structured-file corruption from masquerading as a clean
  no-LSP result. Markdown setext headings and YAML sequences of mappings are indexed. These
  behaviors are part of the baseline contract, not optional polish to rediscover later.
- Diagnostic silence is tracked per server and file, stopped servers are disclosed, and
  late/pre-existing deltas no longer invent causal attribution. Huyang's evidence model must
  preserve that honesty while replacing the prose-heavy rendering.

### Gaps relative to the brief and additional requirements

| Requirement | What exists | Gap |
| --- | --- | --- |
| Direct embedded provider | Socket Neovim plus CLI polling | Persistent MessagePack-RPC client and completion notifications |
| Current MCP | Custom support through `2025-06-18`, partial `server/discover` | Final `2026-07-28` wire format, output schemas, structured content, cancellation, conformance |
| Parallel clients | Per-client ledgers inside one process; cross-process refusal | Shared service, concurrent dispatch, explicit state tokens and workspace scheduling |
| Revisions | changedtick cache, LSP versions, mtime/size fingerprint | Public workspace/document revision model and mutation preconditions |
| Stable handles | path + name path + line | Opaque handle registry with inspectable locator and safe relocation |
| Transactions | grouped edit ledger and undo | Proposal, preview, isolation, verification, commit journal and crash recovery |
| Diagnostic barrier | exact push version for edited buffer; bounded importer closure | Structured evidence per client/file, pull/workspace diagnostics and policy gates |
| Repository formatter | LSP formatter and optional lint command | Project-native transform/check stages in an isolated copy |
| Impact analysis | bounded reference closure | Durable change-set graph, test association and coverage disclosure |
| Standalone documents | Explicit directories work; parser-only tools can work | A one-file workspace that never scans/indexes the parent, and range handles without a parser |
| Structured model API | JSON serialized into one text content block | Stable output envelope plus `structuredContent`/`outputSchema`, with text fallback |

## Coherence review and sequencing corrections

The target architecture is sound, and the ownership split between Go policy/durability and
the Neovim/Lua semantic kernel matches the failure modes in the current implementation. The
review found five sequencing constraints that the implementation sessions must enforce:

1. **Upstream Git is the only baseline.** Start from a fresh, verified `main`, record its
   commit in the workflow inputs, and never develop from the installed lazy.nvim checkout.
   The plan was originally inspected at `dc8c3da`; upstream `4bec1da` adds byte-precise
   structured-file edits and parser evidence, and `947f5b1` fixes per-file diagnostic
   silence and unsupported causal attribution. All must be frozen into compatibility fixtures.
2. **Extract seams before changing behavior.** Move the Go bridge into importable packages
   and establish a tested provider interface before adding embedded RPC, a daemon, revisions
   or transactions. Mechanical package movement and semantic change must not share a commit.
3. **Identity precedes sharing.** Do not expose a multi-client daemon while modern calls
   still use sticky roots or process-derived client identity. Workspace IDs, provider epochs,
   revision tokens and explicit request ownership must exist before shared-service dogfood.
   The SDK transport may be proven earlier in isolated direct mode.
4. **Exclusive staging is deliberately incomplete.** It proves batch validation, coherent
   LSP staging and exact buffer restoration, but it cannot honestly run disk-based formatters
   or tests. It is not a default mutation path until journaled commit exists, and it is not
   the final concurrency design.
5. **Sandbox and diagnostic claims remain evidence-bound.** A copied tree must include the
   exact dirty/untracked canonical state, and a clean semantic verdict must name an
   authoritative barrier or a configured project check. Neither a reference closure nor a
   silence interval is proof of whole-project coverage.

The official MCP Go SDK `v1.7.0` is the stable baseline that first supports MCP
`2026-07-28`; later prereleases are not selected automatically. The implementation session
must pin a reviewed stable version and run the upstream conformance suite plus Huyang's own
wire fixtures. Likewise, `github.com/neovim/go-client/nvim` is a candidate behind the
provider seam, not an architectural dependency until the embedded-provider spike passes.

## What `nvim --embed` changes

Neovim's documented embed channel is MessagePack-RPC over stdin/stdout. It avoids Unix
socket discovery, `--server` helper processes, base64, JSON double encoding and polling.
The Go process also gets direct child lifecycle and cancellation control.

One startup detail is easy to miss: `nvim --embed` by itself waits for a UI to attach
before it continues through normal initialization. A disposable local probe against
Neovim 0.12.5 observed:

- `nvim --clean --embed`: `v:vim_did_enter == 0` until `nvim_ui_attach`.
- `nvim --clean --embed --headless`: `v:vim_did_enter == 1` without a UI.

Therefore use `--embed --headless`. Here, “embedded” describes the transport and process
ownership; “headless” suppresses the UI startup barrier. They are complementary, not
competing modes.

Use `github.com/neovim/go-client/nvim` behind an internal `Provider` interface for the
first implementation spike. It has `NewChildProcess`, knows Neovim's MessagePack EXT
types, and handles bidirectional RPC. Pin a reviewed commit/version. Do not hand-roll the
protocol unless the spike demonstrates a blocking incompatibility. Neovim's client
guidance requires handling inbound requests promptly and not assuming response order.

After connecting:

1. Call `nvim_set_client_info` with type `embedder`.
2. Check API compatibility from `nvim_get_api_info` and fail with a useful version error.
3. Wait for `VimEnter`/`v:vim_did_enter` in headless mode.
4. Prepend the agent99 runtime shipped with the service and call a versioned Lua
   bootstrap. Do not require the user's plugin manager to load agent99.
5. Let the user's normal Neovim configuration supply language servers, Tree-sitter and
   project conventions unless an explicit isolated init is configured.
6. Exchange a kernel capability handshake containing the Lua protocol version and
   supported operations.

Replace polling with one completion notification:

```text
Go -> nvim_exec_lua(require('agent99.rpc').start, request context)
Lua coroutine yields while Neovim/LSP work runs
Lua -> rpcnotify(channel, 'agent99/result', request_id, result_or_error)
```

Keep the current start/poll path temporarily behind the provider interface for A/B tests
and rollback during migration.

## Target topology

```text
Codex / Claude Code / OpenCode / T3
        | Streamable HTTP or stdio adapter
        v
  Huyang service (Go)
  + MCP 2026/legacy negotiation and schemas
  + workspace registry and scheduler
  + transaction manager and recovery journal
  + sandbox, formatter, checks and tests
  + result/evidence store and telemetry
        |
        +-- canonical workspace A -> nvim --embed --headless
        +-- canonical workspace B -> nvim --embed --headless
        +-- transaction sandbox A/tx1 -> nvim --embed --headless (target phase)
        +-- transaction sandbox A/tx2 -> nvim --embed --headless (bounded/optional)
```

Run the core as a user service (`huyang serve`) with a Unix-domain control socket and,
optionally, loopback Streamable HTTP. `huyang mcp` is a thin stdio adapter to that core.
This retains the universally supported stdio installation shape while all clients share
one workspace registry and provider pool. A direct stdio “single process” mode remains
useful for CI and recovery.

Do not use MCP transport connection identity as transaction ownership. MCP 2026 is
stateless and `clientInfo` is implementation metadata, not a unique actor or security
principal. A transaction's unguessable ID is its capability token; every stateful call
also names a workspace ID. Optional `huyang/actor` metadata is useful for attribution,
quotas and UI, but correctness must not depend on a host preserving it.

## Ownership boundaries

### Go service owns

- MCP versions, transports, schemas, annotations, structured/text result rendering.
- Canonical workspace and document-workspace registry.
- Client attribution, quotas, cancellation and request deadlines.
- Provider process lifecycle and restart epochs.
- Per-workspace scheduling and transaction leases.
- Transaction records, operation lists, sandboxes and write-ahead commit journals.
- Project command configuration, trust policy, formatter/check/test execution.
- Evidence/provenance storage, compact response selection and telemetry.
- Crash recovery and garbage collection.

### Neovim/Lua kernel owns

- Loaded buffers, changedticks, buffer-to-LSP document versions and LSP client state.
- Tree-sitter parsing, syntax-node ranges and parser-error discovery.
- LSP queries, workspace edits, code actions, imports and editor formatting.
- Applying a validated batch to provider buffers without saving.
- Mapping pre/post syntax nodes and reporting symbol candidates.
- Normalizing diagnostics and LSP progress into structured evidence.
- DAP integration.

### Explicitly shared contracts

- `ProviderRequestContext`: request ID, actor label, workspace ID, epoch, transaction ID,
  deadline and cancellation token.
- `DocumentSnapshot`: canonical URI, changedtick, LSP version by client, byte hash, disk
  fingerprint and dirty state.
- `SemanticLocator`: human-readable path/name/kind/range plus fingerprints.
- `ProviderResult`: structured value, touched buffers, diagnostics/evidence and provider
  health.
- A versioned internal protocol independent of the public MCP revision.

## Workspace and document model

Use two workspace kinds.

### Project workspace

`open_workspace(root=...)` retains today's behavior: scans a bounded project tree,
starts configured language servers, supports graph queries and project commands.

### Document workspace

Add `open_document(file=...)` and a general `workspace_open({files:[...]})` form. A
document workspace:

- uses the containing directory only as Neovim cwd and configuration context;
- has an explicit allowlist of files and does not walk or index the parent;
- works without `.git`, a project manifest, Tree-sitter or LSP;
- may add related files explicitly later;
- supports reads, exact-text and range edits, revision checks, preview, commit and undo;
- uses Tree-sitter headings/keys/declarations when a parser exists;
- reports `semantic_coverage: text_only` rather than urging the agent to install an LSP.

This is the correct path for one Markdown note, JSON/TOML/YAML configuration, a Dockerfile
or an unknown text format. A file with no parser still gets guarded range handles based on
content anchors; it simply does not get symbol claims.

## Identity and revision model

### Workspace identity

```text
workspace_id    ws_<random 128-bit>
workspace_kind  project | documents | transaction_sandbox
canonical_root  real path (informational, not identity)
epoch           monotonically increases whenever the canonical provider is replaced
state_seq       increases for every committed Huyang transaction or observed external change
```

Return `workspace_id`, `epoch` and `state_seq` on every result. Do not rely on root strings
or sticky routing in the new API. Legacy tools may still infer a root.

### Document revision

Use a layered revision, not changedtick alone:

```json
{
  "workspace_id": "ws_...",
  "epoch": 3,
  "uri": "file:///.../x.go",
  "state_seq": 81,
  "changedtick": 17,
  "content_sha256": "...",
  "disk": {"device": 1, "inode": 2, "size": 123, "mtime_ns": 456},
  "dirty": false,
  "lsp_versions": {"gopls#1": 17}
}
```

`content_sha256` is the authoritative optimistic-concurrency token. Changedtick is a fast
provider-local check and LSP version correlate. Filesystem metadata is a cheap external
change detector but never proof of equal content. Hash lazily, cache by metadata, and
always hash a mutation target before prepare and again before commit.

A read result may abbreviate this to a `revision_id`; `revision_get` returns the fields.

### Semantic handles

Initial handles are service-local and expire with workspace epoch or configured TTL:

```json
{
  "handle": "sym_...",
  "workspace_id": "ws_...",
  "epoch": 3,
  "document_revision": "docrev_...",
  "display": "bridge/headless.go:295 openWorkspace function",
  "locator": {
    "uri": "file:///.../bridge/headless.go",
    "language": "go",
    "kind": "function",
    "name_path": "openWorkspace",
    "parent_path": null,
    "selection_range": [295, 6, 295, 19],
    "signature_sha256": "...",
    "node_sha256": "...",
    "anchor_before_sha256": "...",
    "anchor_after_sha256": "..."
  }
}
```

Resolution order:

1. Exact document revision and syntax range.
2. Same URI, same parent/name/kind and signature fingerprint.
3. Unique candidate in the workspace with matching node/signature and compatible parent.
4. Otherwise invalidate with structured candidates and a conflict reason.

Formatting-only range movement may relocate a handle and returns both old and new
locator. A signature change, ambiguity, deletion or epoch change never silently binds it
to a different declaration. Range handles for text-only documents use expected content
hash plus bounded before/after anchors and obey the same ambiguity rule.

## Plan/transaction model

### Public operation and internal service methods

Expose one model-facing `change_plan` tool with a required discriminated action:
`create|edit|preview|prepare|inspect|apply|discard`. Each action has a closed schema branch.
The service may retain orthogonal internal begin/edit/preview/prepare/inspect/apply/discard
methods and transaction records; these are not separately advertised tools.

`prepare` accepts either an existing plan revision or an inline base revision, policy and
operation list. The inline branch atomically creates and prepares, then returns the compact
inspect/diff/diagnostic/verification summary. This keeps the known multi-file hot path to
prepare plus apply while preserving interactive plan revision when needed.

Operations include replace/insert/delete symbol, replace range, create/move/delete file,
rename, apply code action and homogeneous `replace_matches`. Search may return a frozen,
revision-bound complete result-set handle so one guarded operation can replace all original
matches without retransmitting them. A direct-query branch requires expected match/file
counts and a write budget; incomplete, stale or overlapping sets change nothing. Each
operation carries a handle or
human locator plus an explicit revision precondition. Add `delete_symbol` as a direct
operation.

Plan editing records intent; it does not mutate the canonical tree. `prepare`
validates and applies the complete set to the transaction provider, then formats and
analyzes it. Multiple operations are topologically ordered: file creations/moves,
semantic refactors, region edits, import organization, formatting.

### State machine

```text
OPEN
  -> PREVIEWED
  -> PREPARING
       -> CONFLICTED
       -> FAILED
       -> PROVISIONAL
       -> READY
  -> COMMITTING
       -> COMMITTED
       -> RECOVERY_REQUIRED

OPEN/PREVIEWED/CONFLICTED/FAILED/PROVISIONAL/READY
  -> ROLLING_BACK -> ROLLED_BACK

OPEN/PREVIEWED -> EXPIRED
```

Only `READY` commits by default. A policy may allow a named provisional dimension, but
the override and missing evidence remain in the result. Mutating an operation after
prepare returns the transaction to `OPEN` and discards its old evidence.

### Conflict reasons

Use stable codes with details:

- `workspace_epoch_changed`
- `document_content_changed`
- `document_deleted`
- `target_deleted`
- `symbol_moved`
- `symbol_signature_changed`
- `symbol_ambiguous`
- `format_only_relocation`
- `external_and_unsaved_conflict`
- `workspace_busy`
- `undeclared_tool_write`
- `commit_precondition_changed`

Validate all operations before applying any. If only one file is stale, return the full
validation vector; do not partly stage the rest.

## Isolation and concurrency

### Scheduler

The service accepts requests concurrently. Each workspace has a scheduler with these
classes:

- `pure_read`: revision/status/evidence reads; parallel in Go.
- `provider_read`: semantic calls that may load buffers; concurrently submitted only
  after Lua request-local state is audited.
- `canonical_write`: external resync, commit and legacy immediate mutation; exclusive.
- `sandbox_write`: exclusive within one transaction sandbox, parallel across sandboxes
  and workspaces subject to resource quotas.
- `external_job`: formatter/check/test in a sandbox; cancellable and bounded separately.

Neovim still has one event loop, but the direct RPC client may have several outstanding
requests. Before enabling same-provider interleaving, remove module globals that are
request state or key them by request/transaction. The current `client.lua` coroutine
context is a good starting pattern, not a complete audit.

### Phase-one exclusivity

The MVP may stage in canonical buffers, but must acquire a transaction lease. While that
lease exists:

- the owner must pass `transaction_id` to see the staged view;
- other same-workspace semantic calls wait or receive `workspace_busy` with retry data;
- no staged buffer is saved before commit;
- checks that need disk are marked unavailable, not run against the old files;
- timeout, cancellation or provider death restores exact buffer preimages.

This is correct but not parallel within a workspace. It is a stepping stone only.

### Target sandbox isolation

Create a copy-on-write transaction directory containing the exact current workspace,
including dirty and untracked files, then overlay the proposed operations. Do not use a
plain Git worktree: it omits dirty/untracked state and therefore verifies a different
workspace. Prefer filesystem reflinks when available and safe copying otherwise; never
use hardlinks for files an external tool may rewrite.

Start a separate embedded provider rooted at the sandbox. Formatting, generators,
checks and tests run there. Path mapping in results always returns canonical paths plus a
`sandbox` provenance flag. Resource quotas bound simultaneous providers and bytes copied;
requests queue rather than silently falling back to unsafe shared buffers.

At commit, revalidate every canonical content hash, translate the prepared sandbox diff
to declared writes, and apply it through the journal below. Notify/resync the canonical
provider afterwards. This permits parallel readers and multiple prepared writers; two
writers touching the same document conflict at commit.

## Commit and rollback guarantee

Multi-file replacement is not filesystem-atomic. Do not claim that it is. Implement the
strongest practical guarantee with a write-ahead journal:

1. Record transaction metadata, canonical preimage hashes, exact bytes, modes, symlink
   targets, existence, and intended postimage hashes in the service state directory.
2. `fsync` the journal before touching the workspace.
3. Revalidate every preimage and parent-directory condition.
4. Write same-directory temporary files, preserve intended modes, `fsync`, then rename.
5. Record progress after each durable operation.
6. On any failure, restore already-written paths only when they still equal the
   transaction postimage; never overwrite a third party's newer write.
7. On service start, finish rollback for incomplete journals before opening that root.
8. Mark the journal complete, `fsync`, then garbage-collect it later.

The exact guarantee is:

> With cooperative agent99 writers and no external mutation during the bounded commit
> window, commit applies every declared postimage or recovery restores every preimage.
> An external writer racing the window is detected where possible and is never silently
> overwritten; it may leave `RECOVERY_REQUIRED`, because preserving both parties is more
> important than pretending the old tree was restored exactly.

An absolute “exact prior state under arbitrary concurrent external writes or machine
failure” is impossible without filesystem snapshots or replacing the whole workspace
directory. Document this limitation rather than encoding an unachievable invariant.

Formatters, generators and code actions with an unknown write set run only in the
sandbox. Diff their entire sandbox before/after state. Undeclared output either becomes an
explicit proposed operation after policy approval or fails prepare; it never leaks into
the canonical tree.

## Formatting and verification pipeline

Represent each stage as data:

```json
{
  "stage": "format",
  "implementation": "gofmt -w",
  "mode": "transform",
  "scope": ["a.go", "b.go"],
  "started_revision": "...",
  "exit": 0,
  "writes": ["a.go"],
  "evidence_id": "ev_...",
  "status": "passed"
}
```

Pipeline order:

1. Apply declared operations.
2. If explicitly requested, organize imports through the named LSP action in the sandbox.
3. If explicitly requested, apply the named repository formatter transform in the sandbox.
4. Run the configured non-mutating formatter gate; this is the default formatting policy.
5. Parse edited documents and collect parser errors.
6. Run diagnostic barriers for edited files and importer closure.
7. Run configured static checks for relevant project variants.
8. Run impact-selected tests.
9. Optionally run the full repository gate.

No discovery result authorizes steps 2 or 3. The default is byte-preserving, including tabs,
line endings, final newline, BOM, unrelated trailing whitespace and blank lines. Exact,
syntax-anchored and formatter indentation are separate policies; syntax anchoring is allowed
only for one unambiguous parsed node. Whole-file formatters are labeled whole-file and every
change outside declared operations is isolated in `tool_delta`.

Separate these claims in every result:

- `editor_formatted`: LSP/Neovim formatter changed the buffer.
- `repository_formatted`: configured repository formatter produced the staged bytes.
- `format_gate_passed`: a non-mutating repository check accepted them.
- `not_configured` or `provisional`: no stronger claim is available.

Discover conventional formatters and configs, but make executable commands visible and
confirmable in workspace configuration. Add a checked-in `.huyang.toml` format plus a
user override under the XDG config directory. Never execute a newly discovered repository
command merely because its filename looks conventional unless the workspace is trusted.

For a document workspace, verification may be only UTF-8/round-trip plus Tree-sitter
parse. JSON/TOML/YAML may use a parser or configured validator. The result must say that
no type check or project test exists rather than treating that as failure.

## Diagnostic evidence model

Replace one prose verdict with a coverage matrix while retaining a concise summary:

```json
{
  "confidence": "authoritative",
  "coverage": {
    "edited_documents": "complete",
    "direct_importers": "complete",
    "transitive_importers": "bounded",
    "project_variants": "partial"
  },
  "evidence": [
    {"kind":"lsp_push","client":"gopls#1","document":"a.go","version":19},
    {"kind":"lsp_pull","client":"gopls#1","document":"b.go","result_id":"..."},
    {"kind":"project_check","command":"go test ./...","exit":0}
  ],
  "new": [],
  "resolved": [],
  "preexisting_count": 2,
  "provisional_reasons": []
}
```

Use four confidence levels: `authoritative`, `corroborated`, `provisional`,
`unavailable`. Confidence applies per coverage dimension; the top-level value is the
weakest required dimension.

Barrier order:

1. Version-stamped `publishDiagnostics` for the requested document version.
2. `textDocument/diagnostic` pull result where supported; normalize and deduplicate its
   payload instead of discarding useful evidence.
3. `workspace/diagnostic` for affected workspace scope where supported.
4. Work-done progress only as a veto, never proof of completion.
5. Repository static check as a distinct correctness dimension and fallback.
6. Bounded timeout produces `provisional`, never “no errors.”

`documentSymbol`, semantic tokens and silence are not authoritative type-analysis
barriers. Retain exact provenance `(transaction, state_seq, document revision, producer,
producer version, first seen, last seen)`. Store detailed evidence behind IDs and return
only deltas plus failure-critical details by default.

Build on `closure.lua`, but label its current two-hop reference walk precisely. It is not
a complete dependency graph and should not select a “safe” test subset unless a
language-specific adapter establishes that property.

## MCP surface and compatibility

### Protocol implementation

Replace the scanner in `bridge/mcp.go` with the official
`github.com/modelcontextprotocol/go-sdk` at a version supporting MCP `2026-07-28`
(v1.7.0 or newer at planning time). The final 2026 protocol is substantially different:
no initialize/session handshake, per-request protocol/client/capability metadata,
`server/discover`, stateless Streamable HTTP, and a revised Tasks extension.

Support these revisions through the SDK:

- `2026-07-28` first.
- `2025-11-25`, `2025-06-18`, `2025-03-26`, `2024-11-05` as negotiated legacy paths
  while real clients still need them.

Do not advertise a revision from a handwritten list unless its conformance suite passes.
The current `server/discover` response predates the final shape and must not be extended
ad hoc.

Every tool gets:

- JSON Schema 2020-12 input and output schemas.
- `structuredContent` conforming to `outputSchema`.
- A compact JSON/text content fallback for older or lossy clients.
- `readOnlyHint`, `destructiveHint`, `idempotentHint` and `openWorldHint` annotations.
- Cancellation propagation to provider calls and external jobs.
- Stable application error codes inside successful MCP tool errors; malformed protocol
  requests remain JSON-RPC errors.

Use the Tasks extension only for naturally long operations (`change_plan(action="prepare")`, full
checks/tests, language installation), only after per-client capability negotiation. The
ordinary synchronous tool path remains supported because client adoption will differ.

### Result envelope

All new tools return this common shape:

```json
{
  "api_version": "huyang.workspace/v1alpha1",
  "request_id": "req_...",
  "workspace": {"id":"ws_...","epoch":3,"state_seq":81,"kind":"project"},
  "transaction": {"id":"tx_...","state":"READY"},
  "summary": "2 symbols changed; repository format and targeted tests passed",
  "data": {},
  "evidence": {"ids":["ev_..."],"truncated":false},
  "warnings": [],
  "next": []
}
```

`summary` is optimized for the model. `data` is stable machine structure. Detailed diffs,
diagnostics and command output are cursor-addressable. Failure, skipped coverage,
provisional status and truncation are never hidden behind an evidence fetch.

### Client interoperability matrix

Run the same black-box suite against:

- T3's MCP integration.
- Codex CLI/T3 Codex provider.
- Claude Code.
- OpenCode.
- The official MCP inspector and Go SDK client.

Test both stdio and Streamable HTTP wherever the client exposes them. The local versions
present during planning were Codex CLI 0.151.0, Claude Code 2.1.259, OpenCode 1.18.27 and
T3 0.0.38; they are observations, not compatibility baselines. CI should record the
tested version and tolerate clients that negotiate a legacy MCP revision.

The contract suite covers discovery/initialize fallback, tool listing, output schema,
structured-content fallback, concurrent calls, cancellation, a long task where supported,
large results, error rendering, client metadata loss and reconnect during a transaction.

## Existing-tool migration

### Read tools

Add workspace/revision/handle metadata without changing their current human-readable
payload first. Then route them through workspace IDs internally. `find_symbol`, `skim`,
`document_symbols`, definitions, references and call hierarchy may emit handles.

### Mutation tools

Initially keep immediate semantics:

```text
legacy replace_symbol_body(...)
  -> begin exclusive transaction
  -> add one operation
  -> prepare with legacy policy
  -> commit
  -> render the old response fields plus new envelope metadata
```

Apply the same wrapper to insert, partial replace, pattern replace, create/move/delete,
rename, code action and move-symbol operations. `wait=false` may map to a Task or return a
transaction in `PREPARING`; it must no longer attach a future verdict to an unrelated
client call.

### Undo

New code rolls back an uncommitted transaction or creates a compensating transaction from
a committed transaction's exact preimages. Keep `undo_edit` only as a legacy convenience
over the caller's compatibility-wrapper history. Never use implicit “last transaction in
this MCP connection” in the modern surface.

### Tests and checks

Retain `run_tests` and `check_project` as explicit tools. Internally make them pipeline
stages with revision-keyed cache entries. Cache keys include command/config hash,
workspace content revision, relevant environment allowlist and project variant. A result
always distinguishes targeted from full scope.

## Agent-session implementation stages

These are the units executed by the user-approved single-host, self-queuing serial backlog
bootstrap until backlog-v2 is available. They are intentionally smaller than the
architectural milestones below. One stage is one unattended agent session, one reviewed
commit, and one independently checkable outcome. The clean `feature/huyang` branch, this
committed checklist, and `docs/plans/huyang-handoff.md` are the dependency protocol. A stage may stop with
`BACKLOG STATUS: needs-input`, but it must not silently absorb work from the next stage.

The pre-implementation tooling review is recorded in
`docs/plans/huyang-tools-v1alpha1.md`. It fixes the proposed complete modern coding
roster at 17 tools: a 13-tool orientation/editing foundation and four cohesive debugger
tools. `full` is the portable default; fixed lean workflow endpoints are optional context
optimizations. The contract consolidates multi-file lifecycle under `change_plan`, defines
shared target/outcome/coverage/verification/evidence/filesystem schemas, specifies text-only
degradation, and replaces grep's dense prose tags with structured search. S00 validates the
contract with golden schemas and model-selection exercises before freezing it.

The implementation branch is `feature/huyang` in `https://github.com/iryzhkov/agent99`,
created from the reviewed green base `1c5302efe9ca51e1701e73df72d903f225fefe04`. Use the
clean standalone checkout `/home/igor/Work/huyang` so the stages share one branch and execute serially.
Research-only spikes may use task-scoped temporary clones; their output is a committed
decision record consumed by the implementation stage.

### Launch gates

The user explicitly approved the temporary single-host, self-queuing serial backlog bootstrap.
Start or resume a stage only when all of these are true:

1. The checkout is exactly `/home/igor/Work/huyang`, on `feature/huyang`, and is cleanly
   resumable; the deployed/local Neovim plugin checkout is never used.
2. Upstream is fetched, the pinned base `1c5302efe9ca51e1701e73df72d903f225fefe04`
   remains in branch history, and the predecessor commit and gates recorded in
   `docs/plans/huyang-handoff.md` reconcile with the repository.
3. The committed checklist below identifies exactly one first incomplete named stage.
4. The tracked bootstrap prompt passes one successor through project `huyang development`
   with `--ungated`; normal provider concurrency and hard quota health limits still apply.

Backlog-v2 remains the intended durable orchestrator. Migrating this chain to it requires a
separately validated workflow bundle, but does not change the 23 reviewed stages or their order.

### Dependency and stage summary

| Stage | Depends on | Session outcome |
| --- | --- | --- |
| S00 | launch gates, tooling review | Validate/freeze the tool contract, current fixtures, budgets and base commit. |
| S01 | S00 | Extract package boundaries mechanically; behavior remains identical. |
| S02 | S01 | Introduce the provider interface with the socket path as reference backend. |
| S03 | S02 | Resolve the embedded-client decision with a disposable compatibility spike. |
| S04 | S03 | Ship the embedded provider behind a flag with parity and fault tests. |
| S05 | S04 | Add workspace IDs, epochs, state sequences and document revisions. |
| S06 | S05 | Add the provider-independent text core, graceful degradation and document workspaces. |
| S07 | S06 | Replace the handwritten MCP loop in direct mode with the official SDK. |
| S08 | S07 | Add the long-lived service, stdio adapter and deterministic scheduler. |
| S09 | S08 | Add inspectable epoch-bound semantic handles and safe relocation. |
| S09H | S09 | Add bounded read-only Git history, blame and commit provenance. |
| S10 | S09H | Add durable transaction intent, validation and preview without mutation. |
| S11 | S10 | Add exclusive buffer-backed prepare and exact provider rollback. |
| S12 | S11 | Add the write-ahead commit journal and ordinary commit path. |
| S13 | S12 | Prove crash recovery and compensating undo with exhaustive failpoints. |
| S14 | S13 | Select the sandbox backend through a bounded host/filesystem spike. |
| S15 | S14 | Prepare transactions in isolated sandboxes with bounded parallelism. |
| S16 | S15 | Add repository formatter transforms, gates and declared-write enforcement. |
| S17 | S16 | Add authoritative diagnostic barriers, evidence and provenance. |
| S18 | S17 | Add impact graph, advisory test selection and project variants. |
| S19 | S18 | Migrate legacy mutation wrappers in bounded families. |
| S19D | S19 | Add the compact four-tool modern debugger facade over proven DAP behavior. |
| S20 | S19D | Complete wrapper migration, compatibility evaluation and release candidate. |

### Committed stage checklist

- [x] S00 — Baseline and contract freeze
- [x] S01 — Mechanical package boundary
- [x] S02 — Provider seam with socket reference backend
- [x] S03 — Embedded provider decision spike
- [x] S04 — Embedded provider production path
- [x] S05 — Explicit workspaces and revisions
- [x] S06 — Provider-independent text core and document workspaces
- [x] S07 — Official MCP SDK in direct mode
- [x] S08 — Shared service and scheduler
- [x] S09 — Semantic handles
- [x] S09H — Read-only Git source provenance
- [ ] S10 — Transaction intent, validation and preview
- [ ] S11 — Exclusive provider prepare
- [ ] S12 — Journaled commit
- [ ] S13 — Crash recovery and compensating undo
- [ ] S14 — Sandbox backend decision spike
- [ ] S15 — Isolated sandbox prepare
- [ ] S16 — Repository formatting and verification pipeline
- [ ] S17 — Diagnostic evidence and provenance
- [ ] S18 — Impact graph, targeted tests and variants
- [ ] S19 — First legacy wrapper families
- [ ] S19D — Compact modern debugger facade
- [ ] S20 — Remaining wrappers, evaluation and release candidate

The default dependency graph is linear where shared code is involved. Backlog-v2 may
run fixture capture for S00 and the S03/S14 research probes as separate task-scoped jobs,
but their merge/decision task remains a serial dependency. Do not parallelize mutation
stages merely because the scheduler can: independent clones would produce competing
architectures and make integration the hidden final task.

### S00. Baseline and contract freeze

- Record the current upstream commit, tool schemas, representative compact/full outputs,
  latency and result-response budgets, and supported client versions.
- Validate `huyang-tools-v1alpha1.md` with golden schemas/results and model-selection
  exercises for the orientation, editing and debugging workflows: overview, text search,
  symbol lookup, one-shot edit, inline plan prepare/apply, diagnostic repair, debugger fault
  localization and stale-revision recovery. Record calls, tool-schema tokens, result tokens,
  wrong-tool choices and coverage mistakes; amend before implementation when a workflow is
  not tight.
- Freeze search-refinement/result-lineage, compact text rendering, structural-search,
  hover-routing, fixed-profile launch and debugger-evaluate fixtures from the contract.
- Freeze multi-provider fixtures: one language with competing primaries, complementary
  diagnostic providers, a declared fallback, conflicting findings and one broken provider.
- Preserve current structured-file, multi-workspace, client-scoping, importer-closure,
  stale-edit, formatting, parser-verdict, undo and delayed-diagnostic fixtures.
- Add no new architecture and make no production behavior change.
- Gate: `make smoke`, `go test ./...`, `go vet ./...` and `git diff --check` pass;
  the baseline report and fixtures are committed.

### S01. Mechanical package boundary

- Move bridge internals behind `internal/` and leave small command entry points under
  `cmd/`, without renaming public tools or changing responses.
- Define package ownership for MCP adapters, workspace core and provider transport; do not
  add the new service behavior yet.
- Gate: the S00 snapshots are byte-compatible except for explicitly normalized build/version
  fields; all current suites pass.

### S02. Provider seam with socket reference backend

- Introduce `Provider`, request/result, health and lifecycle contracts around the existing
  socket/start-poll implementation.
- Model deterministic provider IDs, per-language/capability primary roles, complementary
  providers and named analysis profiles without requiring per-call routing on the hot path.
- Route all current tests through the interface and keep socket behavior as the oracle.
- Include cancellation and deadline fields in the interface even if the socket backend can
  initially report cancellation as unsupported.
- Gate: no direct socket assumption remains above the backend package; all old tests pass.

### S03. Embedded provider decision spike

- In a disposable test package, exercise a pinned `neovim/go-client` version against the
  supported Neovim range, `--embed --headless`, inbound requests, unsolicited
  notifications, cancellation, out-of-order completions and provider death.
- Measure startup, warm-call overhead, memory and stderr behavior against the socket backend.
- Commit a decision record and hermetic spike tests or fixtures; remove disposable binaries.
- Gate: choose and pin the library or document the blocking incompatibility and the smallest
  MessagePack-layer fallback. Stop for user input if neither path has bounded risk.

### S04. Embedded provider production path

- Implement bootstrap/capability handshake, completion notification, health checks,
  cancellation, deadlines, epoch-changing restart and failure classification.
- Keep the socket backend selectable for comparison and rollback.
- Gate: headless, debug and injected provider-fault suites pass against both backends; an
  embedded trace starts no `nvim --server` polling subprocess.

### S05. Explicit workspaces and revisions

- Add workspace IDs, kind, epoch and state sequence to the core and every new result.
- Add layered document snapshots and lazy SHA-256 revision tokens; detect same-metadata
  content changes, atomic saves, deletes/recreates and provider restarts.
- New calls require workspace ID; root inference remains only in legacy adapters.
- Gate: no modern mutation can execute without an explicit revision precondition, and the
  adversarial filesystem suite catches every stale case.

### S06. Provider-independent text core and document workspaces

- Add allowlisted `open_document`/`workspace_open` without walking or indexing the parent.
- Implement the bounded native filesystem walker, literal/regex search, exact-byte reads,
  revision/hash/anchor guarded text and file edits, diffs and journal recovery without Git,
  Neovim, ripgrep, Tree-sitter, LSP, formatter or project-command dependencies.
- Support parser-backed sections where available and content/anchor range handles when not.
- Keep stable result shapes while marking unavailable semantic fields and precise coverage.
  `workspace_inspect` reports actionable, sanitized provider/environment failures without
  installing tools or mutating configuration.
- Gate: Markdown, JSON, TOML, YAML, Dockerfile and parserless files under `$HOME` and
  `/tmp` can be read, previewed and edited without scanning siblings. With every optional
  layer disabled, a project can still be oriented, guarded-text edited, diffed and recovered.

### S07. Official MCP SDK in direct mode

- Replace the handwritten scanner with the latest reviewed stable Go SDK release supporting
  `2026-07-28`; do not consume a prerelease by default.
- First preserve a direct/single-process mode: modern stateless and negotiated legacy
  protocol, schemas, structured content, annotations, errors, cancellation and Tasks gates.
- Generate deterministic `full|orient|edit|debug` catalogs from one registry; lean profiles
  are presentation filters and `full` contains every modern coding capability.
- Add conformance plus official-client tests, including malformed/large requests and
  out-of-order concurrent IDs.
- Gate: protocol conformance and the black-box client matrix pass in direct mode.

### S08. Shared service and scheduler

- Add `huyang serve`, Unix control socket, optional loopback stateless HTTP and thin
  `huyang mcp` stdio adapter.
- Implement `huyang mcp --profile ...` with `full` default and fixed HTTP routes `/mcp`,
  `/mcp/orient`, `/mcp/edit` and `/mcp/debug`; do not rely on custom initialization fields or
  mid-session tool-list changes.
- Add registry persistence, provider quotas and explicit scheduler classes. Correctness must
  not depend on transport identity or sticky routing.
- Treat request retry as at-least-once delivery: only idempotent reads may be blindly
  repeated; stateful calls use transaction/request idempotency keys.
- Gate: concurrent clients survive adapter disconnect/reconnect and service/provider restart;
  ambiguous workspace routing is impossible on the modern API.

### S09. Semantic handles

- Add inspectable symbol/range handle records, TTL, epoch invalidation and exact/relocated/
  conflicted resolution results.
- Add frozen search result-set handles with complete-coverage, revision, count and non-overlap
  preconditions for all-match replacement; paged presentation does not truncate the set.
- Support monotonic server-side result refinement with parent lineage, inherited coverage and
  retained/eliminated counts; historical and current-source sets remain distinct types.
- Accept handles in one-operation mutation preparation while retaining human locators.
- Gate: overload, duplicate-name, rename, move, formatting, signature-change and
  delete/recreate cases never bind silently to a different declaration.

### S09H. Read-only Git source provenance

- Populate `workspace_open.recent_commits` with the bounded default three first-parent
  summaries and commit handles; omit email, bodies and path lists from the automatic overview.
- Add commit handles, bounded line/range/symbol/file history views, recent touching commits,
  commit change views and local history search over messages/paths/diffs.
- Define file age as named ref/traversal/rename-policy metrics, including first-parent commits
  since last change; label shallow or rename-ambiguous introduction evidence provisional.
- Map canonical dirty and prepared bytes honestly as uncommitted/derived while retaining
  committed provenance only for unchanged mapped spans.
- Prohibit network, ref/index/worktree writes, hooks, pagers, external diff and textconv;
  sanitize any Git subprocess against repository-configured execution.
- Gate: merges, renames, shallow clones, missing objects, dirty files, prepared plans,
  malicious Git config and binary/submodule histories return bounded truthful coverage and
  never mutate the repository.

### S10. Transaction intent, validation and preview

- Implement the internal plan lifecycle behind the single model-facing `change_plan` action
  union. Persist create/edit/inspect/preview/discard records without modifying provider
  buffers or disk.
- Normalize and topologically order operation kinds; add `delete_symbol`.
- Validate the complete operation vector against revisions/handles before any application,
  and make repeated calls idempotent.
- Gate: preview is deterministic, survives service restart and reports every conflict in a
  stale multi-file batch; canonical bytes and buffers remain untouched.

### S11. Exclusive provider prepare

- Add a transaction lease and batch-apply to unsaved canonical buffers.
- Block or explicitly reject all non-owner same-workspace provider calls so no client can
  observe the staged view without the transaction ID.
- Suppress intermediate diagnostics and restore exact buffer preimages on cancellation,
  failure or provider death. Checks requiring disk remain unavailable at this stage.
- Gate: fault injection at every apply/state transition either reaches READY/PROVISIONAL or
  restores every buffer; the disk never changes before commit.

### S12. Journaled commit

- Persist exact preimages/postimages, metadata and per-path progress; fsync before writes.
- Revalidate the entire canonical write set, then temp-write/fsync/rename and resync the
  canonical provider.
- Keep the cooperative guarantee wording; do not claim filesystem-wide atomicity.
- Gate: normal create/replace/move/delete, modes and symlinks commit correctly, and ordinary
  injected errors enter a recoverable recorded state.

### S13. Crash recovery and compensating undo

- Add startup recovery for every incomplete journal state and protection against overwriting
  a third-party post-write.
- Replace durable undo with a compensating transaction over exact preimages; keep legacy
  `undo_edit` as an adapter.
- Run the service as a separate process and kill it before/after every journal/rename step.
- Gate: all failpoint cases meet the documented guarantee, with external races producing
  explicit `RECOVERY_REQUIRED` rather than data loss.

### S14. Sandbox backend decision spike

- Measure reflink, overlay/fuse-overlay and safe-copy choices on intended hosts and repository
  sizes, including dirty/untracked files, permissions, symlinks and ignored outputs.
- Define quotas, exclusion policy, cleanup and deterministic backend selection/fallback.
- Commit a decision record and fixtures only; do not add half-supported production backends.
- Gate: one primary and one safe fallback backend are chosen, or the user is asked to decide
  an explicit cost/privilege tradeoff.

### S15. Isolated sandbox prepare

- Materialize the exact canonical state, start a transaction provider in the sandbox and map
  all evidence back to canonical paths.
- Permit bounded parallel prepares; serialize writes inside each sandbox and queue on quotas.
- Revalidate canonical revisions only at commit; two prepared writers touching the same file
  conflict there.
- Gate: readers see one stable canonical revision throughout prepare, disjoint transactions
  prepare concurrently, and sandbox bytes equal the declared base plus operations.

### S16. Repository formatting and verification pipeline

- Add layered `.huyang.toml` project declarations and user trust/resource policy.
- Preserve exact bytes by default. Run the non-mutating format gate when configured; run
  import organization or formatter transforms only under explicit visible policy.
- Implement exact/syntax-anchor/formatter indentation modes, protected byte invariants and
  range-versus-whole-file scope enforcement.
- Run parser checks and configured static/test stages against the exact prepared bytes.
- Diff the whole sandbox around unknown-write tools; undeclared output fails or becomes an
  explicit proposed operation under policy.
- Gate: agent99 formatting regressions and formatter/generator fault cases prove exact
  rollback, complete `tool_delta`, and no undeclared canonical writes.

### S17. Diagnostic evidence and provenance

- Normalize versioned push, pull and workspace diagnostics and deduplicate their payloads.
- Aggregate only the diagnostic providers selected by the workspace analysis profile; retain
  per-item provider provenance and conflicting findings rather than collapsing them.
- Record coverage/confidence per required dimension and provenance by transaction, revision,
  producer and producer version. Progress remains a veto, never a completion proof.
- Add a durable acknowledged diagnostic inbox. Late updates appear as compact structured
  notices on later same-workspace replies and remain fully retrievable by cursor.
- Rank culprit transactions as exact/strong/likely/ambiguous/unattributed using producer
  version, postimage, symbol and impact-path evidence; timing alone is never causal proof.
- Remove unrelated-call prose verdict carry from the modern path.
- Gate: fake-LSP permutations and real gopls/tsserver/pyright/lua_ls/bash cases never label
  provisional or unavailable evidence clean.

### S18. Impact graph, targeted tests and variants

- Generalize the current bounded closure into explicit language-adapter edges with
  completeness/cap disclosures.
- Add revision-keyed test history, advisory target selection and build/configuration variants.
- Keep full-gate status independent from targeted status.
- Gate: reflection, dynamic import, generated/config files, graph caps and multi-variant cases
  disclose uncertainty; targeted success never implies full success.

### S19. First legacy wrapper families

- Migrate symbol/range edits and file create/move/delete wrappers to one-operation
  transactions, preserving legacy schemas and text.
- Compare exact baseline snapshots and retain escape flags for the old path.
- Gate: migrated families preserve guarded targeting, parser checks, import organization,
  diagnostic deltas and per-client compatibility behavior.

### S19D. Compact modern debugger facade

- Map existing proven DAP behavior into `debug_session`, `debug_breakpoints`,
  `debug_control` and `debug_inspect` closed action unions; do not expose the legacy
  one-tool-per-action roster in the modern full/debug profiles.
- Reuse normal revision-bound source targets and handles for breakpoints, stack frames and
  source locations. Start/attach may accept initial breakpoints; each stop/control result
  returns stop reason, top location, compact top-frame locals and changes since the last stop.
- Treat arbitrary evaluate as side-effecting unless the adapter/runtime enforces read-only
  evaluation. Require an explicit per-call side-effect policy otherwise.
- Preserve degraded environment repair when an adapter, runtime, source map or executable is
  unavailable; missing debug capability must not break orientation tools.
- Gate: equivalent debug tasks use fewer calls/schema tokens than agent99, retain stop/source/
  variable fidelity, and never evaluate potentially effectful code under the read-only policy.

### S20. Remaining wrappers, evaluation and release candidate

- Migrate rename, code action, homogeneous replace-matches and move-symbol families; make
  tests/checks pipeline adapters with revision-keyed caches.
- Run every protocol/client/language/failpoint suite and the paired token/quality evaluation.
- Publish compatibility window, recovery guide, protocol/kernel/transaction version promises,
  migration guide and deployment-readiness report.
- Gate: the definition of done is met, the branch is clean, and no deployment has occurred.
  Do not queue a successor; wait for explicit rollout approval.

### Session execution contract

At the beginning of every stage:

1. Read this plan, the current handoff and any declared predecessor artifacts completely.
2. Fetch upstream state, inspect Git status/log, verify `feature/huyang`, and reconcile the
   actual code and test state instead of trusting the prior final message.
3. Select only the named stage. If predecessor exit criteria are not actually met, repair
   that stage or stop with `needs-input`; do not build on a false checkpoint.
4. Record the base commit and exact intended exit test in the handoff before editing.

During and at the end of every stage:

- Preserve unexplained changes and never work in the deployed Neovim plugin checkout.
- Add tests with every behavior change; run targeted tests, then `make smoke`,
  `go test ./...`, `go vet ./...` and `git diff --check` unless the stage explicitly
  documents why one is inapplicable.
- Update the stage checklist and `docs/plans/huyang-handoff.md` with decisions, tests,
  risks and the exact next stage.
- Commit code, tests, plan progress and handoff together; leave the workflow checkout clean.
- Produce declared artifacts (decision record, benchmark, conformance report or readiness
  report) before releasing the dependent task.
- Never install/deploy Huyang, update the pinned agent99 plugin, restart a live MCP service,
  modify live config/state, push, or create a pull request without separate authorization.
- End with exactly one backlog status. After a successful commit and clean-tree check, queue
  exactly one successor from `docs/plans/huyang-bootstrap-session-prompt.md`, using `--ungated`.
  A blocked or failed stage queues nothing and records the exact recovery question in the handoff.

### Future backlog-v2 launch bundle

If this bootstrap chain is migrated after backlog-v2 is deployed, create a validated workflow bundle
in the development repository containing:

- `workflow.yaml` with `environment.type: git`, `scope: workflow`, the approved
  upstream/base ref, ordered S00-S20 dependencies and the repository lock;
- one immutable copy of this plan plus the base contract/fixture report as workflow inputs;
- one prompt per stage naming its exact scope, output artifact and gate;
- verification declarations for the normal gates and stage-specific artifacts;
- a final task that emits the release-candidate and deployment-readiness report but performs
  no deployment.

Do not write that manifest against the in-development backlog schema now. Generate and
validate it against the deployed backlog-v2 schema immediately before submission.

## Architectural milestone map (reference)

This map groups the original architectural requirements; it is not the execution order.
S00-S20 above are authoritative. In particular, the workspace identity/revision parts of
M3 ship before M2 exposes a shared daemon. Each milestone ships behind a capability flag
and leaves the old path usable.

### M0: Baseline and contract freeze

- Tag the known-good current implementation and preserve paired evaluation fixtures.
- Capture schemas and representative outputs from the launch-time upstream base commit
  (currently inspected at `947f5b1`).
- Add tests for the newest client scoping and importer-closure behavior.
- Add a protocol/client compatibility dashboard.

Exit: current smoke suites and client matrix are reproducible; no architecture changes.

### M1: Direct embedded provider

- Introduce the Go `Provider` interface.
- Implement current socket provider as one backend.
- Add `nvim --embed --headless` backend with the official Go client.
- Add Lua completion notifications, internal handshake, cancellation and health probes.
- A/B benchmark startup, per-call latency, memory and failure behavior.

Faults: malformed MessagePack result, unsolicited request, Lua panic, provider exit during
call, stalled call, stderr flood, cancellation, incompatible Neovim API.

Exit: all current headless and debug suites pass against both backends; embedded is faster
or its remaining cost is explained; no `nvim --server` subprocess appears in an embedded
trace.

### M2: Service and modern MCP boundary

- Move core lifecycle behind `huyang serve`.
- Add Streamable HTTP and stdio proxy modes through the official MCP Go SDK.
- Implement final `2026-07-28` plus legacy negotiation.
- Add structured output, schemas, annotations, cancellation and conformance tests.
- Bind HTTP to loopback by default with a local credential; add Unix socket activation.

Faults: adapter death, service restart, duplicate/retried request, unsupported revision,
client omits identity, concurrent IDs returned out of order.

Exit: official conformance suite passes and T3, Codex, Claude Code and OpenCode complete
the black-box read/edit smoke task.

### M3: Explicit workspaces, standalone documents and revisions

- Add workspace IDs, epochs, state sequence and revision store.
- Add project/document workspace kinds and file allowlists.
- Hash mutation targets; emit revision IDs on reads.
- Replace sticky routing in new APIs; retain it only in compatibility wrappers.
- Add parser-error evidence and text-only range handles for standalone files.

Faults: atomic-save inode replacement, same-size/same-mtime write, symlink retarget, file
delete/recreate, provider restart, Markdown/config/plain-text files with no LSP.

Exit: a single file in `$HOME` or `/tmp` can be edited without scanning its parent, and a
stale same-metadata mutation is caught by content hash.

### M4: Semantic handles

- Return optional symbol/range handles from reads.
- Add registry inspection, TTL and epoch invalidation.
- Implement safe exact resolution, formatting relocation and ambiguity conflicts.
- Accept either handle or legacy locator in single-operation mutations.

Faults: overloads, duplicate names, rename, move, formatter movement, signature change,
delete/recreate with same name, workspace restart.

Exit: handles never silently bind to a different declaration in the adversarial suite.

### M5: Exclusive transaction kernel

- Add begin/add/preview/prepare/status/commit/rollback.
- Batch validate every operation against its revisions.
- Stage unsaved in canonical provider under an exclusive lease.
- Suppress intermediate diagnostic presentation and return one change-set summary.
- Implement `delete_symbol`.

Faults: second client reads/writes during lease, cancellation at every state transition,
one stale file in a batch, provider death, partial Lua batch failure.

Exit: multi-file editor-only transactions either prepare completely or restore all
provider buffers; other clients never observe an unlabeled staged view.

### M6: Journaled commit and recovery

- Implement exact preimage/postimage journal and startup recovery.
- Use temp-write/fsync/rename with per-step progress.
- Detect external races before and during commit.
- Turn committed undo into a compensating transaction.

Faults: injected failure before/after every journal and rename step, kill -9, disk full,
permission loss, external writer after the first rename, symlink and mode changes.

Exit: exhaustive failpoint tests restore preimages under the documented cooperative
guarantee and never overwrite an injected third-party post-write.

### M7: Sandbox preparation and repository formatting

- Build copy-on-write workspace snapshots including dirty/untracked state.
- Start transaction providers in sandboxes.
- Discover/configure repository formatter transform and gate stages.
- Import the full sandbox diff only as declared operations.
- Permit bounded parallel transactions in one canonical workspace.

Faults: formatter edits undeclared file, generator creates/deletes files, tool timeout,
hardlink/reflink capability differences, huge repository, ignored build outputs.

Exit: another client sees the canonical revision throughout prepare; formatting and tests
run against exactly the staged bytes; two disjoint transactions prepare concurrently.

### M8: Authoritative diagnostic and provenance model

- Normalize push, pull and workspace diagnostics.
- Track evidence by transaction/document/producer/version.
- Replace prose-only verdict with coverage/confidence data plus concise summary.
- Make project check a configurable fallback policy.
- Remove deferred verdict leakage onto unrelated calls.

Faults: server omits/wrongly stamps versions, duplicate push/pull diagnostics, late
cross-file publish, LSP restart, progress never closes, pull timeout.

Exit: every “clean” required dimension names authoritative evidence; all incomplete paths
are provisional or unavailable.

### M9: Impact graph, targeted tests and variants

- Generalize `closure.lua` evidence into language adapters and dependency edges.
- Record test failures/durations and optional coverage associations by revision.
- Select advisory targeted tests and state why each was chosen.
- Model configured build tags, tsconfigs, platforms and feature sets.

Faults: reflection/dynamic imports, generated code, config/schema change, capped graph,
multiple test configs, target passes while full suite fails.

Exit: targeted results never imply a full pass; variant and graph gaps are explicit.

### M10: Wrapper migration and evaluation

- Route each existing mutation family through transactions.
- Remove socket backend only after an escape period.
- Run paired tasks from the brief and compare medians and worst cases.
- Publish protocol, internal-kernel and transaction-format stability guarantees.

Exit: definition of done below is met and old clients remain functional through the
declared compatibility window.

## Test strategy

Keep unit, provider integration, transaction/failpoint, protocol conformance and real-LSP
smoke suites separate so a failure names its layer.

Minimum real-language matrix:

- Go/gopls: cross-package signature change, gofmt, generated file, build tags.
- TypeScript/tsserver: multi-tsconfig import move and project-wide late diagnostics.
- Python/pyright: file move, imports, formatter, delayed cross-file analysis.
- Lua/lua_ls: duplicate names, import organization and fast version-stamped pushes.
- Bash/bash-language-server: silent-server/provisional path.
- Rust/rust-analyzer when available: work-done progress and feature variants.
- Markdown, JSON, TOML, YAML, Dockerfile and parserless text as document workspaces.

Use deterministic fake LSP servers for barrier permutations, and real servers only for
behavioral confirmation. Add failpoints to provider RPC, sandbox copy, every journal
transition, file rename, formatter, checks and tests. A crash-recovery test must run the
service as a separate process and kill it, not simulate death with an ordinary error.

## Observability and budgets

Extend friction records rather than create an unrelated telemetry stream. Never record
source text by default.

Record:

- MCP revision, client implementation/version, transport and compatibility path.
- Workspace kind, provider backend/version, epoch and restart cause.
- Queue wait, provider time, LSP wait, sandbox copy, format/check/test and total latency.
- Transaction states, operation/file/symbol counts, conflicts, rollback and recovery.
- Handle exact/relocated/invalid resolution.
- Diagnostic barrier chosen, coverage, confidence and late-diagnostic count.
- Structured and text result bytes, truncation/evidence fetches and tool-call retries.
- Tokens when the host exposes them; otherwise response bytes/lines as stable proxies.

Initial budgets to validate, not silently enforce:

- Normal read summary: <= 8 KiB and <= 120 lines unless detail requested.
- Successful mutation/prepare summary: <= 12 KiB and <= 160 lines.
- Failure result: never truncate the primary error, affected targets or missing coverage;
  detailed evidence may be paged.
- Direct embedded transport overhead: p95 <= 10 ms excluding tool work.
- Warm standalone document open: p95 <= 300 ms without LSP.
- Warm semantic read: no regression beyond 15% versus socket baseline.
- Transaction journal overhead: <= 5% of prepare+commit time for ordinary changes.
- Sandbox copy has explicit byte/time/provider quotas and reports queueing.

Set final thresholds from M0/M1 measurements. The primary success metric remains fewer
inconsistent-workspace reconstructions, not raw call count.

## Likely repository changes

### Existing Go files

- `bridge/mcp.go`: replace handwritten protocol loop with SDK registration/adapters.
- `bridge/schemas.go`: generate/version input and output schemas and annotations.
- `bridge/headless.go`: split registry from socket provider; later retire socket details.
- `bridge/nvim.go`: become provider transport facade; remove CLI polling from embed path.
- `bridge/routing.go`: workspace-ID routing and legacy adapter separation.
- `bridge/client.go`: attribution only; remove correctness dependence on client identity.
- `bridge/lifecycle.go`: daemon/provider/sandbox lifecycle and quota hooks.
- `bridge/tools.go`: return common structured envelopes.
- `bridge/friction.go`: new timing, conflict, evidence and result-size fields.
- `bridge/main.go`: `serve`, `mcp`, recovery and diagnostic subcommands.

### New Go modules (names may change after package extraction)

- `bridge/provider.go`, `provider_socket.go`, `provider_embed.go`
- `bridge/workspace.go`, `scheduler.go`, `revision.go`, `handles.go`
- `bridge/transaction.go`, `sandbox.go`, `journal.go`, `recovery.go`
- `bridge/pipeline.go`, `project_config.go`, `evidence.go`
- `bridge/mcp_stdio.go`, `mcp_http.go`, `legacy.go`

Before these grow, move package-main internals under `internal/` and leave small commands
under `cmd/huyang` only after the provider seam is covered by tests; do not combine that
mechanical move with semantic changes.

### Lua files

- `lua/agent99/rpc.lua`: request-context dispatch and completion notifications.
- `lua/agent99/client.lua`: generalize coroutine context to request/transaction context.
- `lua/agent99/core.lua`: full document snapshots, hash coordination and no-save staging.
- `lua/agent99/index.lua`: handle locators/fingerprints and relocation candidates.
- `lua/agent99/edit.lua`: batch apply without final verdict per sub-operation; structured
  diagnostic evidence; remove deferred global carry.
- `lua/agent99/edits.lua`: provider-local staging/preimages; eventually cease being the
  durable transaction store.
- `lua/agent99/closure.lua`: structured impact edges and completeness evidence.
- `lua/agent99/install.lua`, `testrun.lua`: pipeline adapters and revision-aware results.
- `lua/agent99/lsp.lua`: emit revision/handle metadata from semantic reads.

### Tests and documentation

- Replace Python bridge helpers that assume one request/one response order with a
  concurrent JSON-RPC harness.
- Add embed-provider, daemon, document workspace, transaction, journal recovery, modern
  MCP and client-compatibility suites.
- Keep the present `drive_headless.py` cases as behavioral regression tests.
- Add protocol and internal-kernel version documents, `.huyang.toml` schema, migration
  guide and operational recovery guide.

## Rejected alternatives

### Put the transaction coordinator entirely in Lua

Rejected because crash recovery, filesystem journals, MCP statelessness, sandboxes,
multi-client scheduling and process quotas are service concerns. Lua closures in the
current ledger are not durable or serializable.

### Put semantic targeting entirely in Go

Rejected because it duplicates Tree-sitter/LSP identity and would drift from the editor's
actual buffers and server state. Go should validate policy; Neovim should resolve and
apply semantic locators.

### Treat `--embed` as a replacement for `--headless`

Rejected. Embed alone has a UI startup barrier. The server needs
`nvim --embed --headless` unless it deliberately implements a UI.

### Keep one full MCP/core process per stdio client

Rejected as the primary topology. It cannot safely share direct stdio-owned provider
children, duplicates language servers, and makes cross-client transaction locks and
recovery process-local. Keep it only as an isolated fallback/test mode.

### Use a Git worktree as the transaction snapshot

Rejected as the general solution because it drops dirty and untracked current state,
does not support non-Git/document workspaces, and verifies a different tree. It can be an
optional optimized backend only when the base is clean and the precondition is explicit.

### Claim OS-atomic multi-file commit

Rejected because ordinary filesystems provide atomic rename per path, not one atomic
rename across an arbitrary set. Use a durable journal and state the cooperative guarantee.

### Make semantic handles serialized permanent identifiers

Rejected for v1. Language servers and syntax trees do not provide that identity. Start
with epoch-bound handles and earn longer lifetime through measured relocation behavior.

## Decisions to resolve with spikes

1. Does the current Neovim Go client handle Neovim 0.11-0.12, inbound requests,
   cancellation and high concurrency well enough, or should Huyang wrap only its
   MessagePack layer?
2. What sandbox backend gives acceptable copies on the actual Linux hosts: reflink,
   overlayfs/fuse-overlayfs, or safe copy with ignore rules?
3. Which real servers reliably support pull/workspace diagnostics, and whether Neovim
   exposes all required result IDs/version fields without patching handlers further?
4. How much same-workspace transaction parallelism is worth the memory of duplicate
   language servers? Default may remain one sandbox writer with configurable expansion.
5. Which of T3, Codex, Claude Code and OpenCode support MCP `2026-07-28`, Tasks and
   Streamable HTTP at their tested releases? The server negotiates; the plan must not
   guess.
6. Which coverage data formats are useful enough to justify ingestion per language?

Configuration layering is frozen before implementation: checked-in `.huyang.toml` declares
commands, variants and provider profiles; user configuration grants trust and overrides
resource policy. Discovery alone never authorizes execution.

None blocks M1-M3. Sandbox choice blocks M7; client support only changes negotiated
features, not the core API.

## Definition of done

Huyang is ready to be the default coding backbone when all of these are true:

1. `huyang serve` survives individual MCP client disconnects and owns direct embedded
   providers without socket polling subprocesses.
2. MCP `2026-07-28` conformance passes, legacy negotiation still works, and the tested
   T3/Codex/Claude Code/OpenCode versions pass the same black-box suite.
3. Separate workspaces execute in parallel; same-workspace scheduling is deterministic;
   uncommitted transaction state is never exposed without its transaction ID.
4. Project and single-document workspaces both work, including parserless text.
   With Git, Neovim, parser, LSP, formatter and project commands disabled, the native text
   core still orients, searches, guarded-edits, diffs and recovers.
5. Every semantic read identifies workspace and revision; every mutation carries a
   revision or handle precondition.
6. Handle relocation never silently selects a different declaration.
7. Multi-file prepare happens in isolation, repository format/check/test stages see the
   exact staged bytes, byte-preserving formatting is the default, transform changes are
   isolated in `tool_delta`, and undeclared tool writes cannot escape.
8. Commit/rollback and kill -9 recovery meet the documented journal guarantee under all
   injected failpoints.
9. Every clean verdict names evidence and coverage; provisional, skipped and unsupported
   dimensions remain visible in the compact result.
10. Targeted tests are clearly advisory and project variants state what was and was not
    covered.
11. Existing tools work as wrappers for the published compatibility window, with no
    regression in their guarded-edit behaviors.
12. Paired evaluations show lower retry/context cost without worse formatter escapes,
    incomplete refactors, final review findings or worst-case recovery.
13. Full and lean fixed catalogs expose exactly the frozen 17/8/13/12 modern tool counts and
    work through the tested harnesses without dynamic profile negotiation.
14. Search refinement preserves typed lineage/coverage, homogeneous all-match replacement is
    atomic, and read-only Git overview/blame/history never mutates or executes repository
    configuration.
15. The four-tool debugger facade preserves DAP fidelity, shares source handles and never
    performs potentially side-effecting evaluation under read-only policy.

## External specifications used

- [Neovim API and RPC](https://neovim.io/doc/user/api/)
- [Neovim startup options](https://neovim.io/doc/user/starting/)
- [Neovim channels](https://neovim.io/doc/user/channel/)
- [MCP 2026-07-28 announcement and lifecycle changes](https://blog.modelcontextprotocol.io/posts/2026-07-28/)
- [MCP 2026-07-28 stdio transport](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/transports/stdio.mdx)
- [MCP 2026-07-28 Streamable HTTP transport](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/transports/streamable-http.mdx)
- [Official MCP Go SDK compatibility](https://github.com/modelcontextprotocol/go-sdk)
