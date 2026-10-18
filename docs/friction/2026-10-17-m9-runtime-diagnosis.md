# M9 private matrix runtime diagnosis

Source examined: `da69dba11db6759baf3b82f6d17d52b346c5b625`.
Failed agent-a job: `74965dd9-ef57-44be-ba5a-83a7f5d8d89a`, definitively exited 1.
Evidence: complete allowlisted extraction at
`/home/igor/.cache/huyang-friction/executor/m9-da69-matrix-artifacts`.
The diagnosis task only read source and synthetic regular-file reports; it ran no
build, test, lint, generator, provisioning, service or Chaldea job.

## Evidence and attribution

- All 15 cases failed. The manager-spelling and effective-pull-proof pure tests
  passed. Those passes do not establish private runtime acceptance.
- Every case's before/after canonical hash difference is exactly `main.py`.
  The probe restores its intentional edits only at the end of the success path;
  Fatal or deadline exits bypass that restoration. Thus the 15 teardown hash errors
  are secondary harness failures, not evidence of an unexpected canonical writer.
  Selected-environment/tool/HOME hash failures were not reported.
- Cases 00, 01, 02, 03 and 06 repeatedly execute a venv Python with Pyright's
  non-isolated `sys.path`/prefix query and exit 1 (4, 5, 6, 4 and 4 times respectively).
  The corresponding base-interpreter path queries in unknown-environment cases
  exit 0. For example, 00/exec.11405 and 01/exec.13619 exit 1 after
  `json.dump(dict(path=orig_sys_path, prefix=sys.prefix), sys.stdout)`.
  The venv `bin/python` symlink hashes equal the hash of
  `/home/job/.cache/hf/m9/tools/bin/python`, confirming creation through the tool alias.
  CPython venv records a creator directory in `pyvenv.cfg`; an alias outside the
  managed installation is an avoidable fixture hazard. The old collection lacks
  pyvenv.cfg contents and interpreter stderr, so the precise startup error is an
  inference, not a recovered traceback.
- In 00, 02 and 03, current pull evidence contains both the real assignment error
  and an unexpected missing `m9_fixture_dep` import. Repeated diagnostics calls
  retain these bodies until the 120-second context kills MCP; the probe waits
  for the known-environment import requirement, not for an absent type error.
  Resolving the selected interpreter path is already correct in attachment settings.
- In 08, fixture Poetry setup and `env info --path` succeed. The actual discovery
  `poetry --no-plugins env info --executable` in 08/exec.21745 is killed by SIGKILL;
  language_server_status reports `environment_manager_query_failed`. Production's
  manager deadline is two seconds. Full syscall ptrace overhead is a plausible
  contributor, not proof that the production deadline needs increasing.
- Cases 01, 04, 06 and 08–12 fail the initial false-clean guard: the edit commits
  but returns `outcome=ok`, no type-error delta and no provisional disclosure.
  For example, 01/004-edit_apply.json and 04/004-edit_apply.json say “no new
  diagnostics”; recovered pull evidence claims complete with a result ID and
  matching version but contains no findings. Cases 05, 07, 13 and 14 similarly
  retain authoritative empty current reports while polling. These failures remain
  blockers. Interpreter fixture repairs alone do not explain the base-interpreter
  unknown cases.
- All 15 old `lsp.jsonl` files contain attachment/progress events but zero observed
  push publications. Huyang evidence uses pulls. The observer wraps only the
  global publish handler, and records no raw pull response, so zero observed pushes
  does not prove zero incoming notifications. The reports cannot distinguish an
  empty producer response from collection/decoding behavior, or recover raw
  missing-import grouping input. Do not change production policy on that gap.

## Bounded source correction

Only the opt-in Go probe and its M9 documentation change:

1. Resolve the private managed base executable before stdlib venv creation and
   Poetry `env use`. Keep selected venv paths unresolved. Require ordinary and
   isolated interpreter checks for stdlib JSON, venv prefix/site-packages and the
   installed synthetic dependency before provider startup; preserve their output
   in the existing allowlisted fixture command log.
2. Request strace seccomp filtering for the existing exec-only trace and retain
   kill-on-exit/process-tree tracking. Inspect fallback warnings in service.log.
   Do not increase the production manager deadline or omit manager nonexecution proof.
3. Observe pushes through the configured client handler and raw pulls through a
   delegating request callback. Preserve capabilities, errors, bodies, callback
   results and all acceptance assertions. Raw-import counting accepts either real
   protocol stream; push-specific quiet/late checks still require actual pushes.
4. Register exact intended source bodies before dispatch. Restore recognized bodies
   in cleanup after adapters/service descendants terminate, including failed cases.
   Save before-restoration hashes; unexpected bodies remain unmodified and fail.
   Final canonical, selected-environment, tool and scratch HOME hashes still apply.

No production Python environment/Lua, IPC helper or client-acceptance source changes.
No claim that these corrections make the matrix pass.

## Parent-owned validation

Run source formatting/compile checks and the existing pure M9 tests on the final
commit through the parent's Chaldea workflow. Go formatting was requested through
Huyang on the final source edit; its receipt supplies no independent formatting gate
result, so the parent must still run its required formatting gate.
Use a fresh guarded scratch HOME, empty run directory, pinned private tools and
binary for the corrected matrix; retain the complete bounded allowlisted collection.
The existing collector already includes fixture-commands.log, lsp.jsonl and the new
before-restore-hashes.json, so no collector allowlist change is needed.

A bounded first run can select local_venv, explicit_wins, unknown,
cold_unknown_baseline, uv_base_rejected and poetry_external to distinguish repaired
fixture startup, unchanged false-clean assertions and real manager timing. The final
acceptance gate remains all 15 cases. Recover raw pull request URIs/responses and
errors alongside evidence_get batches for any false-clean failure. Compare raw
missing-import/type-error bodies to normalization and delivery, and use a prior-base
control if attribution to an M9 regression remains uncertain. Do not label a raw
complete pull authoritative when its effective proof is vetoed.

If a false-clean reply persists, the parent must handle the proven runtime defect
within the appropriate production ownership; this child deliberately does not
change the generic diagnostic collection/ledger code. Re-run full Go/races only if
subsequent source changes warrant broadening the already passing a2e4acd evidence.
