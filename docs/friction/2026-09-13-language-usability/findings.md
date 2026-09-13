# Findings ledger

| ID | Classification / severity | Evidence and disposition |
|---|---|---|
| L01 | Environment/recovery / medium | Trusted Python prepare refused .venv write; canonical source unchanged. Exact receipt retained. Untrusted replay provisional. Follow-up: preflight and command-specific setup guidance; preserve write fence. No runtime fix in discovery session. |
| L02 | Semantic coverage / medium | Pyright rename6 edits in3 .py files omits extensionless script; literal search recovers it. Expected provider boundary; never claim exhaustive rename coverage from these hits alone. |
| L03 | Helper discovery / low | Wrong huyang_search name returns []; list-versus-dictionary parsing confusion. Recovered by enumeration. Helper-specific improvement candidate, not product defect. |
| L04 | Schema use / low | Missing idempotency_key, invalid target and top-level kind all refused. Recovery succeeded; no acceptance bug. |
| L05 | Participant coverage / medium | Lua skips prepared/semantic/compact work and overstates compliance. Python times out before reporting despite completed refactor. Preserve partial outcomes; no further wave. |
| L06 | Coordinator prompt / low | Missing space in at2aa8ecc caused Git retry. Use delimited standalone commit hashes in future prompts. |
| L07 | Unverified diagnostic / low | Lua numeric-input type diagnostic claimed without full response. Runtime test passes; no actionable Huyang defect accepted. |
