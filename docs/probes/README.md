# Probe verdicts

Six adversarial probe jobs ran against `3f64e94` on 2026-09-12 across three
model families, each asked to attack one property of the service and to leave
a report and a live test behind. Four produced work: `probe/bounds` and
`probe/lifecycle` (opencode `muse-spark-1.3-contributor-free`),
`probe/refusals` and `probe/honesty` (Claude Haiku 4.5). Two, `boundaries` and
`recovery`, ran out of turns at `--max-turns 5` and pushed nothing.

Their four reports are in this directory exactly as they arrived. This file is
the verdict on every claim in them, reached by reproducing each one against
the service before deciding anything. The probe test files themselves are not
in the repository: each was written against `main` in isolation, so the four
share helper names and cannot compile together, and each assertion that
mattered was rewritten next to the fix for the defect it found. The branches
were deleted once these verdicts were recorded; the reports and this file are
what they leave behind.

A verdict is one of:

- **fixed** - the claim was reproduced, it named a real defect, and a commit
  on this branch repairs it with a test that fails without it.
- **expected** - the behaviour is real and is what the service should do; the
  probe's expectation was wrong, or its reasoning about the consequence was.
- **discarded** - the claim did not survive reproduction.
- **deferred** - the defect is real and is not repaired here, with the reason
  and the work that will repair it named.

## bounds, budgets and degenerate input

| # | Claim | Verdict |
|---|---|---|
| 1 | `read` with no `max_lines` returns megabytes with no flag and no size disclosure | deferred |
| 2 | A 5 MB file arrives whole and silent, one step below the 32 MB refusal | deferred |
| 3 | `max_lines` counts newlines, so one 2 MB line defeats it | deferred |
| 4 | Outlines are uncapped: 3000 declarations, 387 KB, no flag | fixed, `8bf838c` |
| 5 | 100k matches report `total=1000`, the cap standing in for the count | fixed, `6b93c9a` |
| 6 | `context_lines` is unbounded and repeats the file once per hit | fixed, `b21949d` |
| 7 | Binary files are skipped by search with no trace in coverage | fixed, `d607557` |
| 8 | A BOM is delivered as part of line 1 with no disclosure | expected |
| - | A parsed file with no declarations has no fallback handle (noted among the passes) | fixed, `0860d18` |

Claims 1, 2 and 3 are one defect: `read` has a line bound and no byte bound,
and a byte problem needs a byte bound. They are deferred rather than repaired
here because adding a bounded `max_bytes` to `read` is the first of the
round-two scenarios, where an agent does the work through Huyang and logs what
the tool made hard. Until then the guide's promise - "no size cap unless
`max_lines` is set" - is honest about the missing bound, which is why this is a
gap rather than a lie.

Claim 6 was repaired more broadly than reported. The catalog already declared
`maximum: 20` for `context_lines`; the validator enforced `maxLength` and
`maxItems` and ignored every integer bound, so the schema promised something
the service did not keep. Now every declared minimum and maximum is enforced.

Claim 8 is expected. The bytes delivered are the bytes of the file, and
content is the revision: stripping a BOM would mean the reply no longer
describes the document it names. The report's consequence does not follow -
it predicted that a `replace_literal` derived from the delivered text would be
refused, and it is not, because the literal is matched as a substring:
`old: "package bom"` applies to a file whose first bytes are the BOM.

## plan lifecycle

| # | Claim | Verdict |
|---|---|---|
| 1 | An empty plan commits a no-op as `canonical_changed: true` | fixed, `821089c` |
| 2 | Applying without a preparation, and applying twice, are reported as provider failures | fixed, `ac8907e` |
| 3 | A stale `prepared_revision` is refused as `commit_precondition_changed` | fixed, `03447fa` |
| 4 | A conflict says "plan preview is stale" when the canonical bytes moved | fixed, `42a1e37` |

Everything this report listed as verified-honest was left alone, and its
reading of the two "passing but wrong" cases was right: a test that asserts
only that a call was refused passes while the refusal misdescribes itself.

## refusal quality

| # | Claim | Verdict |
|---|---|---|
| 1 | An unknown `workspace_id` offers no recovery path | fixed, `6ba73d2` |
| 1a | ... and carries no code | discarded |
| 1b | ... and should be `conflict` rather than `failed` | discarded |
| 2 | A nonexistent `plan_id` answers `provider_unavailable` | fixed, `ac8907e` |
| 3 | "Symbol locator did not resolve uniquely" describes two different misses | fixed, `2bca8d9` |
| 4 | A stale `revision_id` on `delete_file` does not hand back the current one | fixed, `09c0c7f` |
| 5 | The same idempotency key produces different results | discarded |

Claim 1a is not so: the envelope carries `workspace_not_found`, and the probe
read the code from the wrong place. Claim 1b asks for `conflict`, which is the
outcome for a precondition a caller can refresh and retry; an unknown
workspace id is not one, so it stays `failed` and gains the two ways back.

Claim 5, filed as CRITICAL, is the one that did not survive contact. The
replay of a call with the same key and the same arguments answers the original
receipt with `idempotency: replayed`, `replayed_request: true` and
`canonical_changed: false` beside `original_canonical_changed: true` - which
is exactly what a replay should say, and is why the two envelopes are not
byte-identical. The test compared rendered JSON and read the difference as a
broken contract. The same key with different arguments is refused with
`idempotency_key_reused` and a new-key follow-up.

## honesty

| # | Claim | Verdict |
|---|---|---|
| 1 | `api_compatible` is proven with no language server running | expected |
| 2 | `symbol_exists` is proven with no language server running | fixed, `d607277` |
| 3 | `diagnostic_delta` is absent when no server answered, which may hide a gap | expected |

Claim 1 is expected. `api_compatible` never asks a language server: it reads
each affected file's exported surface with the adapter for its language, Go
through `go/parser` and TypeScript through its own reader, and a file no
adapter covers is reported as a gap that makes the answer unknown. Coverage
says `api_surface` because that is the evidence it has.

Claim 2 was a real defect in a narrower form than reported. `symbol_exists`
and `symbol_absent` located the declaration through a parse where the native
sectioner knows the language and through a text scan everywhere else, and
stamped both answers `proven` with `parser_sections` coverage. The scan finds
the name in a call, an import or a string as readily as in a declaration. They
now answer only where a parser read the file. For Go and Python, where the
probe expected `unknown`, `proven` was and remains right: the proof rests on a
parse, and no language server was consulted or needed.

Claim 3 is expected. With no server the edit reply is `provisional` and
carries a `verification` block naming the reason; `diagnostic_delta` is absent
because there is no delta to report, not as a claim that the file is clean.
The probe's own test logged this and failed on a different assertion.
