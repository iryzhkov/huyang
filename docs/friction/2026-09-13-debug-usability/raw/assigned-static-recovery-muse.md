# Workshop assigned-static-recovery-muse — raw report

Session: assigned-static-recovery-muse | Host: normandy | Provider: opencode | Model: opencode/muse-spark-1.3-contributor-free
Baseline HEAD: 2c22cbf24c4242eca17533866e86a9da1827e8c6 (verified `git rev-parse HEAD` 2026-09-13T15:22Z)
Binary: /home/igor/.local/share/huyang/bin/huyang | `go version -m` go1.27.0-X:nodwarf5, module github.com/iryzhkov/huyang, vcs.revision 2c22cbf24c4242eca17533866e86a9da1827e8c6, vcs.time 2026-09-13T10:46:59Z — matches baseline.
Shared endpoint (never restarted): /run/user/1000/huyang/control.sock (srw 2026-09-13 03:47).
Isolated clone: /home/igor/.local/state/t3-steward/backlog-v2-workspaces/workers/9a9842d1ddc92dee/run-c68eb02e7cc416c9574ed8d5f2af7e3e/task-b128a95823a934551f8076f3f326c6de/attempt-84209467b683ea398255fee0e84873d7/workspace
Branch: worker/assigned-static-recovery-muse (created from detached HEAD 2c22cbf).
Shared-workspace ID: ws_9594a3735aefa6492ee88644ef516260 (project, rev wsrev_1 at open, wsrev_4 after fixture copies, wsrev_11 after evidence copies).
Owned daemon: SAME baseline binary, owned socket /tmp/huyang-owned-static-recovery/sockets/control.sock, owned state /tmp/huyang-owned-static-recovery/state. Started 2026-09-13T15:23:01Z PID 1462342, restarted 2026-09-13T15:29:45Z (kill + serve, socket recreated 08:29). Never touched shared service. Owned workspace ID: ws_ad940c7199cf22fa3573011a6a9d23cb, rev wsrev_1.
Fixtures (Huyang copy_file, hashes match originals): workshop-assigned-static-recovery-muse/go.mod b1bc4f2b75954f57215c5f43fef93369ae77a25f9f897973a055a30a30e3bcf0, main.go 39d143cf6f3082f595dd60216b05eb9da7341bb07c6f72ba961e7bba9750f82e, main_test.go aa405f1d2ea2e8b9a016dcbe767aa68b6af621d5e31014332e3b3afdda290c22.
Transport adaptation (assistance): experimental catalog via `huyang mcp --profile experimental --socket <owned-sock>` newline-delimited JSON-RPC; must keep stdin open (printf + sleep), literal socket path (single-quote bash -c loses env -> dial unix missing address), idempotency_key required. Tools/list 21 tools saved in evidence.
Checkpoint note: 20-turn/15-min budget forced collapsing per-card checkpoints into this raw report; transcripts saved incrementally.

## Card 1 — completed with help
Normal tools (req_319, req_284): paths main->run->dispatch->deliver (credits 1) and main(fail)->run(0)->dispatch->rejected (deliver skipped). Condition dispatch credits<=0 guards deliver. Async run go func + channel. Calls vs data: deliver call edge from dispatch; credits data along same path; channel result async data back. Dynamic closure is partial-coverage case. Experimental path_explain failed code graph_source_changed (req_7, req_9, wsrev_1, evidence card1-*.out). Refusal preserved. Limit: partial graphs cannot prove unreachability.

## Card 2 — blocked
change_plan preview + debug start without idempotency_key -> error code 0 missing required property idempotency_key (evidence card2-*.out). No plan prepared/applied; canonical unchanged. Retry hint: create with idempotency_key, preview impact, prepare invariants path_unreachable/tests_pass, inspect semantic, discard.

## Card 6 — blocked (program observation only)
go test FAIL credits=0 got rejected want sent:0; go run . sent:1 exit 0; go run . fail rejected exit 1 (2026-09-13T15:29:32Z). Huyang compare_traces + layout/signature repeats not exercised. Fixture test does not validate trace workflow.

## Card 7 — completed with help
Owned restart done (same binary, owned socket/state, never shared). Static queries while no debug session active done. Stale-handle/unfinished-trace recovery not fully proven. Cleanup: no debug sessions created; owned daemon to be stopped before commit.

Evidence: docs/friction/2026-09-13-debug-usability/raw/assigned-static-recovery-muse-evidence/ (7 files, req_324, wsrev_11).
