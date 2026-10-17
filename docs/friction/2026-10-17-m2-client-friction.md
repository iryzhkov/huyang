# M2 client paths, argument tolerance and recovery — 2026-10-17

Implementation branch: `t3/m2-client-friction`, prepared on the M1 tip `7455e65` and
rebased onto `main` at `2a61b61` (the PR 17 merge) after M1 merged, without conflicts. The starting point is the
prototype commit `75dff44` (parent `ef16bef`, ten files), cherry-picked without conflicts;
the M1 diagnostic code, caps, probes and tests are untouched. Source proposal:
`jocasta:006bf908b36a24860685792cf95cbfc7@1`, sections 6a to 6c, 9 and 10; roadmap item M2.

## Evidence

The fleet friction spool counted these refusals (84,748 calls, re-checked by the round-1
audit):

| Class | Refusals | Answer before | Answer now |
|---|---|---|---|
| `read` of a directory | 3,142 failed batch targets + 427 single reads | `read_failed: ... is directory, not regular text`, empty `next` | bounded listing |
| `read targets` with absolute paths and no workspace | 573 | `workspace_required`, even though a single `target` opened a document | one exact documents workspace over the named files |
| relative `root` (`"."`) | 473 opens | resolved against the service's own directory | resolved against the client's directory, with a warning |
| path through a symlink leaving the root | 453 | `read_failed`, empty `next` | refusal kept; `next` reads the resolved file as its own document |
| `search include` | 1 (plus 11 other scope spellings) | `did_you_mean: include_handles` | read as `paths` with a warning |

`start_line` past the end and an empty `search` query with `paths` were the remaining
empty-`next` refusals named in proposal 6c.

## What changed

From the prototype (`75dff44`, unchanged in substance):

- The adapter announces its client's working directory in the control hello and, when the
  client exports no session id, names itself `adapter-<random>`; both are kept across
  reconnects. `resolveClientPaths` resolves a relative root against that directory
  (following symlinks before `..`) and supplies a default root for read-only tools only.
- Read options nested in `target` are hoisted, a string `target` is `target.path`, an
  integral search `limit` above the maximum is clamped, a malformed JSON string is refused
  with its syntax-error offset, and a relative path with no workspace gets
  `next: resend_with_root`.

Added here:

- **Shared bounded listing** (`internal/handlers/listing.go`). `listWorkspacePaths` lists
  what search would read, through the new `Workspace.ScopedPaths`: the Git inventory or the
  bounded walk, scope applied before the file cap, `.gitignore`, depth and cap rules
  honoured, nothing followed past a symlink. A directory read lists immediate children
  (directories with their file counts) bounded at 200 entries; an empty-query search lists
  the files under `paths` bounded by `limit`. Both report `coverage` when incomplete and say
  when they were cut.
- **Directory reads** answer the listing, in single and multi-target reads. A directory
  named by absolute path with no workspace is listed through the project workspace of the
  Git working tree that holds it (it was named absolutely, so there is one meaning, and a
  listing only reads); outside any working tree it is refused with the call that lists it.
- **Implicit documents workspace for `targets`**: every target names an absolute path, so
  one exact documents workspace is opened over just those files. A relative path, a handle
  or a directory in such a batch is refused whole, with the repair for that target.
- **`start_line` past the end** is an empty window with `line_count`, `past_end: true`,
  `start_line` and a warning naming the last line, for canonical and prepared reads; `lines`
  carries the same count, as on every source read, so existing consumers keep their field.
  An empty document says it is empty. A multi-target read now carries a target's warnings,
  prefixed with its path.
- **Symlink escape**: the guard and its refusal stay. The workspace now raises typed
  `SymlinkEscapeError` and `NotRegularTextError` (same text as before), so the handler adds
  `data.resolved_path` and a `next` step without matching error text: outside the root, a
  read of the resolved absolute path with no workspace, which opens only that document;
  inside, a read of the target's workspace path. A dangling link keeps the plain refusal.
- **`search include`** is an alias of `paths` in `propertyAliases`, rewritten like `path`
  and `path_prefix` when it is the only scope spelling, and suggested as `paths` otherwise.
- `gitTopLevel` moved from the service to `workspace.GitTopLevel`, so the handlers can find
  the working tree of an absolute directory with the same rule the client-path resolver
  uses. `adapterWorkingDirectory` makes the adapter's directory injectable for tests.

The `workspace` package changes are additive: two error types, `ScopedPaths` and
`GitTopLevel`. Error text, confinement and write paths are unchanged.

## Safety

- Mutations are never defaulted (audit round 1, blocking 2). The routing test drives an
  unnamed `create_file` through the real socket and checks that nothing is written.
- The default fills in only what the call left unnamed; an explicit `workspace_id` or
  absolute `root` wins, which the routing test checks over the socket for both.
- The replay hash is computed after root adoption, so `"."`, the absolute root and the
  `workspace_id` are one replay key; a different edit under that key is still
  `idempotency_key_reused`.
- Listings expose no more than search does. The symlink step names a file the caller could
  already read as a document; it opens no wider workspace.

## Tests

- `internal/service/client_routing_test.go`: the adapter re-announces the same
  `adapter-` session and directory on reconnect (fake socket); over the real Unix socket,
  unnamed reads default, explicit root and `workspace_id` win, an unnamed edit is refused
  and writes nothing, and a validation refusal is not resolved; relative-root retries replay
  whichever way the workspace is named; a linked worktree (`.git` file) defaults to itself
  and its subdirectory gets no default; the default survives a service restart under a
  running adapter; an HTTP client gets no default.
- `internal/handlers/read_listing_test.go`: directory and root listings, the substring trap
  (`other/pkg/` is not `pkg/`), the 200-entry bound, no listing through an escaping
  symlink, a directory target in `targets`, the past-the-end window (single and batch) with
  `line_count` and `past_end` (single and batch), the last line still an ordinary window,
  an empty document, a reversed window still refused, the symlink-escape `next` followed to a documents read,
  an in-root link, implicit `targets` (named files only; relative and directory targets
  refused), absolute directory reads in and outside a working tree, and the empty-query
  listing with its bound and its refusal without `paths`.
- `internal/mcpapi/argument_aliases_local_test.go`: `include` read as `paths`, left alone
  beside `paths` or `path`, and the refusal pointing at `paths`.
- `internal/workspace/git_toplevel_test.go`: the linked-worktree marker, moved from the
  service package, plus a nested and a missing directory.

## Validation

Not run yet. The global Chaldea slot was held by unrelated work when this source was
prepared, so no build, `go vet` or `go test` has been dispatched. LSP diagnostics (gopls)
over every edited Go file report no errors. The checks to run, each through
`chaldea exec --host agent-a` with `HUYANG_FRICTION=0`, are `go build ./...`,
`go vet ./...`, `gofmt -l cmd internal`, and `go test ./internal/service/...
./internal/handlers/... ./internal/mcpapi/... ./internal/workspace/...` followed by the
full `go test ./...`.

## Follow-ups (not blocking)

- A validation refusal happens before resolution, so the spool still logs the relative root
  it carried (audit round 1, follow-up 5); M3 owns the spool.
- Listing entries say `file` for every listed path, symlinks included, because the listing
  reads no file metadata; a symlink target is refused or named when it is read.
- MCP `roots/list` as a second source for the default workspace, once the spool records
  which clients advertise it (proposal 6a, gap 3.6).
