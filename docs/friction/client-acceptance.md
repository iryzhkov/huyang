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
CODEX_HOME/custom Responses provider and no approval prompts. OpenCode uses
private OPENCODE_CONFIG_DIR/XDG roots, codemode:false, policy restrictions and ACP.
Its provider IDs acceptance and ollama both select the openai-compatible package,
at distinct routes on the same numeric-loopback mock; neither uses an Ollama API.

strace records connect/sendto/sendmsg/execve for the actual client and service
process trees. The destination audit rejects every observed AF_INET/AF_INET6
destination except the case's selected 127.0.0.1 model port. Every observed AF_UNIX
destination must be an absolute path resolving inside J; external, abstract,
unnamed and undecodable addresses fail. This includes failed ambient socket
lookups. The complete destination report, including violations, is retained.
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
candidate stream. Initialize/initialized/list and every result are checked:
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
Chat SSE have separate encoders and shared per-case workflow state. Title-only
requests have a separate, explicit auxiliary response path and cannot advance
the workflow. Missing/deferred/unknown direct tools fail rather than invoking a
fake discovery or SDK fallback.

The read's exact body drives the postimage. Replay and stale refusal must retain
file SHA, inode, size and mtime; replay must retain the original workspace and
document receipt guards. Actual MCP params/replies are matched to the mock's
calls and client-visible text; unmatched calls fail. Refined output must reveal
the second real frozen hit. The final client transcript must carry the observed
revision/result-set/finding tokens.

The Neovim init uses the public native vim.lsp API with a real gopls process.
Push version support is negotiated and pull diagnostics are not enabled.
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

One ACP process creates two simultaneous sessions in the same Location. First A
runs lean while B runs full; then A full while B lean. Both run independent
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
  events.jsonl and workflow.jsonl linking exact model/MCP calls and replies;
- lsp-wire-<pid>.jsonl with real producer upstream/held/released frame bytes,
  timestamps and hashes;
- audit-{client,service}.<pid>, destination-audit.json, plugin-witness.jsonl,
  selections.jsonl, acp-report.json and the case report.json.

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
