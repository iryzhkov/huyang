# Huyang S19 legacy wrapper migration

Captured: 2026-09-10  
Starting commit: `3edeaef629cbbfa4a70fd7fefb200849693abfdd`

S19 migrates `replace_symbol_body`, `replace_symbol_lines`,
`insert_after_symbol`, `insert_before_symbol`, `insert_lines`,
`create_file`, `move_file`, and `delete_file`.

Symbol and range operations retain the canonical provider so guarded targeting, parser
checks, import organization, diagnostic deltas, code-action tokens, and per-client undo
state remain byte-for-byte compatible. Their unsaved buffers are captured into an isolated
sandbox, converted to an exact prepared delta, and committed through one durable journal
before the provider accepts the canonical disk image.

File lifecycle operations execute against an isolated sandbox through the same provider.
Their exact tree delta is journaled into the canonical workspace. Retained receipts let
legacy undo reverse create/move/delete without crossing client ledgers; stale regions remain
guarded and `skip=true` retains its legacy behavior. Sandboxes and diagnostic aliases are
released when their receipts are consumed or the workspace closes.

The old direct-provider path remains available with
`HUYANG_LEGACY_WRAPPERS=direct`, or per tool with
`HUYANG_LEGACY_<TOOL>_PATH=direct`. Dry runs retain the direct read-only path. Calls with
`wait=false` also remain direct because their contract requires returning before the deferred
verdict exists; synchronously materializing and committing a journal would consume that verdict.

Focused evidence:

- `TestLegacyCreateWrapperMatchesDirectSnapshotAndCommitsJournal` compares the exact
  legacy response and bytes with the direct path and proves one committed journal exists.
- `TestLegacyWrapperEscapeFlagsRetainDirectPath` freezes both escape mechanisms.
- Workspace tests cover exact replace/create/delete sandbox discovery, canonical drift
  refusal, the single-operation journal marker, finalization, and state-sequence advance.
- Existing smoke groups cover guarded relocation/refusal, parser failures, formatter/import
  polish, diagnostic attribution, code-action recovery, file lifecycle undo, and per-client
  isolation.

No debugger wrapper, rename/move-symbol/replace-pattern migration, deployment, live
configuration, provider restart, push, or pull request is part of S19.
