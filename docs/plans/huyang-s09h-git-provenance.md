# Huyang S09H read-only Git source provenance

S09H adds bounded local Git history as an optional provenance layer. Git remains absent from
the workspace correctness path and no Git mutation surface is introduced.

## Delivered scope

- Project workspace opening returns at most three first-parent commit summaries. Entries use
  epoch/TTL-bound opaque commit handles and include abbreviated object ID, subject, author
  display name, authored/committed timestamps, changed-path count, and parent count. Email,
  commit bodies, and path lists are not part of the automatic overview.
- File, line/range, and semantic-handle history uses bounded porcelain blame over the exact
  canonical bytes. The same core API accepts prepared bytes and marks their unmatched spans
  `derived_from_plan`; canonical unmatched spans are `uncommitted`. Git attribution is
  retained only for unchanged spans that Git maps to committed objects.
- History results include recent touching commits plus named file-age metrics: ref,
  first-parent traversal, bounded rename-follow policy, first-parent commits since last
  change, last-change time, and introduction evidence. Shallow or rename-ambiguous
  introduction evidence is provisional.
- Opaque commit handles open bounded changed-path and patch views. Merge changes are explicitly
  first-parent diffs. Binary and gitlink/submodule changes are typed rather than decoded as
  source.
- `search(source.git_history)` searches bounded local message, path, and patch data and
  returns a historical result-set handle. Historical sets remain ineligible for current-source
  all-match editing.

## Inert Git boundary

All Git subprocesses are local read operations with a ten-second deadline and two-MiB output
cap. Invocation fixes the working root, disables prompts, optional locks, hooks, pagers,
external diffs, textconv use, fsmonitor, credential helpers, submodule recursion, and network
protocols. Git environment variables capable of redirecting the repository, object store,
index, configuration injection, replacement refs, grafts, SSH, or askpass are removed.
Repository replace/graft history is disclosed as incomplete coverage.

No fetch, remote, ref update, checkout, index write, worktree write, hook, pager, external
diff, textconv, or repository-configured executable is used.

## Coverage and degradation

Every result names ref, traversal, and rename policy and independently reports shallow,
rename-ambiguous, missing-object, binary, submodule, and truncation states. Document workspaces
and non-Git projects keep their normal text behavior and return Git as unavailable. Search,
touching-commit, change, patch, and blame outputs are capped at semantic record boundaries.

## Verification coverage

Focused race tests cover bounded first-parent overviews, merge parent/change shape, rename
following, shallow introduction evidence, missing objects, dirty and prepared content mapping,
binary files, gitlinks/submodules, historical result typing, commit change views, and local
message/path/diff search. An adversarial repository config installs pager, external-diff,
diff-driver, hook, and fsmonitor executables; all history operations complete without executing
them, and HEAD, index bytes, and worktree status remain unchanged.

The official MCP SDK test opens a Git project, receives recent commit handles, reads file
history, searches historical data, and opens a commit change view through the frozen modern
`workspace_open`, `read`, and `search` tools.

## Stage boundary and residual risk

S09H is read-only provenance only. It does not add plan persistence, transaction intent,
prepared-plan transport, mutation wrappers, Git control operations, installation, deployment,
or live-service changes. Prepared-byte mapping is a core API consumed by the later transaction
stage once prepared plans exist; S09H does not create that state early.

Git history quality is bounded by the local object database and Git's rename heuristics.
Shallow, missing, replacement/graft, ambiguous rename, binary, and submodule cases are exposed
as coverage limits rather than guessed provenance.
