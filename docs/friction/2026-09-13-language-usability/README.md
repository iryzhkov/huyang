# Live language-repository workshop — 2026-09-13

Two bounded live sessions completed operationally: Lua/Haiku succeeded; Python/Muse timed out after completing the target refactor. Both target changes independently pass focused tests. Broader usability coverage remains incomplete.

| Language / repository / model | Accepted outcome | Friction and limits |
|---|---|---|
| Lua / plenary.nvim / claude-haiku-4-5 | Private helper renamed without altering public API;125 baseline and126 after tests passed | Literal navigation only; prepared and compact/revision goals skipped; report used Write despite Huyang-only instruction |
| Python / UpKeeper / opencode/muse-spark-1.3-contributor-free | Compatible cross-module rename;33 selected tests passed | Prepared check refused .venv write; schema retries and helper-name/shape confusion; timed out before report or output measurements |

The strongest new finding is **environment setup during prepared checks**. In the trusted Python clone, prepare refused an undeclared .venv write and correctly left canonical source unchanged. Recovery offered inspect/discard without environment-setup guidance. At the same baseline in an untrusted clone, the rename prepared provisionally with checks explicitly unavailable. This separates semantic rename success from test-environment readiness; it does not justify weakening sandbox write guards.

A second coverage limit: Pyright's6 rename edits covered3 Python files but omitted an extensionless Python script. Literal search found that caller. Both participants needed coordinator auditing: task completion did not establish that all requested tool workflows were exercised.

[Python audit](python-verdict.md), [Lua audit](lua-verdict.md), [findings ledger](findings.md) and [manifest](scenario-manifest.json) contain provenance, dispositions and limitations. Raw artifacts remain unchanged under raw/. Python produced no report; its target commit, thread archive, timeout outcome and exact durable refusal were recovered instead.

Normal steward admission only,2 sessions, maximum20 turns and15 minutes each, no retry wave. Owned target clones preserved production repositories. Transport helper access and piping guidance were explicit assistance. The Python prompt's missing space before its baseline SHA caused one avoidable Git retry.

No Huyang runtime fix was made from this discovery session. Test-environment preflight/recovery is a scoped follow-up, not a demonstrated source correctness defect; semantic coverage and helper errors have explicit dispositions. Huyang remains deployed at5d244ad via UpKeeper release2aa8ecc. Experiment refactors remain in isolated clones and are not production changes. The audit is published as documentation only; no new binary or fleet capture is warranted.
