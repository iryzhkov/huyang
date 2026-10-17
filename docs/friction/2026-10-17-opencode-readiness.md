# OpenCode prompt readiness witness, 2026-10-17

This is a source-only M8 acceptance-instrumentation change based on parent
`89cbead1f2f5e990b140e65e8aea7f4f2b860edf`. Product source remains
`9a2c9d6ffc5e50e1a29799e13a940cb6fd9ddbd3`. No client, test, parser,
syntax checker, formatter, build, provisioning, service, or Chaldea command was
run for this implementation. Root owns execution, independent review and GitHub.

The original failed run is retained at
`/home/igor/.cache/huyang-friction/executor/client-93ac-artifacts/ca/c/opencode`.
Both supplied reports were read in full:

- `/home/igor/.cache/huyang-friction/client93-opencode-native-readiness-barrier.md`
- `/home/igor/.cache/huyang-friction/client93-opencode-catalog-diagnosis.md`

Adapter catalog observation happens before forwarding the bytes to native
OpenCode. Native MCP status and connection events also precede a separately
debounced registry rebuild. The existing wire wait remains an additional
observation. It cannot prove that a native request snapshot has the tools.

The witness now registers an awaited public prompt hook and a read-only tool
transform. Setup registers these observers and finishes without reading or
waiting for readiness. Each prompt then requires the genuine final registry to
contain exactly three lean and thirteen full definitions in their managed
namespaces, with exact native IDs/names and `options.codemode === false`.
Unrelated definitions remain untouched. The observer checks input definition
shape and the adapter-wrapped execute function without invoking or replacing
either. The Promise adapter always wraps execute, so hasExecute proves only
the returned shape, not the underlying native executor. Real workflow tool calls
remain the proof that native execution works.

The synchronous transform only increments a local event generation and
resolves one pending wake per prompt. It never accesses the editor, reads or
reloads the registry, logs, or changes tools. The prompt arms its wake before
calling `ctx.tool.list()`, then compares the generation after that asynchronous
read. A changed generation forces another genuine read. Repeated notifications
coalesce; an unchanged incomplete observation waits for a rebuild event.

There is no permanent ready bit. Every prompt, including all provider/model
switches, gets its own deadline and proof. Thirty seconds is a failure deadline,
never a readiness condition or a warming delay. Failed registry reads, invalid
definitions, deadline expiry and disposal reject before this prompt can be
admitted. Failed transform registration retains a rejecting prompt hook.

Witness entries retain session ID, native message ID, local prompt sequence,
checked/current event generations, observed IDs/names/namespaces/codemode and
definition/executor presence, plus ready or failure outcome. Executable tools
and prompt contents are not serialized. The seven scripted prompts require
seven distinct native message proofs attributed three to A and four to B.
Failure entries are also copied into the final ACP report on early rejection.
Final ownership capture is written before witness parsing. Missing, malformed,
truncated, empty or non-object witness data leaves the raw file untouched and
records a witness_parse_error in the ACP report alongside any parsed prefix.
That error fails acceptance after both reports are written. If an exception is
already in flight, it is recorded as original_failure and continues to propagate
without replacement by the witness error.

The generated plugin imports
`../acceptance-support/opencode_readiness.mjs`. Its exact helper bytes are
copied beneath `oc-config/acceptance-support/`, outside native plugin discovery
and inside retained case directories. The new tracked helper/test files fall
under the existing full tracked-source manifest. Candidate plugin copy/hash
checks and the existing context witness remain in place.

Public API and ordering proof uses only official source at the immutable
OpenCode 2.0.30 reference commit
`f16ef9ad8878279ea2be3cfab7baeed97aef61aa`. The implementation consulted these
sources as text; it did not install or execute them. Every range below was
independently re-read against the complete cached pinned bytes under
`/home/igor/.cache/huyang-friction/review-oc-src/` after main review round 1.
The corrected anchors use physical source lines, including blank lines:

