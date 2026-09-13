# Recovery fixes and five-language expansion

Active user request: fix prior session issues and run C, Java, JavaScript/TypeScript, Go and C++ live sessions. This work is NOT complete. First wave C/Muse, Java/Haiku, JS+TS/Muse accepted exactly once (three submission-accepted events verified). C++/Haiku and Go/Muse prompts ready, not submitted yet; submit after first-wave capacity clears. Normal scheduling, no overrides, maximum3 active assignments. All five synthetic source fixtures compile/test successfully on coordinator (C clang, C++ g++, Java21, Node26 TypeScript stripping, Go). Originals are copied by Huyang into owned repositories; fixture tests alone do not validate usability.

Implemented locally, not committed/published:
- Specific undeclared-write recovery now precedes generic prepare-failure recovery. Regression failed before and passes after.
- Verification audit error names stage and command while preserving wrapped code and rollback.
- Recovery explicitly says dependency environments must be prepared separately and non-mutating checks must remain read-only; no automatic .venv permission.
- Rename receipts warn that LSP edits are not exhaustive, suggesting review of remaining literal matches, including extensionless scripts. This is an evidence-boundary fix, not fabricated complete alias analysis.
- Workshop helper --describe returns one object; unknown wire name returns typed error and valid names, exit2. Tested on installed shared endpoint.

Focused handler/workspace tests and full make check budget passed (exit0): /tmp/huyang-language-fixes-gates.log and .exit. Post-fix owned-daemon MCP replay confirmed compact undeclared_tool_write receipt names the command, includes environment recovery, and leaves canonical new.txt/.venv absent; exact receipt in recovery-after.json. Review final gate, fix failures, add appropriate receipt coverage tests if needed, and validate Python recovery on new owned binary before publication. Need source commit/trailer, feature+main to both remotes, controller validation through UpKeeper, reviewed capture preserving unrelated pins, exact release CI, then fleet convergence. Current installed source remains5d244ad and release2aa8ecc; don't restart baseline workers' shared daemon mid-session.

Known fresh observation: Java fixture creation reported lsp_attach_deadline_exceeded twice despite installed jdtls and working javac21. Participant should assess isolated project attachment; no defect conclusion yet.

Next on wake: inspect repository and run state, collect exact results, audit target commits/tests and provider evidence. Preserve raw feedback; fix confirmed new issues. Submit C++/Go when capacity allows (prompts under prompts/). Source checkout before this request was clean2615bf0; all current dirty changes belong to this request. No unreviewed participant source merges.
