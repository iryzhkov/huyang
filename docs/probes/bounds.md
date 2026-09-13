# Probe report: bounds, budgets and degenerate input

Live-probed 2026-09-12 from `internal/livetest/probe_bounds_test.go`
(`go test -tags live ./internal/livetest -run 'TestProbe' -v`):
16 tests, 8 fail, 8 pass. Nothing took more than ten seconds; the slowest
call was the 100k-match search at 3.1 s. The service state directory did not
grow on any read or search (270 B before and after). Each test starts its own
daemon and works on a copy of the `go` fixture, so none of this touches
canonical data.

Promise under test: every store and every reply is bounded by a named
constant, and a reply that was cut says so.

## 1. Read with no max_lines is byte-unbounded (FAIL)

Test: `TestProbeRead50000LinesIsUnbounded` (fails).

Calls: create `big.go` (50 000 comment lines, ~2.2 MB), `workspace_open`, then
`read {workspace_id, target: {path: big.go}}` with no `max_lines`.

Happened: `outcome=ok`, 2 350 376-byte reply, 2 300 013 bytes of content,
5 002 lines, no `truncated` flag, 212 ms.

Expected: the guide promises "no size cap unless max_lines is set", which is
honest about the missing bound, but a 2.3 MB blind dump with no weight flag
means an agent that asked for a file gets a reply whose size it never agreed
to. `readMany` lists every target's bytes under `entries` ahead of the
bodies; the single-target reply has no equivalent size-up-front field beyond
`lines`, which says nothing about bytes.

Matters because: context budgets are measured in bytes/tokens, not lines.
An agent reading an unfamiliar file has no way to ask "how big is this"
before paying for it.

## 2. A 5 MB file reads in one reply with no flag (FAIL)

Test: `TestProbeRead5MBFileIsUnbounded` (fails).

Calls: create `big5.go` (~5.2 MB, under the 32 MB `MaxBytes` read limit),
`workspace_open`, `read` with no `max_lines`.

Happened: `outcome=ok`, 5 350 214-byte reply, 5 242 853 bytes of content, no
`truncated` flag, 488 ms.

Expected: either a byte cap with a `truncated` flag (the way `max_lines`
caps lines), or at minimum a size disclosure. The only read limit is the
32 MB `MaxBytes` refusal, so anything under 32 MB arrives whole and silent.

Matters because: the failure mode is a 30 MB reply the agent did not ask
for, one line below the refusal threshold. The cliff edge is invisible until
it is hit.

## 3. One 2 MB line defeats max_lines (FAIL)

Test: `TestProbeSingle2MBLineDefeatsMaxLines` (fails).

Calls: create `longline.go` (one 2 MB line plus newline), `read` with
`max_lines: 100`.

Happened: `outcome=ok`, 2 097 521-byte reply, `truncated` absent,
`lines=1`.

Expected: `max_lines=100` on a one-line file is trivially satisfied, so the
whole 2 MB line is delivered. `capLines` counts newlines; nothing caps a
line. A minified JS bundle, a long base64 blob, or a generated single-line
file passes straight through the only read bound that exists.

Matters because: `max_lines` is the bound the guide tells agents to use
against big files, and it is a line bound on a byte problem. An agent that
set the cap did everything right and still got 2 MB.

## 4. Outlines are uncapped: 3000 declarations, no flag (FAIL)

Test: `TestProbeOutlineOfManyDeclarationsIsUnbounded` (fails).

Calls: create `manydecl.go` (3000 Go functions), `read` with
`view: outline`.

Happened: `outcome=ok`, `declaration_count=3000`, 386 703-byte reply, no
truncation flag of any kind, 276 ms.

Expected: every other list-shaped reply is capped and says so (search hits
default 50/100, verify lists 20, structured entries 100 with
`*_truncated` flags). `CompactOutline` loops over all sections with no cap,
so a generated file's outline can exceed the file it summarizes, and the
caller cannot tell a complete outline from a cut one because the cut never
happens.

Matters because: the guide recommends "outline first, then windows" for
files over ~500 lines, i.e. exactly the files whose outlines can be huge.
The recommended cheap call is unbounded.

## 5. 100k matches: total reports the cap, 99k matches are invisible (FAIL)

Test: `TestProbeSearch100kMatchesHidesTheTotal` (fails).

Calls: create `many.txt` (100 000 `needle` lines, 700 KB), `search
{query: needle}` with the default limit.

Happened: `outcome=ok`, summary `"1000 matches; search coverage
incomplete"`, `returned=50`, `total=1000`,
`coverage={capped:true, complete:false, files_read:3, bytes_read:700253}`,
warning `"response limited to 50 of 1000 matches"`, 3.1 s.

Expected: `total` should be the number of matches in the workspace, or say
it is a lower bound. The store stops collecting at `MaxMatches` (1000) and
`total` is set to `len(hits)`, i.e. the cap. `coverage.capped=true` says
*something* was cut but quantifies nothing: the ~99 000 dropped matches are
not counted, estimated, or hinted at, and the warning counts "of 1000" as
if 1000 were the total.

Matters because: an agent deciding whether to refine, narrow paths, or
trust the result is told there are 1000 matches when there are 100 000. The
refine loop converges on a lie.

