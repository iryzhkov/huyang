# Questions about `t3-steward`

These answers are pinned to `d47f481 Fix native wait CLI admin transport limits`, the output of `git -C /home/igor/Work/t3-steward log --oneline -1` on 2026-09-12. I made 52 Huyang calls against that checkout. In the call lists, `workspace_id` means `ws_94ae80c95b78f76d1362ae8a626d54c5`; the first search opened it from `root=/home/igor/Work/t3-steward`.

## 1. Failure and retry

A failed task is not automatically retried by a manifest retry count. Failure is recorded when explicit success is missing or verification fails in `internal/backlog/dag.go:96-116`, `DAGExecution.CompleteAttempt`. A retry happens only when an operator submits the durable `retry` admin command. The decision is made in `internal/backlogadmin/execution.go:178-251`, `planAttemptCommand`: it accepts retry only from `failed` or `cancelled`, creates attempt number +1 with a stable ID derived from the command, and reopens the run as queued. `Service.ExecutePendingCommands` in the same file at lines 30-69 loads pending commands, calls the planner, and atomically applies the result. Thus the operator/admin command decides whether a failed task is retried; the code settles whether that request is currently legal.

Calls, in order (16):

1. `search {root:"/home/igor/Work/t3-steward", query:"retry", mode:"literal", context_lines:3, limit:100}`
2. `search {workspace_id, query:"MaxAttempts", mode:"literal", context_lines:4, limit:100}`
3. `search {workspace_id, query:"RetryTask(", mode:"literal", context_lines:5, limit:100}`
4. `search {workspace_id, query:"AdminCommandRetry", mode:"literal", context_lines:6, limit:100}`
5. `workspace_inspect {workspace_id, view:"map"}`
6. `search {workspace_id, query:"max_attempt", mode:"literal", context_lines:4, limit:50}`
7. `search {workspace_id, query:"MaxTurns", mode:"literal", context_lines:4, limit:50}`
8. `search {workspace_id, query:"max_turns", mode:"literal", context_lines:4, limit:50}`
9. `search {workspace_id, query:"retry_policy", mode:"literal", context_lines:4, limit:50}`
10. `search {workspace_id, query:"RetryPolicy", mode:"literal", context_lines:4, limit:50}`
11. `read {workspace_id, target:{symbol_locator:{path:"internal/backlog/runner.go", name_path:"(*Runner).Tick"}}, numbered:true}`
12. `search {workspace_id, query:"ProgressFailed", mode:"literal", paths:["internal/backlog"], context_lines:5, limit:100}`
13. `search {workspace_id, query:"AfterFailure", mode:"literal", context_lines:5, limit:100}`
14. `read {workspace_id, target:{path:"internal/backlogadmin/execution.go"}, view:"outline", numbered:true}`
15. `read {workspace_id, targets:[{symbol_locator:{path:"internal/backlogadmin/execution.go",name_path:"planAttemptCommand"},numbered:true},{symbol_locator:{path:"internal/backlogadmin/execution.go",name_path:"Service/ExecutePendingCommands"},numbered:true}]}`
16. `read {workspace_id, target:{symbol_locator:{path:"internal/backlog/dag.go",name_path:"DAGExecution/CompleteAttempt"}}, numbered:true}`

## 2. What manual `backlog start` bypasses

`backlog start` marks the attempt `AdminForceStart` in `internal/backlogadmin/execution.go:178-251`, `planAttemptCommand`. In the ordinary quota evaluator, `internal/backlog/quota_admission.go:117-227`, `quotaAdmissionSession.Evaluate`, that flag exits before quota-window checks. It therefore bypasses quota-observation freshness, admission state, the surplus start window, forecast capacity/reservations, and quota drain runway; the forced assignment is also excluded from automatic quota throttling by `internal/backlog/worker_exchange.go:263-311`, `FleetCoordinator.reconcileWorkerThrottle` (the `!attempt.AdminForceStart` filter is at lines 283-292). It does not erase general safety. `commandSafetyBlocker` in `internal/backlogadmin/execution.go:443-559` still requires local and external dependencies to have succeeded, resource locks to be free, and a fresh, ready, placement- and provider-compatible worker. The planning path still requires a valid task class, an estimate, and task deadline runway before the force shortcut. Finally, `internal/store/sqlite/admin_apply.go:18-218`, `Store.ApplyAdminCommand`, atomically rechecks the safety fingerprint and worker-validity horizon, fences command and target revisions, validates the transition, records one audited outcome, and refuses a settled sink. Those are the dependency, lock, worker, revision, and effect/idempotency safeguards that still apply.

