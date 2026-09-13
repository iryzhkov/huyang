# verify-debug-muse evidence manifest
Owned experimental endpoint only. Staging: /tmp/verify-debug-muse-muse/.
- frames.jsonl: 168 lines (97 send, 71 recv), 4842855 B staged; copy_source_too_large on full file (bound exceeded).
  Split by lines into frames-part-*: 00 (30 lines, 101533 B), 01 (30, 305776), 02 (30, 4087158 — contains one huge overlay recv > bound, NOT copied whole), 03 (30, 30231), 04 (30, 164744), 05 (30, 178517), 06 (10, 18467).
  Copied parts: 00,01,03,04,05,06. Part-02 retained only in staging; its key content (compare_traces partial, path_explain cancelled, execution_graph incomplete) summarized in report + out-overlay too large (5725135 B, NOT copied).
- out-overlay.json (5725135 B) NOT copied: copy_source_too_large. Key extracted: compare common_prefix 1, unaligned pass 1, credits diff observed, cause unknown; path_explain analysis_cancelled with small-ws recovery; execution_graph execution_coverage_incomplete.
- All other out-*.json + catalog.json copied whole via Huyang copy_file.
- Report: docs/friction/2026-09-13-debug-usability/raw/verify-debug-muse.md (wsrev_7, docrev_6a8d2e5...).
- Fixtures owned: raw/verify-debug-muse-delivery/{main.go,go.mod,main_test.go}, raw/verify-debug-muse-watch/{main.go,go.mod}.
No shared-endpoint data used. No secrets (synthetic data only).