## 6. context_lines is unbounded and multiplies per hit (FAIL)

Test: `TestProbeSearchContextLinesHasNoCap` (fails).

Calls: create `ctx.go` (15 714 bytes, 3 widely spaced `marker ALPHA` lines),
`search {query: "marker ALPHA", context_lines: 1000000}`.

Happened: `outcome=ok`, 3 hits, 54 781-byte reply (3.5x the file), no cap,
no flag.

Expected: `argInt(arguments, "context_lines", 0)` is passed straight to
`attachSearchContext`, which clamps only to the file bounds. Each hit
carries ~the whole file; N hits carry it N times. There is no maximum and
no `context_truncated` disclosure.

Matters because: context is the feature that lets one search replace
grep-plus-read. An agent passing a large `context_lines` (or a large limit
with modest context) gets a reply that repeats the file per hit, defeating
the bound the `limit` was supposed to provide.

## 7. Binary files are skipped by search with no coverage trace (FAIL)

Test: `TestProbeInvalidUTF8AndBinaryGoRefused` (fails).

Calls: create `binary.go` (NUL bytes) and `invalid.go` (0xFF 0xFE, no NUL),
read each, then `search {query: Total}`.

Happened: both reads correctly fail with `failed/read_failed` and "is
binary, not regular text". The search returns 6 matches with **no
`coverage` block at all**.

Expected: the over-budget branch of `readSearchable` sets
`Complete=false, Capped=true` and appends to `Skipped`; the binary branch
returns `false` without touching coverage. Since the handler only attaches
coverage when `!Complete`, a search that skipped files looks exactly like a
search that read everything.

Matters because: this is the one place where "a reply that was cut must
say so" is inverted -- the reply was cut and says nothing. An agent cannot
distinguish "no matches in binary.go" from "binary.go was never
searched". Any fixture with a binary or invalid-UTF-8 file silently
narrows every search.

## 8. A BOM leaks into line 1 with no disclosure (FAIL)

Test: `TestProbeBOMLeaksIntoContent` (fails).

Calls: create `bom.go` (UTF-8 BOM + a small package), `read` it.

Happened: `outcome=ok`, content starts with U+FEFF (`"\ufeffpackage
bom\n\n"`).

Expected: the BOM is valid UTF-8 so the file is text, but the delivered
line 1 is not the file's line 1 for any prefix match, symbol lookup, or
`replace_literal` the agent derives from it: `old: "package bom"` will not
match content that begins `\ufeffpackage bom`. Either strip the BOM or
disclose it.

Matters because: the agent's next edit targets bytes it was shown, and the
bytes it was shown contain an invisible character. The resulting
`replace_literal` refusal will show `actual` text that looks identical to
what the agent sent.

## Bounds that held (all PASS)

- `TestProbeReadWindowPastEnd`: `start_line` past the end fails as
  `failed/invalid_line_range` naming the document size ("start_line 999999
exceeds document line count 11"); `end_line` past the end clamps
  (`end_line=11`). Exact, actionable, bounded.
- `TestProbeRegexBacktrackingFinishes`: `a(a+)+b` against 20 000 a's
  finishes in ~9 ms with 0 matches (RE2, linear); `(` answers
  `failed/invalid_regex` with a `retry_as_literal` next step.
- `TestProbePlan200OperationsRefused`: 200 operations are refused at the
  schema layer -- "has 200 items, maximum 8; append another bounded batch
  with change_plan action=edit and edit.mode=add". Names the bound and the
  recovery in one line.
- `TestProbePlan4MBContentBoundary`: 4 MB+1 refuses naming the exact byte
  bound ("4194305 bytes, maximum 4194304 bytes"); exactly 4 MB stages
  (`Plan intent created`). The boundary is inclusive and exact.
- `TestProbeEditApply64OperationsBoundary`: 64 operations apply in ~12 ms
  (18 KB reply, all 64 files land); 65 refuse at the schema layer naming
  maximum 64.
- `TestProbeCRLFAndMixedWhitespaceRoundTrip`: CRLF and mixed tab/space
  files round-trip byte-exactly (46 and 44 bytes), with correct line counts.
- `TestProbeEmptyAndNewlineOnlyFiles`: empty/newline-only/unterminated
  files report 0/3/1 lines; outlines answer `whole document, no
  declarations found` with a fallback handle for the first two. Note:
  `nonewline.go` (`package p`, parsed natively, zero sections) carries
  `fallback=false`, so there is no handle addressing it -- a minor gap in
  the degenerate corner.
- `TestProbeDiagnosticsOver300Files`: the open reply discloses truncation;
  `diagnostics` over 300 files takes ~1 ms with a 774-byte reply;
  `search {query: return, limit: 10}` returns 10 of 306 with complete
  coverage. The file budget (2000) holds with room to spare.

## Timing and state

No call took more than ~ten seconds. Slowest was the 100k-match search at
3.1 s (1000 handle registrations plus result-set freeze for a 700 KB
file) -- worth watching if the cap ever rises. State-directory size was
unchanged across the big reads and searches (270 B before and after):
handles and result sets are capped stores, and nothing leaked onto disk.