- [ToolDomain](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/plugin/src/promise/tool.ts#L66-L72)
  exposes final-registry `list()` and synchronous transform callbacks.
  [Options](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/schema/src/tool.ts#L27-L42)
  put namespace/codemode in options.
  [Info](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/schema/src/tool.ts#L92-L102)
  has name, input and execute.
- [MCP registration](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/core/src/tool/mcp.ts#L41-L49)
  uses the unqualified MCP name and server namespace.
  [Effective names](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/core/src/tool/runtime.ts#L276-L281)
  normalize the name and prefix the namespace. These managed names need no
  normalization changes.
- [State rebuild](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/core/src/state.ts#L172-L194)
  runs transforms synchronously before committing the new value. Resolving a
  Promise wake schedules its continuation after this synchronous callback.
  [Tool list/snapshot](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/core/src/tool.ts#L224-L235)
  read the same state; codemode false selects direct tools.
- [SessionPrompt](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/plugin/src/promise/session.ts#L14-L20)
  has session/message IDs and delivery, with no model or abort signal.
  [Hook registration](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/plugin/src/promise/registration.ts#L1-L21)
  returns an asynchronous dispose method.
- [Promise adaptation](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/plugin/src/promise/adapter.ts#L591-L595)
  awaits the callback Promise; rejected session hooks propagate as defects,
  not successful empty requests.
  [Tool list adaptation](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/plugin/src/promise/adapter.ts#L479-L482)
  wraps every returned execute member in a function.
  [Hook dispatch](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/core/src/plugin/hooks.ts#L88-L95)
  awaits callbacks without swallowing their result.
- [Prompt preparation](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/core/src/session/prompt.ts#L29-L51)
  awaits activation and the prompt hook.
  [Session admission](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/core/src/session/session.ts#L145-L178)
  follows successful preparation; execution wake follows admission.
  [Context selection](https://github.com/anomalyco/opencode/blob/f16ef9ad8878279ea2be3cfab7baeed97aef61aa/packages/core/src/session/context.ts#L121-L143)
  subsequently takes the native tool snapshot.

These sources establish ordering for fresh prompts in idle acceptance sessions,
not a lease against later reloads or disconnections. The genuine first primary
HTTP catalog/schema assertions for initial A/B and every later switch remain
mandatory. Permissions and the unchanged candidate context filter still decide
the selected request tools. No failed first request is ignored or retried to
warm discovery.

The public Promise prompt API cannot notify this JavaScript waiter of native
per-prompt interruption. It supplies no supported abort field. Native
interruption remains owned by the interruptible preparation effect; it does not
admit a cancelled preparation. Any leftover JavaScript wait ends at its own
deadline or plugin cleanup. Cleanup cancels all pending waiters before disposing
registrations. The tests exercise this supported cleanup path, not an invented
prompt cancellation option. There is no claim of immediate JavaScript
cancellation on ACP interruption, or cancellation of an already-running turn.

Pure Node fake-SDK tests cover setup without waiting, delayed native publication,
initial readiness, stale reads/lost wakeups, notification coalescing, concurrent
session proofs and separate deadlines, rechecking later prompts, malformed
managed catalogs, genuine read errors, disposal, deadlines including hung reads,
late resolution after failure, and failed observer registration. Fake editors
reject every access; native tool definitions are frozen and executors fail if
called. The implementation author has not run these tests. The added pure
Python witness-retention regression mocks all file IO and checks malformed,
truncated, missing, empty and non-object witness data, report preservation,
acceptance failure, original exception identity and valid witness handling.

Root reports that agent-a job `c4a8f5f7` passed 21 Node and 94 Python tests
at `e14ad5929d812a3bc2c86a13418e0a16a0f47e29`, 115 total. Its complete
4,073-byte evidence has SHA-256
`c9fd418085b1dd1f57c948f320f5ab6386d1ac6cdffc9f696c252a2bb4e0bdb1`.
That revision-specific evidence does not validate this follow-up or qualify
native runtime acceptance.

Parent validation must follow root's agent-a procedure: preserve the supplied
on-disk HOME at `/home/job`, assert it before writing, and put TMPDIR beneath
that HOME. After pinning the final integrated revision:

```sh
chaldea exec --host agent-a -- sh -c '
  set -eu
  test "$HOME" = /home/job || exit 1
  export TMPDIR="$HOME/.cache/huyang-friction/opencode-readiness-witness-fix/tmp"
  mkdir -p "$TMPDIR"
  node --test tests/client-acceptance/test_opencode_readiness.mjs
  PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests/client-acceptance -p test_opencode_witness.py
'
```

Root should then run its existing authorized native acceptance command with the
unchanged source identity, pins, raw producer/HTTP evidence, NSS boundary,
ACP ownership and strict network checks. It must retain helper bytes and witness
generations alongside the first primary request for all seven prompts.
No extra outbound destination is introduced by the waiter.

Native unknown terminal IPC remains a separate FAIL with no waiver. Reply
mapping/fatal-workflow bookkeeping belongs to the other worker. Pending M9
Python policy decision 8596 is unrelated. This source change makes no runtime,
review, CI, deployment or overall M8 acceptance claim.
