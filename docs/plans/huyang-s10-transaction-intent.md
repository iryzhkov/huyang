# Huyang S10 transaction intent, validation and preview

S10 adds durable plan intent and deterministic predicted previews without staging provider
buffers or changing canonical workspace bytes.

## Delivered scope

- The workspace core owns opaque plan IDs, monotonically increasing plan revisions, stable
  operation IDs, OPEN/PREVIEWED/DISCARDED state, and durable create/edit/inspect/preview/discard event
  records. Plan files are mode 0600 and use temp-write, file sync, rename, and directory sync.
- The frozen twelve-kind shared operation union is normalized into one record shape.
  Opaque handles and human symbol locators are resolved to durable range locators when intent
  is recorded. `delete_symbol` is a first-class operation and normalizes its replacement to
  exact deletion.
- Explicit `depends_on` edges and derived filesystem/range dependencies are topologically
  ordered. Independent operations retain user order; edits to one base document run from
  higher to lower byte offsets so their revision-bound coordinates remain stable.
- Preview validates the complete operation vector against current workspace, document, and
  handle preconditions. It accumulates every conflict, publishes no partial diff on failure,
  and never applies the simulation to provider buffers or disk.
- Successful prediction simulates supported text/range/symbol and file create/move/delete
  operations in memory, emits deterministic per-file exact-byte diffs, and hashes the result
  into a stable preview revision.
- The modern `change_plan` schema now has closed action branches for create, edit, preview,
  inspect, prepare, apply, and discard. S10 implements the first five intent actions;
  prepare/apply remain explicit unavailable outcomes for their S11/S12 stages.
- Existing durable stateful-request receipts make retries replay the original result after
  concurrent calls and service restarts. Plan storage is restored with the service-owned
  workspace identity.

## Verification coverage

`internal/workspace/plan_test.go` covers deterministic restart-safe preview, same-file safe
ordering, delete-symbol normalization, canonical non-mutation, all-conflict stale multi-file
validation, durable lifecycle events, plan revision edits/discard, duplicate operation IDs,
and dependency-cycle refusal.

`internal/bridge/modern_mcp_test.go` covers official-SDK change-plan calls, closed schema
validation, durable idempotent replay, service restart, workspace identity reuse, inspection,
repreview determinism, and canonical byte preservation.

The stage gates passed:

- `go test -race ./internal/workspace ./internal/bridge -count=1`
- `make smoke`
- `go test ./...`
- `go vet ./...`
- `git diff --check`

## Boundaries and residual risks

S10 records and predicts intent only. It does not acquire a transaction lease, write unsaved
provider buffers, suppress diagnostics, run format/check/test stages, create a prepared
revision, apply canonical bytes, or introduce the multi-file recovery journal. Provider-native
rename, move-symbol, frozen-match, and code-action operations remain representable intent but
preview reports that their resolver belongs to a later named stage.

Plan persistence is local service state rather than a portability format. The structure is
versioned and fails closed on an unknown version; future schema migrations must preserve or
explicitly supersede recorded intent.
