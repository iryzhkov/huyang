# Fresh Haiku verification audit

Run run-99478aca3c9a0946c25bb2f89c448fc7, thread thread-e3d95b35ba9a05c1b6f44754c7103204, worker report commit f86d250 (resolve full hash from handoff clone before final ledger). Raw report and steward final/thread retained unchanged.

Disposition: invalid new-version verification. The worker started /tmp/huyang-5d244ad-workshop on an owned socket, but the archived thread contains normal configured mcp__huyang calls and no connection through the new experimental MCP transport. Workspace ws_4554cfa922ba87a0d34ce479f4a5b798 belongs to the shared old service. It incorrectly concluded revision-only inspection was unavailable.

The rename fixture independently passes node test.ts. This validates the edited fixture, not new response controls. The purported compact receipt is a hand-written report summary, not a measured compact tool response. Goal3 used shell byte extraction and estimated a511-byte MCP response; no tested new byte-window call supports that number. Claims about file_range bounded reads were untested. Do not include these numbers in before/after metrics or count this as successful cross-model validation.

Preserve incorrect claims as answer-quality evidence. No implementation regression established. Transport setup was specified but not followed; a bounded assisted transport retry may distinguish access failure from feature discovery, after current Muse handoffs. No broad extra workshop wave.
