# M1 correction: language-server recency and enumeration

Base: `b035d892932087aff67ee618303b9be3eeda51b8`.

The limiter's `vim.lsp.get_clients` wrapper previously treated every returned
client as used by the active tool request. Global and name-based housekeeping
scans refreshed all recency values in client-ID order and pinned every client
until the request ended. Diagnostic baseline collection also visited every
loaded buffer through the wrapper. These scans could replace LRU with creation
order or prevent a new language from evicting an otherwise idle server.

## Change

- Add `limits.list_clients(filter)` for enumeration without recency or request
  ownership changes. Diagnostic reuse scans and baseline lookups use this path.
- Only filtered lookups for buffers already touched by the active request
  refresh recency and hold returned clients. Global scans, name scans and
  lookups for unrelated loaded buffers stay neutral across the runtime.
- Keep explicit buffer use, start/reuse and asynchronous attachment accounting.
  Active RPC ownership, outstanding LSP requests, progress, unsaved buffers,
  transactions, debugging and stop-until-exit capacity rules are unchanged.
- Extend `tests/unit_limits.lua` with gopls-versus-bashls/clangd LRU regressions
  for global, name, unrelated-buffer, raw and real baseline enumeration; a
  real diagnostic collector new-language replay at capacity; and reattachment
  during a request after enumeration. Existing tracked-child checks remain.

Only limiter behavior and diagnostic client enumeration are changed. Baseline
diagnostic semantics and Go ledger code are outside this correction.

## Validation

Runtime validation is explicitly deferred. The attempted agent-a dispatch was
refused because another local Chaldea exec was running. **Nothing dispatched**;
refusal journal ID: `545bbf91-fd19-49c3-bc7c-8234a3be5945` (2026-10-17).
No build, Lua suite, Go test or selftest ran for this correction. The lead
instructed this worker to commit the prepared change and leave one later
integrated dispatch to the lead; no retry, queue or process interruption occurred.

Source review and `git diff --check` pass. Huyang mutation diagnostics were
provisional and are not runtime test evidence.

The pending checks are `tests/unit_limits.lua` and `tests/unit_edit.lua` on
job-local Neovim 0.12.5 and 0.11.5, plus
`bash tests/langprobe-chaldea.sh selftest`, through `chaldea exec --host agent-a`.
Use `HUYANG_FRICTION=0`, umask 022 and a short canonical cache TMPDIR.
Only the two Neovim archives are needed for these Lua checks:

- 0.12.5 SHA-256: `bce0f56eda1f1b1db6eee8f4133d7a38813ea07933837dd1777411ca384c6875`
- 0.11.5 SHA-256: `b2f91117be5b5ea39edd7297156dc2a4a8df4add6c95a90809a8df19e7ab6f52`

This correction has no runtime validation job ID or result. Prior integrated
jobs predate this change and cannot validate it.

## Follow-ups

The review's separate stopped-client progress-by-name, empty runtime hook,
unseeded immediate provisional state and timer lifetime notes remain for their
respective owners. They are not changed by this focused recency correction.