Calls, in order (11):

1. `search {workspace_id, query:"AdminForceStart", mode:"literal", context_lines:5, limit:100}`
2. `search {workspace_id, query:"commandSafetyBlocker", mode:"literal", context_lines:5, limit:100}`
3. `search {workspace_id, query:"ForceStart", mode:"literal", context_lines:5, limit:100}`
4. `search {workspace_id, query:"manual override", mode:"literal", context_lines:5, limit:100}`
5. `read {workspace_id, targets:[{symbol_locator:{path:"internal/backlogadmin/execution.go",name_path:"commandSafetyBlocker"},numbered:true},{symbol_locator:{path:"internal/backlog/quota_admission.go",name_path:"quotaAdmissionSession/Evaluate"},numbered:true}]}`
6. `search {workspace_id, query:"SafetyValidUntil", mode:"literal", paths:["internal/store/sqlite","internal/backlogadmin"], context_lines:5, limit:100}`
7. `search {workspace_id, query:"Effect", mode:"literal", paths:["internal/store/sqlite","internal/backlogadmin"], context_lines:5, limit:100}`
8. `search {workspace_id, query:"ExpectedRevision", mode:"literal", paths:["internal/store/sqlite","internal/backlogadmin"], context_lines:5, limit:100}`
9. `search {workspace_id, query:"stale target revision", mode:"literal", paths:["internal/store/sqlite","internal/backlogadmin"], context_lines:5, limit:100}`
10. `read {workspace_id, target:{symbol_locator:{path:"internal/store/sqlite/admin_apply.go",name_path:"Store/ApplyAdminCommand"}}, numbered:true}`
11. `read {workspace_id, target:{symbol_locator:{path:"internal/backlog/worker_exchange.go",name_path:"FleetCoordinator/reconcileWorkerThrottle"}}, numbered:true}`

## 3. Coordinator-to-worker handoff and disappearance

The wire contract is the `internal/workerproto` package. `cmd/t3-steward/backlog_v2_worker_client.go:269-453`, `newCoordinatorWorkerSession`, builds signed/authenticated protocol clients over `workerproto.SSHTransport`; when a worker has a persistent `Connection` configured it substitutes a persistent stream transport. `internal/backlog/worker_exchange.go:56-201`, `FleetCoordinator.ReconcileWorker`, snapshots the worker, builds execution-package offers for already committed assignments, calls `DeliverOffers`, validates and persists claims, refreshes the snapshot, renews leases, and sends durable lifecycle commands. If the worker disappears mid-attempt, `internal/store/sqlite/worker_commands.go:429-503`, `Store.ExpireAssignmentLeases`, changes the elapsed claimed assignment to `unknown` (and a creating/confirmed dispatch to `dispatch unknown`). That state is deliberately nonterminal and cannot be reassigned until the coordinator proves the old execution stopped. Ambiguous thread creation likewise retains the same persisted thread ID and dispatch token and marks dispatch unknown in `internal/backlog/dispatch.go:52-123`, `ReconcileAssignmentDispatch`.

Calls, in order (12):

1. `search {workspace_id, query:"OfferAssignment", mode:"literal", context_lines:5, limit:100}`
2. `search {workspace_id, query:"AssignmentOffer", mode:"literal", context_lines:5, limit:100}`
3. `search {workspace_id, query:"worker disappears", mode:"literal", context_lines:5, limit:100}`
4. `search {workspace_id, query:"lease expired", mode:"literal", context_lines:5, limit:100}`
5. `search {workspace_id, query:"AssignmentUnknown", mode:"literal", context_lines:5, limit:100}`
6. `symbol_find {workspace_id, query:"WorkerExchange", include_source:false}`
7. `symbol_find {workspace_id, query:"ExpireAssignmentLeases", include_source:false}`
8. `symbol_find {workspace_id, query:"ReconcileAssignmentDispatch", include_source:false}`
9. `read {workspace_id, target:{path:"internal/backlog/worker_exchange.go"}, view:"outline", numbered:true}`
10. `read {workspace_id, targets:[{symbol_locator:{path:"internal/backlog/worker_exchange.go",name_path:"FleetCoordinator/ReconcileWorker"},numbered:true},{symbol_locator:{path:"internal/store/sqlite/worker_commands.go",name_path:"Store/ExpireAssignmentLeases"},numbered:true},{symbol_locator:{path:"internal/backlog/dispatch.go",name_path:"ReconcileAssignmentDispatch"},numbered:true}]}`
11. `read {workspace_id, target:{path:"cmd/t3-steward/backlog_v2_worker_client.go"}, view:"outline", numbered:true}`
12. `read {workspace_id, target:{symbol_locator:{path:"cmd/t3-steward/backlog_v2_worker_client.go",name_path:"newCoordinatorWorkerSession"}}, numbered:true}`

