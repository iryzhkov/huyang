C rename complete.

- Absolute target: `/home/igor/.local/state/t3-steward/backlog-v2-workspaces/workers/9a9842d1ddc92dee/run-29f22f1d12b98aba6706032819d1748d/task-f2cde001d500212cde0440bcd3840686/attempt-dab74e8dd7f16c53f8accba2cc977d91/workspace/target-c`
- Target baseline: `68b56f3` initial C fixture; result: `9320760` rename quote_total→invoice_total + edge test
- Worker report commit: `60f9b50` in `worker-c` (`docs/friction/2026-09-13-language-expansion/raw/c.md` + `c-evidence/` 7 frames)
- G1 completed: semantic (clangd attached, symbol_find 2 decls; refs @api.c 3 code-only, @api.h 1 + disagree note) vs literal 6 (3 code + 2 strings + decl); strings distinguished
- G2 completed with omission: prepared plan `plan_67ccf18...` PROVISIONAL (3 ops, missed `c/api.h`), evidence `ev_d82a4f08...` incomplete; applied exact `prep_783d...` with `accept_provisional=true` → `wsrev_5`; header via separate `replace_literal` → `wsrev_6`
- G3 completed: `workspace_inspect view=revision` → `wsrev_6` (1523B frame); `read max_bytes=64 compact` → 64/65B truncated + continuation; frames archived before printing
- G4 completed: edge `assert(invoice_total(0)==0)` → `wsrev_7`; final literal `quote_total` 2 strings-only, `invoice_total` 5; `clang -std=c11 -Wall -Wextra -Werror api.c main.c -o /tmp/dab74e8d-c-test && /tmp/dab74e8d-c-test` exit 0 (baseline + final, 30s timeout)
- Unknowns: header missed by LSP rename; header-anchored refs incomplete; C `verify_run` unavailable (no `.huyang.toml`)

BACKLOG STATUS: done