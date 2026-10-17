# Nine-language native observation capture

This is observation-only test instrumentation based on
`72cbe76c9265a7bbd972b9e4d7bd942f35dffdd7`. It does not fix or clear the
failed nine-language acceptance run.

The diagnosis used the complete Rust/Python MCP transcripts and persisted
diagnostic ledgers from agent-a job `991dd73a-cb7d-4bbc-9b44-ab4198481b44`,
source `a2d0d7cb8f975e8988726970872dd7b456608159`. Its original
`hf-nine-artifacts/results.tar.gz` has SHA-256
`ee6392e41087ad60e1ed02f4840c2dab78cf5df72babcdc8fcfe4a9b06ccaf8a`.
Rust D's two findings arrived after the fixed collection window, with the
original transaction and baseline-missing label. Python's ledger contains
five empty provisional pulls and no findings. Neither observation identifies
the missing native acquisition events. The separate full15 M9 matrix has no
result available to this implementation task.

The existing private generated init loads `tests/langprobe-native-trace.lua`
before enabling its unchanged seven native server configurations. A generic
on-init observer precedes the existing callbacks. It wraps native client
requests, callbacks, cancellations, notifications, current handler resolution
for publish/refresh/progress, client errors and exit. It forwards the original
arguments, nil return slots and thrown error values. It preserves configured
handlers, settings, roots, capabilities and existing callbacks. The original
nine-language Go test and all fixture bytes, assertions, eight/thirty attempts
and timings are unchanged.

Each event identifies the Neovim PID and sequence. Client events identify the
client and request ID where available. A per-client request sequence also
correlates a synchronous callback that precedes the native dispatch return.
Server PID is null when Neovim does not expose it; the native command is
recorded at client initialization. Local buffer versions, changedticks,
postimage and disk hashes are correlation snapshots, never producer proof.
Producer publish bodies and versions, refresh/progress messages and outgoing
didOpen/didChange/didSave bodies retain their native values.

The observer also records late-evidence RPC starts, cancellation, complete
success/error envelopes and send returns. It installs that hook on natural
loading of `huyang.rpc`, without loading production modules early. A recorded
Lua send is not proof of Go receipt or ledger insertion. Failures before Lua
entry remain outside this observation boundary.

The existing report includes `native-observation/` with the generated init,
observer source, one native JSONL and one native debug LSP log per Neovim PID,
and a manifest with file sizes, hashes, truncation/failure state and late RPC
outcomes. The debug log path/level changes only local observation logging.
Native logging adds overhead; no timing threshold has been changed.

Bounds are sixteen processes, 8 MiB per JSONL and per LSP log, 4,096 events per
JSONL, 256 KiB per encoded event, 64 KiB per local document snapshot, thirty-two
attached-buffer snapshots per event, 256 KiB each for the init and hook, and
4 KiB per retained failure marker and 16 MiB for the manifest. An oversize
manifest discards its duplicate outcome summary with an explicit failure; raw
events remain in JSONL. Oversize input files retain a labelled prefix.
Every truncation, missing/malformed capture, observer failure marker or
unfinished late collection fails report collection and the script exit.
Observation errors stop that observer's capture and preserve native forwarding.
The live files stay outside the archived output directory; only bounded copies
enter the existing report.

No build, test, formatter, linter, generator, native client, service,
provisioning or Chaldea command was executed by the implementation worker.
Root must source-verify the exact implementation revision on agent-a, compile,
then capture Rust/Python with `LANGPROBE_LANGUAGES=rust,python` and an explicit
partial label. Inspect the complete observation manifest and native bodies
before the unchanged strict nine-language acceptance run. A partial capture
does not count as acceptance.
