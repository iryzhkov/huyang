# Probe: plan lifecycle

Live-probe of the `change_plan` state machine, run 2026-09-13 against
`3f64e94` on branch `probe/lifecycle`.
Tests: `internal/livetest/probe_lifecycle_test.go` (15 tests, `TestProbe*`).
Run: `go test -tags live ./internal/livetest -run 'TestProbe' -v`.

Each test starts its own daemon (`start(t)`, isolated home, no language
servers, so prepares land PROVISIONAL) and works on a copy of the `go`
fixture. Every finding below names the exact calls, what the service said,
what an honest answer would have been, and why the difference matters to an
agent that trusts the reply. Test status is stated per finding: a **failing**
test is left failing; a **passing** test whose behaviour is still wrong is
flagged as such.

## 1. An empty plan commits a no-op as `canonical_changed: true` (FAILING)

**Calls.** `change_plan {action: create, operations: []}` →
`change_plan {action: prepare, plan_id, plan_revision}` →
`change_plan {action: apply, plan_id, plan_revision, prepared_revision,
accept_provisional: true}`.

**What happened.** Every step answers `ok`. The apply receipt says
`state: COMMITTED`, `canonical_changed: true`, `revision: wsrev_2`
(`from_revision: wsrev_1`), while in the same object
`changed_paths` is nil, `preparation.affected_files` is nil, and the
operation list is empty. No byte on disk moved.

**Expected.** Either refuse the empty plan at create/prepare, or commit it
with `canonical_changed: false` and no new canonical revision. `true` plus
an empty path list is self-contradictory: the flag and the list disagree
about the same commit.

**Why it matters.** An agent that gates `verify_run`, rebuilds, or history
polling on `canonical_changed` will do work for a change that does not
exist, and the canonical revision counter advances (`wsrev_1` → `wsrev_2`),
so revision-range reasoning (`revision_diff`, history) gains a phantom step.

**Test.** `TestProbeEmptyPlanChangesNothing` fails on the `canonical_changed`
assertion.

## 2. State violations reported as `provider_unavailable` (PASSING, still wrong)

**Calls.** (a) `change_plan {action: create, operations: [create_file]}` then
`change_plan {action: apply, plan_id, plan_revision, prepared_revision:
"prep_never_prepared"}` — a plan that was never prepared. (b) Prepare a
one-file plan, apply it, then apply the same `plan_id`/`plan_revision`/
`prepared_revision` again.

**What happened.** Both applies are refused with no byte moved, but the code
is `provider_prepare_failed` / `provider_unavailable`: (a) "prepared sandbox
is not available"; (b) "the prepared sandbox for this plan is gone; it was
rolled back, applied or discarded". The human message is accurate; the code
blames the provider.

**Expected.** A state violation code (`plan_state_invalid` — the same code a
discard of a COMMITTED plan correctly gets, see §5). Nothing about the
provider is wrong: no language server would change the answer, because there
is no preparation to stage (case a) or nothing left to commit (case b).

**Why it matters.** On a machine with no language server installed,
`provider_unavailable` is also the code for "no server answered" — the
documented meaning an agent knows. A code-driven caller routes this to
`language_server_setup`/restart and retries, which cannot help. Worse, in
case (b) the `next` block offers `prepare` on a plan that is already
COMMITTED, sending the agent back into a lifecycle that has already ended.

**Tests.** `TestProbeApplyOpenPlanWithoutPrepareIsRefused` and
`TestProbeApplyTwiceWithSamePreparedRevision` pass (refusal + bytes verified);
the wrong-code finding is in the logged envelopes, not the verdicts.

## 3. Stale `prepared_revision` refused under the wrong code (FAILING)

**Calls.** Prepare plan (rev 1, `prep_A…`), `edit` it (state returns to OPEN,
old preparation released), prepare again (rev 2, `prep_B…`), then `apply`
with `plan_revision: 2` but `prepared_revision: prep_A`.

**What happened.** Refused, no byte moved — but as
`conflict` / `commit_precondition_changed` with the summary string
`prepared_revision_changed`. The service knows exactly what is wrong (it says
so in the summary) and files it under a different code, while a dedicated
`prepared_revision_changed` code exists (`internal/workspace/errors.go`).

**Expected.** Code `prepared_revision_changed`, matching the summary. The
coarse code is not false — a stale preparation is a kind of changed
precondition — but it merges two different recoveries: "someone else wrote
the file, re-preview" versus "you are holding last week's handle, use the
current one".

**Why it matters.** An agent switching on codes treats this like an external
write (`commit_precondition_changed`: canonical moved, rebase needed) instead
of a local bookkeeping error (wrong handle, the current revision is already
in the reply). The `next` block here is correct (prepare/discard at
`plan_revision: 2`), which is what saves the agent — the code alone would
send it the wrong way.

**Test.** `TestProbeApplyWithPreviousPreparedRevision` fails on the
code assertion; refusal and byte-safety assertions pass.

## 4. Conflict message says "plan preview is stale" when canonical moved (PASSING, still wrong)

**Calls.** Two plans replace the same `ledger.go/Total` symbol (→ 8 and → 9),
both prepared against the same bytes; apply the first, then apply the second
— in both orders (`TestProbeTwoPlansOverSameFileSecondApplyConflicts`,
`TestProbeTwoPlansOverSameFileReverseOrder`).

**What happened.** The loser is refused with
`conflict` / `commit_precondition_changed`: "plan preview is stale". The
winner's bytes survive byte-for-byte, the loser's bytes are absent. Order
does not matter — first apply wins either way.

**Expected.** The verdict and the bytes are honest; only the message is
oblique. "Preview" is the deterministic pre-prepare record, an internal
artefact the caller never asked about. What actually happened is "canonical
moved under this preparation".

**Why it matters.** Low severity, but the message is the only explanation an
agent gets for a conflict on the happy path (two agents, or plan-then-edit).
"Preview is stale" reads as "re-run preview" rather than "your base moved;
rebase or re-prepare", and the fix-it action differs.

**Tests.** Both pass; noted here because the refusal text is the behaviour an
agent will quote back to the user.

## Verified honest (no finding)

- `create` → `OPEN`; `preview` → `PREVIEWED`; prepare still works afterwards;
  nothing stages or moves bytes before apply (`TestProbePreviewThenPrepare`).
- Re-preparing a prepared plan is idempotent: the same `prep_…` revision is
  returned, still readable (`TestProbePrepareTwice`).
- `edit` on a prepared plan returns it to `OPEN` and the released revision
  answers `prepared_revision_unavailable`, never canonical bytes
  (`TestProbeEditPreparedPlanReleasesPreparation`,
  `TestProbeApplyWithPreviousPreparedRevision` byte checks).
- Discard of OPEN / prepared plans lands `DISCARDED`, releases the sandbox,
  moves no bytes (`TestProbeDiscardOpenPlan`, `TestProbeDiscardPreparedPlan`).
- Discard of COMMITTED or already-DISCARDED plans is refused with
  `conflict` / `plan_state_invalid` ("cannot move from COMMITTED to
  DISCARDED"), applied bytes untouched (`TestProbeDiscardCommittedPlanIsRefused`,
  `TestProbeDiscardTwice`). Note the outcome word is `conflict`, not
  `failed` — both are refusals; callers must not treat only `failed` as "no".
- A `depends_on` cycle is refused at create ("operation dependency cycle"),
  an acyclic chain prepares and applies in order
  (`TestProbeDependencyCycleIsRefused`,
  `TestProbeChainedDependsOnAppliesInOrder`).
