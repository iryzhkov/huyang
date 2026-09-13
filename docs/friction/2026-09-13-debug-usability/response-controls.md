# Experimental response controls

These controls are opt-in in the experimental MCP catalog. Existing profile schemas and default payloads remain compatible.

For a bounded source excerpt, call read with target.path, max_bytes: 1024 and response_mode: "compact". The cap counts UTF-8 source content bytes, including line-number prefixes; metadata and JSON escaping add wire bytes. It is per target and does not bound source acquisition memory. Compact reads default to 65536 source bytes when no explicit cap is given.

When byte_truncated is true, repeat the same target, view, line selection and numbered options with the returned continuation fields byte_offset, expected_revision_id and max_bytes. Offsets refer to that selected/rendered content, not absolute file offsets. UTF-8 characters are not split. Revision changes refuse continuation with read_revision_changed and no source content. Nonzero offsets without the preceding revision refuse with read_revision_required. Explicit max_bytes is 4 through 1048576. Outline/history views do not support byte windows.

For a focused workspace refresh, call workspace_inspect with view: "revision" and response_mode: "compact". This returns the current workspace revision without provider or pipeline status.

response_mode: "compact" sends one text payload by default. response_format: "text" explicitly omits duplicate structured data; "structured" sends structured data with a short text receipt; "both" explicitly keeps both copies. These transport choices are available across the experimental catalog.

For edit_apply, compact mode returns the mutation outcome, revision, changed paths and verification status, retaining warnings, evidence and recovery. Set include_diagnostics: true for diagnostic details, or follow up with diagnostics. Failure/conflict recovery is not shortened. Presentation does not alter mutation execution or durable receipts.

Socket regression (2026-09-13): 131085-byte one-line input, max_bytes 1024, text-only result 2277 bytes; revision envelope 266 bytes. These are fixture measurements, not a promise that total JSON size equals max_bytes. Original byte-volume baseline is in volume-baseline.json. Fresh participant verification and publication remain pending.
