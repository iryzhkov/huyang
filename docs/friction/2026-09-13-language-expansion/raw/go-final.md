Done. Go rename QuoteTotal→InvoiceTotal with DiagnosticName preserved.

- Target: `/home/igor/.local/state/t3-steward/backlog-v2-workspaces/workers/9a9842d1ddc92dee/run-bdfe96273d69c1350bdd36057b2f4181/task-60405ccd79403a3bdb2cc4d732b4b651/attempt-41847136b9a6d1501dbd542ff22d1a88/workspace/target-go`
- Baseline: `c2d4301ed945198470cee1b95a54f5b97ee52bd2` (fixture)
- Result: `8651b28c8019406870a7d81dc698fe914687fa56` (rename + TestInvoiceTotalEdge)
- Worker report commit: `6db1a89` in `worker-go-expansion`
- Raw artifacts: `worker-go-expansion/docs/friction/2026-09-13-language-expansion/raw/go.md`, `raw/go-evidence/{prepare-rename.json 11504B, apply-rename.json 13182B, read-concise.json 1695B, workspace-revision.json 1523B, baseline-test.txt 43B, final-test.txt 43B, edge-test.txt 118B}`
- Tests (30s timeout, /tmp/go-exp-attempt-41847136…/): baseline `go test ./...` ok 0.007s exit 0; final `go test ./...` ok 0.002s exit 0; `TestInvoiceTotalEdge -v` PASS; `go vet` exit 0. Edge covers 0→0, 100→700, DiagnosticName stays "QuoteTotal".
- Goals: 1 completed (gopls attached, semantic 3 vs literal 5, strings excluded), 2 completed (READY prepare plan_2c06…/prep_adbbe…, exact apply wsrev_4→5, no provisional), 3 completed (max_bytes 120 truncated 120/139 + revision-only wsrev_5), 4 completed. Omissions: unknown-tool error not exercised, no full verify_run, no LS restart (already attached). Retry: prepare tee race → replay → fresh-key prepare.
- Unknowns: coordinator files beyond 3 fixtures not inspected; Huyang baseline taken as given. Nothing pushed, no gitlink.

BACKLOG STATUS: done