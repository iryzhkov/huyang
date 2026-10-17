# Diagnostics round 2

Worktree: /home/igor/.t3/worktrees/huyang/t3-bc2c2ea1  
Branch: t3/fix-diagnostic-delta  
Base: 9ef5e98 (round 1); installed-build observations supplied by the task were from ef16bef.  
Date: 2026-10-16/17, America/Los_Angeles.

The version-proof, coordinate, baseline and late-delivery changes are implemented.
The private nine-language integration run passes its assertions, including honest
provisional outcomes. It does **not** certify clean resolution for TypeScript,
JavaScript or Java: their installed producers omit publication versions, and no
current-version clean proof was available.

## Root causes

Line references in this section name the indicated baseline revision.

1. **Line +1 twice.** ef16bef: `lua/huyang/edit.lua:1001` and `:1117`
   already normalize LSP coordinates to one-based lines.
   `internal/handlers/edit.go:493` and
   `internal/handlers/prepared_delta.go:92` added another one.
   `notices.go` did not add it. All three renderers now share
   `diagnosticLine`, which preserves the normalized coordinate.

2. **Unproven evidence could produce an authoritative clean verdict.**
   ef16bef: `internal/workspace/evidence.go:329` accepted ChangeBarrier even
   when the diagnostic publication version was absent or old; `:335` accepted
   a pull result ID without document-version proof.
   `lua/huyang/edit.lua:1202` accepted an unversioned publication as fresh;
   `:1210` then read findings from Neovim's diagnostic cache rather than from
   the publication carrying that version. `:1227` allowed a syntax barrier
   to suppress the progress veto.
   `internal/providerpool/evidence.go:83` overwrote the report for each batch,
   returning only the last batch's confidence and edit delta. Missing documents
   were not independently accounted for.

   A separate live transport trace established a Neovim 0.12.5 issue:
   initial loading/reloading sent didOpen(version=0), whereas Huyang expected
   changedtick=2/4. Gopls v0.23.0 omits version zero from its JSON publication.
   Reloading with :edit! also restarts document versions, allowing publications
   from different open lifetimes to collide. The fix seeds a nonzero initial
   version before attachment, uses Neovim's actual LSP version table, and updates
   buffers in place. didSave follows the disk edit, after didChange is flushed,
   so rust-analyzer's compiler diagnostics run again.