## 4. Quota forecast

The historical forecast is computed in `internal/report/forecast.go:76-125`, `BuildDemand`, and lines 131-178, `Demand.Forecast`. `BuildDemand` takes a quota bucket key, interactive usage rises, raw observations (which establish the observed date range), and a timezone; it builds per-weekday/per-hour samples. `Demand.Forecast` takes the target weekday/hour slot and uses the configured quantile and minimum sample count, falling back from the exact slot to same-hour weekday/weekend pooling and then all days. The CLI assembly is `cmd/t3-steward/forecast.go:15-146`, `cmdForecast`: it collects local and optional remote observations for the requested history span, uses dispatched thread IDs plus a five-minute threshold to classify interactive rises, supplies config quantile/minimum samples and fallback-per-hour, and combines expected interactive demand through the bucket reset with current used percent and the safety margin to calculate backlog headroom.

Calls, in order (6):

1. `symbol_find {workspace_id, query:"Forecast", include_source:false}`
2. `symbol_find {workspace_id, query:"BuildForecast", include_source:false}`
3. `symbol_find {workspace_id, query:"ComputeForecast", include_source:false}`
4. `symbol_find {workspace_id, query:"ForecastInput", include_source:false}`
5. `read {workspace_id, targets:[{symbol_locator:{path:"internal/report/forecast.go",name_path:"Demand/Forecast"},numbered:true},{path:"internal/report/forecast.go",view:"outline",numbered:true}]}`
6. `read {workspace_id, targets:[{symbol_locator:{path:"internal/report/forecast.go",name_path:"Demand"},numbered:true},{symbol_locator:{path:"internal/report/forecast.go",name_path:"BuildDemand"},numbered:true},{symbol_locator:{path:"cmd/t3-steward/forecast.go",name_path:"cmdForecast"},numbered:true}]}`

## 5. Sink tasks

A sink task is the coordinator-owned, run-local terminal aggregate for a workflow graph. It has no worker, route, or attempt. It depends on every executable task and waits until all run executions are proven quiescent; then it records failed/cancelled/skipped task IDs and fixes the run's final status. This supplies one stable run-completion node (including for run waits) and prevents a completed run from reopening. `internal/domain/sink.go:86-136`, `BindRunSink`, creates or rebinds the implicit `__sink`; `ProjectRunSink` at lines 138-197 settles it. Normal bundle ingestion calls the creator from `internal/backlog/ingest.go:190-289`, `BundleIngester.buildRecords`, specifically lines 283-287.

Calls, in order (7):

1. `search {workspace_id, query:"SinkTask", mode:"literal", context_lines:5, limit:100}`
2. `search {workspace_id, query:"sink task", mode:"literal", context_lines:5, limit:100}`
3. `search {workspace_id, query:"RunSink", mode:"literal", context_lines:5, limit:100}`
4. `search {workspace_id, query:"BindRunSink", mode:"literal", context_lines:5, limit:100}`
5. `search {workspace_id, query:"ReservedSink", mode:"literal", context_lines:5, limit:100}`
6. `read {workspace_id, targets:[{symbol_locator:{path:"internal/domain/sink.go",name_path:"BindRunSink"},numbered:true},{symbol_locator:{path:"internal/domain/sink.go",name_path:"ProjectRunSink"},numbered:true},{path:"internal/backlog/ingest.go",start_line:250,end_line:290,numbered:true}]}`
7. `read {workspace_id, target:{path:"internal/backlog/ingest.go"}, view:"outline", numbered:true}`

## Friction log

The study took 52 Huyang calls: 29 searches, 15 reads, 7 symbol finds, and 1 workspace inspection. Several reads carried multiple targets, so the number of source regions read was higher than the call count. No study call had to be repeated with corrected arguments, and no study call refused the requested operation.

The first call was the worst answer-to-question fit. Searching bare `retry` returned 214 matches across 55 files, limited to 100, and about 15,000 tokens before harness truncation. I guessed it because the question used it and I did not yet know the package vocabulary. It revealed four unrelated meanings—workflow attempts, admin commands, transport retry, and turn continuation—but I needed 15 more calls to distinguish them. A result-set refinement could narrow text or paths, but could not express “which retry creates a new workflow attempt after terminal failure?”

