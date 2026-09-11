// Package workspace is the transactional core of Huyang. It owns everything below the
// bridge that must be exact, durable and bounded, and it has no knowledge of MCP, Neovim
// or any language server:
//
//   - Identity: a workspace ID, kind, canonical root, provider epoch and state sequence
//     (workspace.go). The state sequence advances once per committed write set and is the
//     canonical revision ("wsrev_N") callers pin.
//   - Revisions: per-document revisions derived from content and kind (workspace.go,
//     text.go). Reads, refreshes and the native mutation path all go through the same
//     snapshot so every revision a caller sees was observed, never guessed.
//   - Handles: revision-bound range, match and symbol handles and frozen result sets, with
//     relocation and conflict classification when the document moved on (handles.go).
//   - Plans: the OPEN to COMMITTED lifecycle of a change plan, its per-kind normalization
//     and dry-run preview, and per-record persistence (plan.go, plan_recovery.go).
//   - Journals: the write-ahead commit journal, the apply loop that makes it durable,
//     startup recovery and compensation (commit.go, recovery.go, prepare.go).
//   - Sandboxes: exact copies of the canonical tree in which prepared bytes are
//     materialized and verified without touching canonical files (sandbox.go).
//   - Pipeline policy: the trusted verification stages, their resource caps and the
//     impact graph that selects affected tests (pipeline.go, impact.go).
//   - Evidence: diagnostic evidence, coverage dimensions, confidence and the bounded
//     notice stream that callers page through (evidence.go, errors.go).
//
// Three invariants hold across the package and callers must not break them:
//
//  1. Content is the revision. A document revision derives from its content hash plus
//     kind, mode and symlink target; device, inode and mtime are change detectors only.
//     Identical bytes after a touch, an atomic save or a checkout keep the revision and
//     every handle bound to it. Nothing may mint a revision from metadata alone.
//  2. Every store is bounded. Revisions per document, handles, result sets, diagnostic
//     items, notices and evidence, plan events, terminal plans and commit journals each
//     have a named, commented constant that caps them. Anything that adds persistence
//     adds its bound in the same change.
//  3. Only the transition table changes plan state. planTransitions lists every legal
//     edge; transitionPlan and its callers refuse any other move, including discard, and
//     startup reconciliation uses the same table. A precondition failure before the first
//     canonical write is CONFLICTED with a rolled-back journal; RECOVERY_REQUIRED is
//     reserved for a failure after a write.
//
// The durable write path is single: atomicWriteFile writes state files and native
// mutations, commitWrite writes commit postimages, and both share writeTempAndRename.
package workspace
