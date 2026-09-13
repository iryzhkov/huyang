# Friction logs and their verdicts

Round two asked agents to do real work in a real repository with Huyang as the
only way to read, search and change files, and to record what the tool made
hard while they did it. Four scenarios ran on 2026-09-13 and finished; two
more - a byte bound for `read`, and a TypeScript rename - were cancelled
before they started and their work is still owed.

The logs in this directory are the agents' own, unedited. This file records
what was done about each finding. Where round one produced defects in the
API's honesty, round two produced defects in its usability, and one of them
could have corrupted a repository.

| Finding | From | Verdict |
|---|---|---|
| `paths` filtered hits after the match cap, so a scoped search of a common term could answer nothing, and the frozen set held files outside the scope | replace-matches, 1 and 3 | fixed, `4e40a19` |
| `create_file` fails on a missing parent directory, with no next step | replace-matches 7, citadel | fixed, `36a4250` |
| Two untracked scaffolding symlinks make every search incomplete, which makes every all-match replacement ineligible | replace-matches, 4 | fixed, `adddf96` |
| `read {path: ...}` is rejected as an unknown property without naming the shape that was wanted | replace-matches, 2 | fixed, `70cdf09` |
| A six-site migration produced 79 KB of patch, and `committed_diffs` came back with null bodies | replace-matches, 6 | fixed, `2965796` |
| `make live` cannot pass in a worker checkout: the kernel's path is too long for Neovim's byte-compile cache | replace-matches, 8 | fixed, `e0bfae3` |
| A frozen result set stays valid while a plan is built and fails loudly once canonical moves | replace-matches, 5 | no defect; the scenario's question, answered |
| A cold non-Go workspace orients correctly and cheaply, and a two-target read is the best call of the run | citadel | no defect |
| Pyright reports the compiled schema validator as not callable | citadel | not ours: a third-party analyzer's answer about a Python pattern the repository runs successfully |
| `workspace_inspect view=status` answers scheduler, pipeline, provider and limit structures when the caller wanted the current revision | citadel | open: a narrower view is worth having and is not yet designed |
| `revision_diff` refused with `diff_evidence_incomplete` after a shell-created directory | citadel | expected: the service did not observe that write, and says so rather than guessing |
| Pushing to `ssh://igor@normandy/...` fails with exit 64 | citadel | not ours: the key's forced command on that host; the run recovered through a `file://` URL |
| A shared checkout left on a scenario branch blocks a worker's push | citadel | not ours; the preamble's rule against working in `/home/igor/Work` is what keeps it rare |
| Reading a 241-line file whole after an outline was the right call, and the outline made it obvious | sectioner-docs | no defect |
| A compact JSON fixture is 17 lines and 4 KB, so line counts understate what a read will cost | citadel | open: this is the same gap as the missing byte bound on `read`, which round two was meant to close and did not |

The two cancelled scenarios leave one real defect open: `read` has a line
bound and no byte bound, recorded in `../probes/README.md` as bounds 1 to 3
and still deferred.

The work the four runs produced is merged here: the sectioner tests they
wrote, the migration they staged, and these logs.