The batched `MaxTurns` and `RetryPolicy` exploration produced another roughly 17,000-token response and was truncated. Most was transport and test detail. This was paid-for evidence I did not need once `AdminCommandRetry` led to `planAttemptCommand`. Exact symbol reads were much better: the two-target read of `planAttemptCommand` and `Service.ExecutePendingCommands` returned complete bodies and settled question 1.

`workspace_inspect view=map` reported 398 entries but marked them truncated and provided no usable path map. I wanted a package/file outline. It cost a call without advancing the answer. Per-file `read view=outline` was the reliable substitute.

Three question-4 guesses—`BuildForecast`, `ComputeForecast`, and `ForecastInput`—returned `semantic_coverage_partial`, not a clean “no such symbol.” Each suggested checking language-server status or doing literal search, although the embedded fallback also said no file mentioned the spelling. I read those replies twice to decide the negative evidence was adequate and that the LSP suggestion would violate this scenario's allowed-tool list. The earlier broader `Forecast` query had already found `Demand.Forecast` and `BuildDemand`.

`symbol_find WorkerExchange` returned seven full opaque handles with hashes, byte offsets, expiry times, and an LSP-enrichment warning when I needed only names and files. Its useful part was accurate, and a one-call outline found `FleetCoordinator.ReconcileWorker`. The other symbol finds plus a multi-target read efficiently supplied the lease and dispatch bodies.

Broad `AssignmentOffer`, `AssignmentUnknown`, `AfterFailure`, `ExpectedRevision`, and sink searches were test-heavy and sometimes truncated. Scoping `ExpectedRevision` helped but still found more than 100 occurrences. Search has no evident “exclude *_test.go” argument; this was the clearest point where I knew the filter I wanted but could not express it. Negative searches such as `worker disappears` were harmless but unhelpful.

I did not read any `t3-steward` repository file with shell tools and did not reach for `grep`, `sed`, `cat`, `find`, `ls`, a heredoc, or an editor against it. Git was used only for the requested pinned commit and deliverable branch/commit/push. I did use `cat` on `/home/igor/.config/agents/huyang.md`, machine guidance outside either repository, not source under study.

Creating this deliverable exposed one actual mutation failure. My first `edit_apply {workspace_id:"ws_d524a9bf35094d7a92a94600ded58008", operation:{kind:"create_file",path:"docs/friction/steward-questions.md",content}, idempotency_key:"scenario-steward-questions-create"}` failed because `docs/friction/` did not exist: Huyang tried to open `docs/friction/.steward-questions.md.huyang-...` and returned “no such file or directory,” with an empty `next` list. The scenario explicitly said creating that path with `create_file` would create the directory, so that part of the prompt/tool expectation was wrong. After reporting the fallback, I ran shell `mkdir -p docs/friction` and retried `create_file` with a fresh idempotency key. No repository file content was written through the shell.

Publication had a separate environment mismatch. The exact instructed `git push ssh://igor@normandy/home/igor/Work/huyang HEAD:refs/heads/scenario/steward-questions` failed twice with “Could not read from remote repository,” even though `git ls-remote` against that SSH URL succeeded. Since this worker is on normandy and the prompt says pushing to `/home/igor/Work/huyang` is safe, I pushed the same ref directly to that repository path and verified its tip. This was a Git transport fallback, not a repository content read or write workaround.

The initial study search successfully opened the workspace from `root`, as advertised, and every later study call reused revision `wsrev_965`. I did not call `workspace_open` on the study checkout because the scenario restricted its allowed study tools. I did call it on the separate Huyang deliverable before editing, as required by the global editing guidance.

The strongest parts were revision-pinned, numbered symbol reads and multi-target reads. Full bodies were necessary for `planAttemptCommand`, `quotaAdmissionSession.Evaluate`, `commandSafetyBlocker`, `Store.ApplyAdminCommand`, `FleetCoordinator.ReconcileWorker`, `Store.ExpireAssignmentLeases`, `ReconcileAssignmentDispatch`, `newCoordinatorWorkerSession`, `BuildDemand`, `Demand.Forecast`, `cmdForecast`, `BindRunSink`, and `ProjectRunSink`; search context or outlines alone did not settle the qualifications. Outlines were valuable as routing aids, and multi-target reads reduced round trips without blending bodies.

## Claim verification

Every behavioral claim above cites a file and either a named symbol or line range actually returned by a Huyang `read` call. Search-only evidence was used for discovery, not as the sole basis for a final claim. The pinned checkout line is:

```text
d47f481 Fix native wait CLI admin transport limits
```
