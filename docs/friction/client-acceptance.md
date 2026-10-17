# Private actual-client acceptance (M7/M8)

Implementation base: `1469c92c5aea7a67a06048593ba87c57caed8c97`.
Round 1 fixes are based directly on `6e7ee7ab865498870d042f508dff35dbb0637281`.
This directory is an opt-in job program, outside the Go test suite. No runtime
acceptance is claimed by its implementation commit. It changes no production
renderer, service, client installation, Ollama installation or fleet configuration.

The parent owns candidate builds, the single Chaldea slot, execution, review and
integration. Cherry-pick only this harness commit onto the integrated source.
Implementation used Huyang for all repository reads/edits. Public npm metadata
was resolved to a lock with Arborist's metadata-only `buildIdealTree` and
`tree.meta.save`; no `reify`, extraction, installation, builds, tests, clients or
models were run during implementation. Do not confuse metadata resolution with
a successful bootstrap.

## Inputs and exact runtime command

Run inside a new disposable **Chaldea agent-a job**, Linux x86_64 glibc, UID0 in a
user namespace with a clean ordinary home and no credentials. Both entry points
refuse the initial host user namespace. HOME must equal `/home/job`, a real directory with no symlink.
Before any write, J must resolve strictly below it; HOME is never rewritten. Before bootstrap and each client, a name-only
preflight refuses `.codex`, `.agents`, `.opencode`, `.claude*`, `.npmrc` and the
corresponding OpenCode/Codex/Claude discovery paths under `.config` and
`.local`/`.cache`, including dangling links. It records checked/existing paths,
never their contents. This checks client discovery paths, not every possible
credential location. Every client has supported config overrides; XDG/temp/cache
paths are job-owned.
Keep the job path short enough that `J/c/opencode/control.sock` is under 100 bytes.

The job image/input must provide:

- Python >=3.11, Node/npm (use the job's pinned Node 26 toolchain), Go >=1.25,
  Neovim >=0.11 with its own matching runtime, and strace with ptrace permitted
  within the job. The harness installs none of these system tools.
- A real, separately built production candidate executable and matching source
  Lua runtime (`lua/huyang/rpc.lua` must exist). No test-only candidate is accepted
  as a substitute. Record the integrated source SHA used for that build.
- The unchanged candidate V2 module staged as
  `acceptance-input/huyang-profiles.js` from Omarchy merge
  `ebec80bca66d684d6fe47c5c57c7cf212b6558b2`, path
  `config/opencode/plugins/huyang-profiles.js`. Parent verified its Git blob as
  `88ea20a15450e52fd94194a1f91ff69e7277da30` and exact content SHA-256 as
  `e46883992727741758115492030f75bb26dbea14dccd3a4b4c3459cc5547f6c4`.
  The runner requires this fixed SHA and an explicit matching --plugin-sha256;
  it checks both input and discovered-copy bytes. A document revision is not a
  content hash. Root stages and verifies the immutable merged asset inside the
  job; this branch vendors no duplicate. Fleet merge
  `da2642f5cd4ccd86708459939b2a2a3fe21fe1bf` carries the same plugin blob
  (parent-supplied provenance). Stage non-ignored job inputs before submission;
  Chaldea does not transfer ignored builds, symlinks, installed clients or host
  configuration.

Root provisions the cache under HOME; only the post-test artifact bundle returns to `/work`.
With those inputs already present in the job, the complete invocation is:

```sh
J="$HOME/.cache/hf/ca"
python3 -B tests/client-acceptance/bootstrap.py \
  --job-isolated --job "$J" --tool-path "$PATH"
python3 -B tests/client-acceptance/run.py \
  --job-isolated --job "$J" --tool-path "$PATH" \
  --candidate "$PWD/acceptance-input/huyang-candidate" \
  --runtime "$PWD" \
  --source-commit "$ACCEPTANCE_SOURCE_COMMIT" \
  --plugin "$PWD/acceptance-input/huyang-profiles.js" \
  --plugin-sha256 e46883992727741758115492030f75bb26dbea14dccd3a4b4c3459cc5547f6c4 \
  --nvim "$ACCEPTANCE_NVIM" --strace "$ACCEPTANCE_STRACE"
```

Set `ACCEPTANCE_SOURCE_COMMIT` to the actual integrated 40-hex source SHA;
`ACCEPTANCE_NVIM` and `ACCEPTANCE_STRACE` are absolute job executable paths.
These are harness inputs, never model credentials. The parent may put the two
commands in its one existing Chaldea job after its build/provisioning steps.
Neither entry point launches Chaldea or consumes another job slot.

Parent's job-only tracing provision pins (not downloaded or executed by this
fix session): Debian `strace_6.13+ds-1_amd64.deb`, SHA-256
`cae290b67cf835350a2cde58ca332256fba475784b3e02a9a8677d73eaf47511`,
from [Debian's strace pool](https://deb.debian.org/debian/pool/main/s/strace/strace_6.13+ds-1_amd64.deb),
plus `libunwind8_1.8.1-0.1`, SHA-256
`db21a86dd05c93413f0ef36282a9c64d32410d240f332273a53be1408bec1f62`.
Parent confirmed a private ptrace/exec/loopback probe. Pass its absolute
job-private launcher as --strace when needed; the child allowlist does not
inherit LD_LIBRARY_PATH, so that launcher must supply its own private loader
configuration. No host installation is part of this workflow.

Bootstrap requires a new J. A failed run is retained; use a new J for a retry.
Bootstrap executes `npm ci --ignore-scripts --legacy-peer-deps --no-audit
--no-fund` against the committed package-lock v3, into J/tools. All lock entries
must have registry.npmjs.org URLs and SHA-512 SRIs, and the lock may not change.
Only the direct native paths are invoked:

| Client | Pin | Path below J/tools |
|---|---|---|
| Claude Code | 2.1.301 | node_modules/@anthropic-ai/claude-code-linux-x64/claude |
| Codex | 0.163.0 | node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex |
| OpenCode V2 | 2.0.30 | node_modules/@opencode/cli-linux-x64/bin/opencode |
| OpenCode plugin SDK | 2.0.30 | node_modules/@opencode/plugin |

The Codex native name is an alias to `@openai/codex@0.163.0-linux-x64`.
No PATH/installed-client fallback is allowed. Bootstrap checks exact version
output, records executable SHA-256s, and the runner verifies those hashes before
execution and after each client case. The executable hashes are sealed from
SRI-verified installation bytes at bootstrap, rather than guessed from registry
metadata, which supplies tarball hashes.

The producer pin is **gopls v0.21.1**, chosen because the bounded reader checked
this public pin and its Go 1.25 requirement. Bootstrap uses job-local
GOBIN/GOPATH/GOMODCACHE/GOCACHE, `GOTOOLCHAIN=local`, and
`go install golang.org/x/tools/gopls@v0.21.1`. It records version, module build
metadata and executable hash. To reuse a parent's already provisioned artifact,
add `--gopls-artifact ABS_PATH --gopls-sha256 EXACT_SHA256` to bootstrap; its exact
v0.21.1 gate still applies. M1's v0.23.0 cannot silently substitute for this pin.

## Runtime boundaries

Each client has a fresh fixture, supported config directories, private production
Huyang daemon/socket/state, and synthetic model key
`sk-huyang-acceptance-not-a-secret`. Child environments are constructed from
an allowlist. HOME is never assigned a new value, and no auth/config is copied
from it. Claude uses bare/dontAsk/strict MCP and no builtins. Codex uses private
CODEX_HOME/custom Responses provider and no approval prompts. Its supported
`[features] plugins=false` disables curated-plugin startup sync without redirecting
chatgpt.com or exempting external traffic. The Codex case explicitly covers **direct
tools**, not the bundled model's default `code_mode_only` mode. The same pinned
binary and `gpt-6.1-sol` slug use a job-local `model_catalog_json`. The committed
entry comes from the reader's saved source copy attributed to
[rust-v0.163.0](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/models-manager/models.json).
Only `tool_mode="direct"` and `supports_search_tool=false` change; a full parsed-object
equality check enforces those two changes, and the remaining bytes are preserved.
The exact source-copy and entry proof is recorded below. The runner guards the
entry bytes and records saved-copy provenance, original/override/catalog hashes,
the upstream reference URL and the two overrides in the case manifest and
codex-model-catalog-provenance.json. The URL is a reference, not an independent
verification of upstream raw bytes. MCP uses supported
`omit_tools_from=["deferred","code_mode"]`, `startup_readiness="connection"` and a
131072-byte input-schema threshold to preserve the actual schemas. Connection
readiness requires the live startup path rather than allowing a cached catalog
to satisfy startup. All 13 actual formal Huyang descriptors and exact schema
comparisons remain required. Job-local
`RUST_LOG=codex_core=debug,codex_mcp=debug` and `log_dir` retain startup/exposure
diagnostics for these synthetic cases only. Default code-mode execution is not
qualified by this case. OpenCode uses
private OPENCODE_CONFIG_DIR/XDG roots, codemode:false, policy restrictions and ACP.
Its provider IDs acceptance and ollama both select the openai-compatible package,
at distinct inference routes on the same numeric-loopback mock. Native Ollama
provider discovery can also issue GET `/ollama/api/tags`; the mock returns an empty
model list as an explicit auxiliary response, without advancing any workflow.
Job-only `OPENCODE_DISABLE_MODELS_FETCH=1` disables the remote models catalog.
The job config also sets `providers.lmstudio.settings.baseURL` and
`providers.vllm.settings.baseURL` beneath the same mock port. Their exact auxiliary
GET routes return empty catalogs: `/lmstudio/api/v1/models` returns `{"models":[]}`,
`/vllm/health` returns 200 `{}`, and `/vllm/v1/models` returns `{"data":[]}`.
These discovery requests are recorded separately; they never advance inference
or advertise Huyang tools. All other methods/routes retain their existing gates.
ROOT's reader checked these keys/routes at `v2.0.30` (`f16ef9ad`), including the
[pinned startup](https://github.com/anomalyco/opencode/blob/v2.0.30/packages/cli/src/server-process.ts)
and [local-provider discovery](https://github.com/anomalyco/opencode/blob/v2.0.30/packages/core/src/plugin/provider/local.ts)
implementations. No models-file schema is assumed and OPENCODE_MODELS_PATH is unset.

strace records timestamped network, process, namespace and FD lifecycle syscalls
for the actual client and service process trees (`-ff -ttt -T -y`, retained exit
records). A bounded parser reconstructs unfinished/resumed calls and retains each
exact original line. Any parser error or unsupported outbound socket shape fails.
Parser-validated incomplete named calls immediately followed by that same TID's
validated final exit retain raw records and any known destination; they cannot
establish a model connection or receive an ACP exemption. This includes a bare
`close(fd) = ?` only with one structurally valid FD, retaining its annotation and
the observed close-to-exit interval with `complete=False`. No syscall duration or
successful close is inferred. A terminal unknown syscall has no inferred
destination and requires a killed exit; `???(` followed by normal exit still
fails. Such records are retained in `terminal_incomplete_records`.
Only connect/sendto/sendmsg destination arguments are audited; payload text and
bind/listen/getsockname metadata cannot masquerade as outbound destinations.
Failed connects and EINPROGRESS remain audited, and a separate actual connect to
the selected model port is required.

The destination audit rejects every observed AF_INET/AF_INET6 destination except
the case's selected 127.0.0.1 model port and exact native ACP connect records proven
by ipc_audit.py. Captures after ACP initialize and immediately before teardown must
prove pinned executable path/SHA, process start times/ancestry, network namespace,
expected native serve child, and its FD socket inode bound to one listening
127.0.0.1 endpoint distinct from the model port. Both captures and complete
timestamped process/bind/listen/getsockname/FD lifecycle evidence must agree through
the last connect, including TID mappings. A clean proof returns exact original
(path,line) records; only those client connect records are exempt. Every returned
record must match the independently audited trace. Any proof error grants nothing.
The ACP exemption never covers service/sendto/sendmsg, discovery, external or
ambient sockets. Separately, only the exact sockaddr
`{sa_family=AF_NETLINK, nl_pid=0, nl_groups=00000000}` is recorded in
`local_kernel_queries`: it addresses the local kernel, not an IP endpoint. This
classification grants no ACP exemption, model-connect count or workflow progress.
Nonzero netlink PIDs/groups, any other netlink shape and unknown address families
remain violations, as do external/provider ports such as 1234/8000 and ambient Unix
sockets. AF_UNIX connects allow only the exact canonical case paths
`J/c/<client>/control.sock` (client MCP and service LSP relay trace trees) and
`J/c/<client>/h.sock` (client adapter trace tree only). The report records these
paths and roles in `unix_allowed_sockets`. Symlink/noncanonical aliases, sibling
cases, stale/temporary/other files anywhere in J, wrong trace roles, other outbound
calls, external, abstract, unnamed and undecodable addresses fail. Failed ambient
socket attempts, including nscd, remain violations. The complete destination
report, including violations, is retained.
Execve lines are recorded for inspection, not checked against an executable
allowlist. These are observed destination gates, not a network firewall claim.
Public downloads belong to bootstrap; client/LSP execution uses an offline Go
environment. Unknown endpoints, encodings, catalogs, auxiliary requests or client
protocols fail with raw artifacts. There is no upstream model forwarding path.

The job-local executable named `huyang` is a transparent recorder launcher.
It accepts the real `mcp` arguments, including plugin-selected --profile, and
executes the real candidate adapter. The generated wrapper embeds explicit
ACCEPTANCE_CONTROL and ACCEPTANCE_FIXTURE assignments before exec, independent
of whether a client clears its MCP child environment. The candidate adapter connects to the
explicit private production socket. Both directions of MCP are byte-forwarded
with backpressure; observers parse copies. Protocol stdout contains only the
candidate stream. Legacy initialize/initialized/list or stateless `server/discover`
with the observed 2026-07-28 metadata/version, complete tools capability and Huyang
server identity is required before list/calls. Each actual list reply and every result are checked:
exactly one TextContent, absent structuredContent, and no advertised outputSchema.
The recorder never edits a result or catalog.

## Cases and proof obligations

All three real clients run the full workflow: open/read; start and warm real
gopls; guarded create_file with replace=true and the exact read revision_id;
limit=1 search; refinement with the actual result_set_handle; diagnostics for a
real delayed finding; a quiet read; byte-for-byte argument/key replay; a stale
guard with a new key; final guarded reread. The mock never performs MCP calls.
It emits ordinary model tool calls, extracting tokens only from the tool-result
text in the actual client's next model HTTP request. Plain data:/evidence lines
are part of that visible text, not structuredContent or an independent SDK path.

Messages SSE, Responses SSE (including namespace preservation), and compatible
Chat SSE have separate encoders and shared per-case workflow state. Formal
Responses input.additional_tools descriptors are inspected alongside top-level
tools, preserving native namespaces; orchestration descriptions are never a tool
catalog or an SDK fallback. Title-only
requests have a separate, explicit auxiliary response path and cannot advance
the workflow. Missing/deferred/unknown direct tools fail rather than invoking a
fake discovery or SDK fallback.

The read's exact body drives the postimage. Replay and stale refusal must retain
file SHA, inode, size and mtime; replay must retain the original workspace and
document receipt guards. Actual MCP params/replies are matched to the mock's
calls and client-visible text; unmatched calls fail. Successful text is exact.
OpenCode's stale refusal alone may use its observed `tool.execution` JSON envelope:
exact error/content keys, empty content, and message equal to the real MCP error
text after removing its single final newline. Both raw texts and the checked mapping
are retained; the real MCP result must still be isError with one TextContent.
Refined output must reveal the second real frozen hit. The final client transcript
must carry the observed
revision/result-set/finding tokens.

The Neovim init uses the public native vim.lsp API with a real gopls process.
Push version support is negotiated and pull diagnostics are not enabled. A native
`before_init` callback clears the client pull capability after merging. Advertising
that client capability alone does not enable pull. The relay records the actual
gopls initialize result and rejects a diagnosticProvider, dynamic diagnostic
registration, or any upstream textDocument/diagnostic or workspace/diagnostic
request. Every held producer must have a successful observed initialize result.
Versioned push support remains required; no producer frame is rewritten.
`wire.py lsp` preserves Content-Length headers and body bytes in both directions.
It forwards all progress/requests/responses normally, holds only actual
postimage publishDiagnostics frames, and never constructs a diagnostic.
The MCP observer arms the gate before the edit is forwarded. Release requires
the receipt in the real client's next model HTTP request and an actual queued
gopls finding. Held/delivered SHA-256 lists must match in order and count.
A delivered versioned clean preimage publication is required before editing;
missing baseline/producer/version fails explicitly.

Full-profile evidence_get calls derive the edit transaction from the receipt's
visible evidence IDs. The diagnostics full report must match that transaction,
the exact committed revision_id and the real held document version, name gopls
as producer, and retain the complete late attribution/candidate list. The raw
diagnostic item is saved, including baseline_missing when supplied by Huyang.
Immediately after release, the workflow awaits the byte-preserving delivered
event before issuing another tool call. It scans every subsequent newly returned
tool reply, including evidence_get/search/refinement, diagnostics, replay and
refusals, and records the first notice’s exact line, attribution, model call ID
and MCP connection/request ID. A full workflow must observe that notice exactly
once and name the original transaction; any later duplicate fails. Diagnostics
collects the ledger once and verifies the item without requiring the one-shot
notice label in that particular reply. There is no diagnostic polling loop or
arbitrary sleep.

OpenCode's actual loader discovers the unchanged candidate module in its private
plugins directory. No direct setup/filterRequest call or fake hook registry is
used. A separate read-only V2 witness imports the locked @opencode/plugin and
records setup/context/session/provider/model facts. Initial config has one
managed huyang MCP registration; the real candidate transform must produce
full/lean registrations. HTTP tools must be full13 or lean3 with their actual
MCP schemas; lean additionally matches the frozen reduced M7 schema fixture.

One ACP process creates two simultaneous sessions in the same Location. A bounded
condition wait requires both actual full/lean tools/list replies before ACP prompts;
an empty primary model catalog remains a failure if the client emits one.
First A runs lean while B runs full; then A full while B lean. Both run independent
fixtures and markers concurrently. A same-provider model named ollama must
remain full; ollama/mock2 must remain lean. B switches back to full. ACP selection
responses with configOptions prove the exact selected model through the request’s
JSON-RPC/session binding; an optional sessionId must agree. A notification is not
required when the response supplies that proof. Any selection notification that
does arrive must name only the selected session and exact provider/model. If the
response omits configOptions, only an already-received matching notification can
supply proof; there is no unconditional notification wait.
An unavailable provider/model must error without inference. Lean runs
read/search/guarded edit/stale refusal/reread. Refinement, diagnostics and replay
are never sent to lean because their inputs/tools are absent by design.

## c237 runtime evidence and bounded corrections

Actual agent-a job `c2375b9f` used source `a2e4acd` and returned full-client acceptance
exit 1: Claude, Codex and OpenCode cases were all **failed**. These are provenance
cases, not a production verdict. Corrections in this harness remain unvalidated
until ROOT runs a new disposable job.

Claude used real stateless discovery; the legacy-only observer rejected full13
before forwarding it, leading to empty HTTP catalogs. Codex exposed orchestration
`input.additional_tools` with `functions.exec` and no direct full13 catalog, and
attempted external startup traffic. OpenCode inferred before its MCP catalogs
arrived, called the auxiliary tags route, advertised the client pull capability
despite the initial caps-table removal, and wrapped its real stale refusal. The old
relay rejected that capability before forwarding initialize, so c237 proves no
actual gopls initialize result or pull/push outcome. Its external
DNS/443, ambient Unix lookups and discovery ports remain audit failures unless
supported startup configuration eliminates them. Native ACP control IPC requires
the separate ownership proof below.
The observed ephemeral port `44187` was the pinned native ACP process calling its
own `serve --stdio --port 0` child's private `/api/*` endpoints. It is separate
from provider discovery. c237 has no required ownership captures/lifecycle trace,
so its audit remains failed and cannot pass retroactively. Future runs may exempt
only exact connects proven by the native ACP ownership procedure above; no port
number, API payload, or loopback address alone supplies ownership. Runtime must
confirm the configured discovery and remote-fetch controls remove their respective
ambient destinations. No substitute MCP/diagnostic result is introduced.

Focused review round 1 verified the committed Codex entry without changing it:

- Saved source copy: `/home/igor/.cache/huyang-friction/executor/cx-src/models.json`,
  472072 bytes, SHA-256
  `943ca7fe1d19ed019054158303f0dbbd80b3bef43b3aa425a71aa7cb3fb52c2b`.
- `tests/client-acceptance/codex-model-entry.json`: 66833 bytes, no final LF, SHA-256
  `873c956b8ef4766fb4e059d25924ee2c672e29149c99e1fa936e89e83410c518`.
  Its bytes occur exactly once in the saved copy at zero-based byte offset 66362
  (half-open range `[66362, 133195)`); that copy has exactly one matching slug,
  and its parsed object equals the committed entry.
- The two-field override is 66826 bytes, SHA-256
  `fddf0cf41d3c971e4734caeff58be51ed2ad25570fd0dbe6bc815b86cb3891b4`.
  The generated 66840-byte catalog has SHA-256
  `2bd3950c9f255abedd4cf7bdfb31e5091d80b4034db100a3c1e8e2eb50275558`.

These are actual UTF-8 content hashes, not Huyang document revisions. The saved
copy is the provenance evidence; upstream raw bytes were not independently
verified during this fix. The previous guard/provenance digests were incorrect
and are replaced. The exact c237 `audit-client.9239` RTM_GETADDR kernel query fits
the local-kernel classification above; c237 still fails its other gates.
`test_runtime_audit.py` adds synthetic destination/grant and real-entry catalog
regression coverage for the parent to run; no tests ran in this fix session.

ROOT separately reported all 14 helper-only synthetic tests passing on agent-a
job `69bc9da9-7452-4f98-bb50-e2b876e7c666`, exact helper source `cc7207e`.
The companion cherry-pick `7ac7c71` preserves the same tree. This is helper-unit
evidence only; no integrated or actual-client pass is claimed here.

## Bounded job 026f harness corrections

Job `026f8582` against source `893bad523d5aaa6b6a7f2021978936f0471375f0`
exposed Codex's pinned schema conversion and an OpenCode discovery-log argument
collision. This correction owns only the client harness. Its implementation parent
is `ed06ae1ff721c997fcae1387f2b95531248980d9`; apply only this commit's owned-file
delta onto M8, without importing the parent's M9 production history.

For **Responses only**, compare the raw MCP schema against the bounded port of
Codex 0.163.0's `sanitize_json_schema` followed by `JsonSchema` serde serialization.
The permitted losses are exactly `$schema`, `minimum`, `maximum`, `maxLength` and
`maxItems`, at schema positions. Every loss is recorded with its JSON-pointer path
and original value in `workflow.jsonl` under `codex_schema_projection`, together
with the projected schema hash. The port preserves compositions, infers types
using the reviewed sanitizer's hint order and fills missing object `properties`
with `{}` and array `items` with `{"type":"string"}`. It never walks enum payloads
as schemas or mistakes property names for schema keywords.

Every surviving key/value is compared exactly, including nested descriptions,
`required`, `additionalProperties`, `enum`, composition branches and `minItems`.
Tool descriptions must also match the actual MCP catalog. Require all 13 formal
function names exactly, the `mcp__huyang` namespace and an explicit boolean
`strict:false`. Missing, extra, duplicated, renamed or deferred Huyang tools fail.
Compaction has no exemption: shortened descriptions, removed properties or
constraints fail the same exact comparison. Raw `mcp-catalogs.jsonl`,
`mcp-catalog-full.json`, `http-catalogs.jsonl` and model HTTP bytes remain intact;
the projection is separate evidence, never a rewrite of either raw catalog.

This is a port for the reviewed Huyang catalog subset. Reference/definition
pruning, `const`, boolean schemas, null Option fields and any other unapproved
keyword loss fail closed. These forms are absent from the observed 13 schemas;
the pinned source supports additional lowering, but this harness does not
authorize it without new evidence. Claude and OpenCode schema comparison is
unchanged. Claude's late `document_revision=wsrev_2` failure remains a production
bug: its document-revision assertions and receipt proof are unchanged.

The two source copies were read through Huyang from
`/home/igor/.cache/huyang-friction/executor/cx-src`, a non-repository documents
workspace. Saved `tree.txt` confirms their upstream filenames under
`codex-rs/tools/src`. The case manifest's `codex_schema_conversion` records the
version, paths, allowed losses, bounded coverage and these exact SHA-256s:

- `tools_json_schema_types.rs` → `codex-rs/tools/src/json_schema/types.rs`:
  `17b11a7cf8530b9df722d382cad8066aca6c2a5d27d593e937ffdd3304f64d4a`.
- `tools_json_schema.rs` → `codex-rs/tools/src/json_schema.rs`:
  `f129a1fc19bcd20f7112c5e83f6e9f39e9b8e5f7698891c029d36527bb0387f1`.

The reference tag is `rust-v0.163.0`. These hashes identify the saved copies
actually inspected; upstream raw contents and binary/source correspondence
were not independently attested. The pinned model entry remains unchanged.

`record(path, /, **entry)` now accepts the destination positionally, so the
OpenCode discovery entry's `path` remains the native HTTP route. The exact
discovery routes/responses are unchanged. The new `test_native_schema.py`
contains an actual MCP/Responses `read` schema pair from the retained job, with
the raw artifact hashes. Negative vectors cover unapproved losses, descriptions,
required/enum/additionalProperties changes, branch loss, minItems loss,
compaction, registration names and strictness. Workflow-level vectors exercise
Responses acceptance/rejection and preserve Claude\'s unprojected comparison.
Its in-memory HTTP regression
exercises all four existing discovery routes through `Controller.http_request`
and the real record encoder; it proves the route field survives and no workflow
advances. It creates no disk files, processes, sockets or services.

For the parent's disposable verification job, the focused command is
`python3 -B -m unittest discover -s tests/client-acceptance -p test_native_schema.py`.
No tests, runtime, builds, linters, generators, clients, Chaldea jobs, SSH,
installations or services were run in this implementation session. No push or
review was performed. IPC and destination-audit code and proof were not changed.

## Bounded terminal-close and exact Unix-socket correction

The implementation parent is `aa7d11044b6f658f79897600df4f14b0b256cdbc`.
This owned-file delta is intended for cherry-pick onto M8 `c6425da`.
Job `026f8582` supplied the actual Codex bare close fixtures for TIDs 9366/9367;
they are retained as incomplete records only after the immediately following
same-TID final exit. Ordinary duration requirements, unknown-normal-exit
rejection, conservative listener-close checks, exact model-port credit and
all-or-nothing original-record ACP grants remain required.

The parent can run the focused synthetic regressions in its disposable agent-a
job with `python3 -B -m unittest discover -s tests/client-acceptance -p 'test_*audit.py'`.
No tests/builds/clients/linters/generators were executed in this implementation
session. Huyang edit receipts are not runtime verification. The separately
prepared scratch acquisition wrapper is for root review and one bounded dispatch;
it does not change the full launcher, enable NSS controls in acceptance, establish
a strace fix, or establish any native-client pass.

## Artifacts and remaining limitations

Retain J/report.json, J/failure.json when present, J/runtime-manifest.json,
J/bootstrap-report.json, J/home-*-preflight.json, J/plugin-preflight.json,
J/bootstrap/*, J/tools/package*.json, and every
J/c/{claude,codex,opencode} case directory. Case directories include:

- case-manifest.json, home-preflight.json, generated nonsecret client configs, init.lua, synthetic
  fixture bytes and private durable Huyang state;
- client stdout/stderr or duplex ACP client.jsonl, service stdout/stderr,
  model-http.jsonl with raw requests and emitted SSE/auxiliary JSON;
- mcp-wire.jsonl raw duplex bytes, mcp-catalog-{full,lean}.json,
  mcp-catalogs.jsonl (every actual connection/request catalog), http-catalogs.jsonl
  (all HTTP tools and additional_tools, including empty/auxiliary/rejected catalogs),
  events.jsonl and workflow.jsonl linking exact model/MCP calls and replies;
- lsp-wire-<pid>.jsonl with real producer upstream/held/released frame bytes,
  timestamps and hashes;
- audit-{client,service}.<pid>, destination-audit.json, plugin-witness.jsonl,
  selections.jsonl, acp-report.json, acp-ipc-initial.json, acp-ipc-final.json,
  acp-ipc-proof.json and the case report.json;
- Codex's generated direct model catalog, codex-model-catalog-provenance.json and
  job-local codex/log diagnostics (only the synthetic acceptance session).

Native packages/caches under J/tools, J/bin, J/node_modules, J/bootstrap-env and
J/preflight need not be returned if the parent archives the manifests, lockfiles
and complete case directories separately. Do not delete failed evidence.
All process/request waits are bounded, process groups are job-owned, cases have
a default 300-second deadline and model traffic has a 500-request ceiling.

No bootstrap/client/producer/ACP execution, build, test, push, installation or
service restart was performed in the implementation session. Runtime must still
establish the native package paths/loader behavior, Claude's bare MCP behavior
under UID0, Codex's exact namespace and SSE parser contract, ACP response/optional-notification
shape and ordering, gopls's versioned empty baseline/push and Huyang late ingestion.
The OpenCode provider/config keys, witness ctx.session.hook API, any runtime
package-resolution attempt, Claude --permission-prompts none, and strict Codex
feature keys remain runtime compatibility questions. HOME discovery and ambient
Unix-socket refusals may expose additional client behavior; those failures remain
explicit artifacts, not reasons to use installed configuration or broaden access. Unsupported shapes fail;
no SDK/stub success is substituted. The harness covers these specified protocols,
not every client extension, compression encoding or auxiliary model operation.
Binary-to-source provenance is the parent's build responsibility; hashes and the
provided source SHA are recorded, not an independent build attestation.
The parent reported M7 core rebase `10f9774fccb1790fdce144c8b5dd8b25f0221bfd`
and M8 author fix `308bac6` separately; neither is integrated into this harness
branch. Its runtime source/renderer compatibility and core catalog budget remain
the parent’s review/runtime gates. No installed host was updated.

Primary references: immutable npm version metadata
[Claude native](https://registry.npmjs.org/@anthropic-ai/claude-code-linux-x64/2.1.301),
[Codex](https://registry.npmjs.org/@openai/codex/0.163.0),
[Codex native alias](https://registry.npmjs.org/@openai/codex/0.163.0-linux-x64),
[OpenCode native](https://registry.npmjs.org/@opencode/cli-linux-x64/2.0.30),
[plugin SDK](https://registry.npmjs.org/@opencode/plugin/2.0.30);
[Anthropic SSE](https://platform.claude.com/docs/en/build-with-claude/streaming),
[Responses events](https://developers.openai.com/api/reference/resources/responses/streaming-events),
[OpenCode ACP](https://dev.opencode.ai/v2/docs/cli/acp/),
[gopls metadata](https://proxy.golang.org/golang.org/x/tools/gopls/@v/v0.21.1.info).
Candidate plain grammar and the in-process-only late fixture remain in
[plain_reply_test.go](../../internal/service/plain_reply_test.go); that fixture
does not satisfy this actual-producer acceptance gate.
