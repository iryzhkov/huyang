# Budget measurements after diagnostic helper extraction

Measured source: `9a2c9d6ffc5e50e1a29799e13a940cb6fd9ddbd3`.
Agent-a job `d8bef30c-34ea-4963-95a6-15936ff67df1` completed all five predeclared runs.
Every run passed the original 20-round budget, presentation and structural gates.
The baseline and threshold arithmetic were unchanged; the RSS limit remained 25,795 KiB.
These are individual original-gate results. The diagnostic median defines no new pass rule.

| Revision | Sample | Cold RSS KiB | Post RSS KiB | Long functions | Largest Go file | Original gate exit |
|---|---:|---:|---:|---:|---:|---:|
| c91b374 | 1 | 18172 | 23484 | 1 | 1537 | 1 |
| c91b374 | 2 | 16280 | 24032 | 1 | 1537 | 1 |
| c91b374 | 3 | 16516 | 23592 | 1 | 1537 | 1 |
| c91b374 | 4 | 18208 | 25972 | 1 | 1537 | 1 |
| c91b374 | 5 | 16136 | 22616 | 1 | 1537 | 1 |
| 9a2c9d6 | 1 | 16292 | 23004 | 0 | 1495 | 0 |
| 9a2c9d6 | 2 | 18220 | 23728 | 0 | 1495 | 0 |
| 9a2c9d6 | 3 | 18116 | 23104 | 0 | 1495 | 0 |
| 9a2c9d6 | 4 | 16076 | 23304 | 0 | 1495 | 0 |
| 9a2c9d6 | 5 | 15880 | 25308 | 0 | 1495 | 0 |

The earlier revision was measured first, with no overlap between measurement jobs.
Each invocation created its original fresh server, XDG state and project copy.
The first invocation compiled with initially absent private caches; later invocations reused those caches.
Both jobs used the same pinned Go 1.27.0 executable and Neovim 0.12.5 archive.
Every invocation verified all tracked source files and the unchanged baseline before and after its scenario.
No extra warm-up, GC forcing, replacement samples or best-of selection was used.

The earlier revision's five gates failed their existing structural checks; sample four also exceeded the RSS limit.
The corrected revision's five gates passed. Their post-RSS range was 23,004–25,308 KiB, with diagnostic median 23,304 KiB.
The earlier revision's range was 22,616–25,972 KiB, with median 23,592 KiB.
Overlapping distributions show variability in this measurement; these ten observations do not attribute a specific runtime or GC cause.

The initial corrected-revision run remains failed: job `4c1d5c26-9e0d-4b91-aa7b-5566a52adbfd` recorded 25,836 KiB, 41 KiB above the limit.
Its unmodified JSON is retained as `initial-failed.json` beside all five successful reports in `docs/plans/budget/2026-10-17-9a2c9d6-replicates/`.
The initial earlier-revision run `7647170c-0833-4708-b97c-4ce14578eaea` also remains failed for structural checks.
No historical failure is reclassified or removed.

Complete archives: c91 job `b3337f0c-4fa1-440a-854d-281739fffac8`, 39,146 bytes, SHA-256 `aa7750a7177b10beeec518956a580c340b8aa931322dfeecf137b4a570eacd64`;
9a2 job `d8bef30c-34ea-4963-95a6-15936ff67df1`, 39,375 bytes, SHA-256 `fdfd5cff64ff9bbacec6f1cfff5b9c110a039cedcca3828ae93678c0c55329ba`.
Both archives preserve every raw log, original gate exit, source verification, baseline proof and completion status.
The comparison wrapper was accepted by independent Opus 5.5 high review, round two; its digest is `cefdd2c6a4194f25e424abbef802b05392acaf4b5e60dc54f39c86053bf9983e`.
Nested timeout groups remain a documented collection limitation; no timeout or completion marker alone proves descendant cleanup.