3. **TS/JS false resolution.** The combination of `edit.lua:1202`,
   `:1225` (Complete = any publication), the ChangeBarrier bypass above,
   and the ledger's resolve-absent rule allowed an empty unversioned
   publication/cache snapshot to retire every previous finding. A later error
   publication then became “new” again. This explains the observed flip without
   any error-line change. The old path did not preserve enough publication
   provenance to identify which TypeScript analysis phase supplied each empty
   set; the intermediate/partial-phase explanation is an inference, not a
   captured server trace. The new path binds raw findings to their publication
   version, rejects unversioned cleanliness, and gives versioned empty pushes
   a bounded 150 ms quiet period. Upstream also tracks missing TypeScript
   publication versions in [typescript-language-server issue 983](https://github.com/typescript-language-server/typescript-language-server/issues/983).

4. **Java late/stale handling.** ef16bef: `edit.lua:883` used a synthetic
   “#late” producer identity, different from the regular “#client-id” identity.
   The ledger therefore could not resolve regular findings with a late clean
   batch. `:880` consumed the pending observer on its first publication;
   observers were armed only for attachment failures, not ordinary missing
   current proof. Old/mismatched versions could also consume that observer.
   Late evidence now preserves producer identity and continues observing the
   same edited version. A proven-current empty batch resolves stale findings.
   Unversioned positive findings arrive provisionally without transaction
   attribution; unversioned emptiness never resolves anything. Late pull
   responses are retained too, provided the requested buffer has not changed.

5. **First-touch errors called new.** 9ef5e98:
   `internal/workspace/evidence.go:582` announced every previously unseen ID,
   and the edit handler had no pre-edit provider snapshot.
   A first observation was being presented as evidence of introduction.
   An existing provider now supplies a nonblocking snapshot of current evidence
   for already-loaded, unchanged workspace buffers. These findings seed the
   ledger without new announcements. Otherwise first-touch findings carry
   `baseline_missing: true`; the edit summary, when authoritative, says
   “new or pre-existing (no baseline)”. Compact diagnostics, diagnostic_updates,
   and the output schema preserve that flag. Native reads alone do not
   manufacture a semantic baseline.

## Implementation details

- Push, pull and per-document workspace evidence need publication/request
  version >= the expected LSP version, completeness, and no pending progress.
  A syntax request acknowledgement is insufficient.
- Explicitly old findings cannot refresh stale items or resurrect resolved IDs.
  Late clean resolution uses the same normal ledger path and proof rule.
- Reports aggregate all edited documents, findings, resolutions, evidence IDs,
  and the weakest confidence. Missing document batches create provisional proof
  gaps. Coverage keys include producer **and document**; old persisted producer
  dimensions migrate using their retained evidence.
- Providers with both pull and push select a consistent successful stream per
  document/client. This matters for Rust: rustc push and rust-analyzer pull use
  different messages for the same error, so alternating streams caused false
  new/resolved transitions during an early private probe.
- LSP waits yield through the kernel coroutine machinery. Pull responses that
  miss the edit deadline can still become late evidence; responses for
  superseded bytes are discarded.
- No language-server status or installation policy was changed.

## Before: task-supplied installed-build observations

All nine languages had the one-line coordinate offset and first-touch D findings
called new. A dash means the task did not specify that outcome; it is not a pass.

| Language | A / reintroduction | B | C | D |
| --- | --- | --- | --- | --- |
| Go | — | — | — | Called new |
| Rust | A missed, arrived one edit late; reintroduction silent | A finding arrived late | — | Called new |
| Python | — | — | — | Called new |
| TypeScript | Reintroduction silently clean | False resolved while error remained | — | Called new |
| JavaScript | Reintroduction silently clean | False resolved while error remained | — | Called new |
| Java | A provisional attach timeout; reintroduction silent | Late finding arrived | Stayed stale indefinitely | Called new |
| C++ | — | — | — | Called new |
| C | — | — | — | Called new |
| Bash | — | — | — | Called new |

## After: final private build

Final all-language run: `TestLangprobeABCD`, 131.608 s, all nine subtests passed.
“Retained stale” means C is still provisional and no resolution is claimed.
“Honest” means D was identified as pre-existing by a real baseline or marked
new-or-pre-existing with baseline_missing; all final D findings used the latter.
All reintroduction findings were delivered immediately or through late evidence;
none returned an authoritative silently clean edit.

| Language | A reported | Line correct | B new / false resolved | C | D label | Provisional reasons | A latency |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Go | Immediate | Yes | 0 / no | Resolved | Honest | None | 264 ms |
| Rust | Late; provisional reply | Yes | 0 / no | Resolved | Honest | work_done_progress_pending on A/reintroduction | 261 ms |
| Python | Immediate | Yes | 0 / no | Resolved | Honest | None | 150 ms |
| TypeScript | Immediate | Yes | 0 / no | Retained stale | Honest | push_missing_current_document_proof | 1626 ms |
| JavaScript | Immediate | Yes | 0 / no | Retained stale | Honest | push_missing_current_document_proof | 1600 ms |
| Java | Late; provisional reply | Yes | 0 / no | Retained stale | Honest | push_missing_current_document_proof | 1613 ms |
| C++ | Immediate | Yes | 0 / no | Resolved | Honest | None | 139 ms |
| C | Immediate | Yes | 0 / no | Resolved | Honest | None | 76 ms |
| Bash | Immediate | Yes | 0 / no | Resolved | Honest | None | 719 ms |

The integration test permits a provisional C when the producer cannot prove a
current clean publication, but rejects an authoritative clean claim with stale
findings. It checks A reporting/provisional status, eventual positive findings,
coordinates, B announcements/resolutions, D labelling, and reintroduction.

Artifacts are retained at
`/home/igor/.cache/huyang-friction/langprobe-r2/validated/`:
per-language `*-replies.jsonl` and `*-result.json`, copied/reset Git projects,
and `service.log`. Earlier exploratory runs occupy separate sibling directories.

## Validation and reproduction

Regression tests were added before the corresponding fixes and initially failed
for stale-version barrier acceptance, old clean resolution, earlier-batch delta
loss, and missing edited-document coverage.

Go tests:
- TestDiagnosticPublicationVersionProof (push, pull, workspace; absent/old/current/later versions)
- TestOldCleanPublicationCannotResolveAndLateCurrentCleanDoes
- TestRecordDiagnosticsAggregatesAllDocumentsAndDeltas
- TestMissingEditedDocumentBatchKeepsReplyProvisional
- TestFirstTouchWithoutBaselineIsLabelledAndCleanBaselineProvesNew
- TestPreEditBaselineDoesNotAnnouncePreexistingFinding
- TestLegacyProducerCoverageMigratesToDocumentProof
- TestDiagnosticLinePreservesOneBasedProviderCoordinates
- TestOwnedProviderAdvertisesDiagnosticPublicationVersions
- TestLangprobeVersionNegotiationAndBaselineLatency (opt-in real gopls)
- TestLangprobeABCD (opt-in nine-language private-service test)

Lua unit checks additionally cover publication payload/version binding,
nonblocking baselines, unversioned emptiness, repeated late clean publications,
unattributed provisional late findings, delayed pull completion, and rejection
of a pull response after another buffer change.

Checks passed:
- `HUYANG_FRICTION=0 go test ./...`
- `go vet ./...`
- `gofmt -l cmd internal` produced no output
- `git diff --check`
- `nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_edit.lua`

The private binary was built with:
```sh
go build -o /home/igor/.cache/huyang-friction/build/huyang ./cmd/huyang
```

The opt-in test starts that binary with
`serve --socket /home/igor/.cache/huyang-friction/build/sock --state-dir /home/igor/.cache/huyang-friction/build/state`
and drives
`mcp --socket .../sock --profile edit` over persistent stdio JSON-RPC
(initialize, initialized notification, tools/call). It clones the supplied
projects locally, resets the copies with Git, and shuts its service down.
Without a source directory it generates portable tiny Git fixtures; without a
binary override it builds a temporary private binary. Missing installed servers
are skipped, and the whole test is skipped unless HUYANG_LANGPROBE=1.

```sh
TMPDIR=/home/igor/.cache/huyang-friction/build \
HUYANG_FRICTION=0 HUYANG_LANGPROBE=1 \
HUYANG_LANGPROBE_BINARY=/home/igor/.cache/huyang-friction/build/huyang \
HUYANG_LANGPROBE_SOURCE=/home/igor/.cache/huyang-friction/langprobe \
HUYANG_LANGPROBE_OUTPUT=/home/igor/.cache/huyang-friction/langprobe-r2/validated \
HUYANG_LANGPROBE_SOCKET=/home/igor/.cache/huyang-friction/build/sock \
HUYANG_LANGPROBE_STATE_DIR=/home/igor/.cache/huyang-friction/build/state \
go test ./cmd/huyang -run TestLangprobeABCD -count=1 -v -timeout=25m
```

## Latency and remaining limits

The warm pre-edit kernel snapshot took 0.20–0.41 ms in focused local probes;
the final measured call was 0.266 ms. It adds one local provider RPC and ledger
persistence for current loaded documents, with no LSP requests, attachment, or
analysis wait. Ledger disk I/O was not separately timed. A cold provider adds no
baseline RPC. Versioned empty pushes now require 150 ms of quiet within the
existing shared 1.5 s evidence budget; that is distinct from baseline overhead.
The table reports measured edit A wall times, not just the snapshot cost.
The supplied 34 s installed Rust observation had different startup conditions,
so no controlled speedup ratio is claimed.

TS/JS/jdtls still cannot prove clean current versions on this host. Their
unversioned positive observations are delivered provisionally; clean empty
observations cannot safely resolve stale findings. Resolving those cases requires
a producer with stamped diagnostics or a reliable semantic pull/validation
response. A documentSymbol acknowledgement or silence is deliberately not used
as a substitute. This is the remaining functional limitation of task (c), not a
claim that Java's clean resolution has been fully fixed live.

Round 1's downgrade warning still applies to retained “persisting” notices.
No fleet distribution occurred. Work stayed local; nothing was pushed, installed,
merged or published, and the installed Huyang binary/service and Ollama/GPU were
not changed.
