# Coordinator audit: Muse static and recovery

Run run-c68eb02e7cc416c9574ed8d5f2af7e3e, thread thread-49bf904f5bf2ec59cabde926f7b01c63, commit e116d2a50ddbda06285162dc3d6b93cd8916a532. Report, seven worker evidence files and steward final/thread retained unchanged. Baseline fixture was preserved. No worker code merged.

Card 1: partial. Source-derived path/guard explanation agrees with ground truth, but two experimental path queries refused with graph_source_changed and no next action. Card 2 blocked after omitted-idempotency-key schema refusal; no prepared invariant experiment. Card 6 blocked/unexercised. Card 7 partial: owned same-binary restart and queries while not debugging completed, but stale handle/unfinished trace recovery not fully exercised. These are bounded incomplete observations, not full card completion. Worker candidly reports the missing work.

The original graph refusal could reflect the analysis budget rather than edited source. Coordinator regression demonstrated that validate with cancelled context on unchanged source returned graph_source_changed. Fix explicitly reports analysis_cancelled with interruption cause and smaller-workspace recovery, and preserves source acquisition errors. Regression failed before and passed after. Original transcript alone does not prove this was the cause of req_7/req_9; keep that distinction. No source revision or proof safeguards are weakened.

Broad repository acquisition remains bounded; fixture/module workspace scope is appropriate for targeted debugging. A fresh verification wave will target output controls, launch discovery and truthful trace/value-origin evidence. No endless retry tournament is authorized.
