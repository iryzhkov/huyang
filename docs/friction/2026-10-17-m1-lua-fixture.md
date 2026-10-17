# M1 Lua fixture repair — 2026-10-17

Base: `869f0bf`.
Assigned isolated worktree: `/home/igor/.t3/worktrees/huyang/t3-b13a71c0`.
Only `tests/unit_edit.lua` and this new report are changed.

## Failure and fixture fix

The late-pull fixture replaced `vim.lsp.get_clients` globally with an incomplete
protocol stub. Native Neovim buffer callbacks could therefore see the fake client:
Neovim 0.12.5's change tracking accessed its missing `flags`, and error-exit
`VimLeavePre` attempted its missing `stop` method.

Fake-client enumeration is now limited to the coroutine executing
`edit.diagnostic_evidence`. Buffer edits, response waits, collection, and native
exit callbacks use the saved real client registry. The fixture also intercepts
`vim.lsp.buf_attach_client` for the fake ID, so the later progress-test override
of `get_client_by_id` cannot register the partial object with native buffer
tracking. Other client attachment requests use the original function, and the
attachment stub is restored with the existing discovery stubs at fixture cleanup.

All existing assertions remain unchanged, including delayed responses after a
buffer change, queued positive and empty responses followed by each invalidation,
and collection-time progress checks. Production diagnostics logic is unchanged.

## Validation pending

No build or runtime test was dispatched by this child. Parent Chaldea job
`63038d7d` owns the sole local slot; the lead will validate this source commit in
a short follow-up job after that job's confirmed completion.

Required follow-up on `agent-a` through `chaldea exec --host agent-a`:
- Neovim 0.12.5: `nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_edit.lua`.
- Neovim 0.11.5: the same unit suite with that version's binary.
- The bootstrap selftest requested by the lead.
- Set `HUYANG_FRICTION=0` on every `go test` invocation.

Local source check before commit: `git diff --check`. Runtime compatibility with
both editor versions remains unverified until the follow-up job completes.
