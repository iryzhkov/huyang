# Debugging and usability workshop — 2026-09-13

Bounded workshop, reproduced fixes and publication complete. Normandy, homelab and omarchy-pc converged; laptop revision remains unverified. Original prompt: /home/igor/Work/plans/huyang-debug-usability-session-prompt.md. Coordinator thread: 92bdd669-fcfc-4a2c-90b1-3addf4dbe039.

Implementation: **5d244adf13c978599a267ee6f35b446faf7430c5**, pushed to feature/huyang and main on GitHub and Forgejo. Baseline: 2c22cbf24c4242eca17533866e86a9da1827e8c6. Raw participant reports are unedited; use coordinator verdicts and [findings](findings.md) to assess their claims. [Scenario manifest](scenario-manifest.json) records run provenance and terminal states.

## Results and coverage

| Participant | Baseline / assigned work | Fresh verification |
|---|---|---|
| OpenCode 1.18.30, opencode/muse-spark-1.3-contributor-free, medium/build defaults | Correct repair with debugger/static evidence; assigned static/prepared/comparison work blocked or partial; isolated restart completed | Output goals accepted. Passing/failing traces, comparison, worker write watchpoint and partial value origin accepted; cancellation recovery followed successfully |
| Claude Code 2.1.268, claude-haiku-4-5, default thinking | Repair and TypeScript rename passed tests; tracing skipped or falsely claimed after CLI fallback | First output replay used wrong endpoint. One assisted retry still failed transport/parsing and did not demonstrate the three goals |
| Codex CLI 0.154.0, requested gpt-5.6-luna | Blocked: Normandy Huyang routing permits only Sol; no costly substitution or policy change | Not run |

This is mixed cross-model coverage, not a model ranking or successful completion of all nine cards. Nine sessions ran across the baseline, assigned and targeted verification waves, including one assisted retry; two wrong-host submissions were cancelled before dispatch. Normal steward admission, maximum three concurrent assignments, supported maximum20 turns; detailed timeout exceptions are in the manifest. Measured monetary cost is unavailable.

## Confirmed changes and measurements

Experimental tools now expose UTF-8 byte-bounded source reads, revision-guarded continuation, revision-only workspace inspection, compact mutation receipts and explicit text/structured/both response formats. Full diagnostics remain available; verification results, uncertainty, refusals and recovery guidance survive compaction. Stable catalogs remain unchanged.

| Evidence | Before | After |
|---|---|---|
| Long single-line source | max_lines=1 delivered131142 source bytes | Coordinator socket check delivered1024 content bytes in2277-byte MCP result; fresh Muse useful excerpt643 content bytes in1422-byte JSON-RPC frame |
| Revision-only goal | Status2097 envelope /4506 combined characters | Coordinator266-byte envelope; independent Muse378-byte frame |
| Duplicate payloads | Same source appeared in text and structured copies | Compact defaults to one text copy; explicit structured format uses a short receipt plus structured body |
| Prepared receipt | Full details required manual filtering | Fresh Muse compact apply retained provisional outcome and accepted gaps; its explicit both-format frame was9974 bytes |

Counts use the stated envelope/frame units and are not interchangeable. Muse same168-byte source comparison measured2378-byte full/both versus922-byte compact/text frames, but guide options also differed; this is not a controlled mode-only reduction. Byte limits bound source content per target, not JSON escaping/metadata or acquisition memory. Compact source defaults64KiB; explicit bounds range4 bytes–1MiB.

Debug launch descriptions now recommend verified absolute module paths. Cancelled analysis previously misreported graph_source_changed; it now returns analysis_cancelled with cause and smaller-workspace guidance. A failing-before regression and a fresh Muse recovery validate this fix.

Haiku's assisted claim that max_bytes was ignored was rejected: coordinator replay on the same file returned512 source bytes in a2072-byte frame. The worker committed four raw reports in the coordinator checkout (62a6cf2), violating isolation; only reports changed and remain preserved, with corrections in [the audit](verify-assisted-verdict.md).

## Limits and deferred scenarios

Prepared path_unreachable proofs, non-Go/dynamic-callback coverage, exception/cancellation tracing, layout/signature comparison variants and full stale-handle recovery were not completed. Continue-after-exit guidance remains unreproduced. Value-origin identity/alias/interval uncertainty and causal unknown results are expected limits, not weakened evidence. No additional tournament is planned.

Raw evidence and per-run verdicts record assistance and unreliable participant metrics. Two oversized original debug logs remain in /tmp/verify-debug-muse-muse, with hashes in the evidence manifest; selected exact responses are committed. Do not interpret the repository archive as containing every original byte.

## Validation and publication

Full make check budget and go test -race ./internal/handlers passed; authoritative log /tmp/huyang-usability-gates-cancel.log. Optional staticcheck/golangci executables were unavailable; configured checks passed. Huyang has no configured GitHub workflow, so no source-CI success is claimed.

Normandy was upgraded through UpKeeper's regular controller pull API to clean5d244ad; service restart and19-tool MCP probe succeeded. [Controller validation](controller-validation.json) records the prepublication run. Source audit commits after5d244ad contain documentation/evidence only; the validated binary remains pinned to5d244ad.

UpKeeper capture ran from Normandy, then unrelated environment provenance was restored before publication. Release **2aa8ecc557f56a1dd8b93f68d16cf99a53ca53cd** is pushed to both configured remotes. Its manifest changes only the Huyang pin and capture timestamp. Local release gate passed233 tests, Ansible checks and4 redaction tests using an isolated Python3.11 environment; the existing Python3.14 environment was incompatible with pinned Ansible and was not altered.

[Exact release CI](https://github.com/iryzhkov/UpKeeper/actions/runs/34767567619) passed before fleet convergence. UpKeeper run20260913T160743Z-cd089171 exited0: Normandy converged, homelab and omarchy-pc updated, all at clean5d244ad with active services, sockets and successful MCP probes. [Fleet record](fleet-convergence.json) pins the exact published manifest. Laptop was skipped as self-managed; its installed revision remains unverified and it continues through its local UpKeeper pull --self timer.
