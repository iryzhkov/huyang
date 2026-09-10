# Huyang S17 diagnostic evidence and provenance

## Scope and result

S17 adds the authoritative diagnostic evidence layer after S16's isolated repository
pipeline. It does not add S18 impact-graph completeness, affected-test selection, project
variants, legacy wrapper migration, or debugger facade work.

The implementation provides:

- a versioned durable per-workspace diagnostic evidence ledger;
- normalized push, pull, workspace, and configured project-check evidence;
- per-provider diagnostic identity, provenance, and conflict preservation;
- coverage and confidence states (`authoritative`, `corroborated`, `provisional`,
  `unavailable`) whose weakest required dimension controls the report;
- structured evidence IDs, cursored inbox notices, acknowledgement, and restart recovery;
- evidence-ranked culprit attribution (`exact`, `strong`, `likely`, `ambiguous`,
  `unattributed`) without timing-only causality;
- modern `diagnostics` and `evidence_get` execution paths and compact same-workspace
  `diagnostic_updates`;
- diagnostic barriers in prepared and explicit verification flows, with configured project
  checks retained as a distinct corroborating fallback;
- `PROVISIONAL` modern edits and prepared plans whenever semantic evidence is incomplete.

## Barrier and provenance behavior

The Neovim provider exposes an internal structured `huyang_diagnostic_evidence` operation.
It waits boundedly for attachment, preserves each selected LSP client's identity/version,
uses the version stamp recovered from `publishDiagnostics`, retains pull payloads and result
IDs, and exposes open work-done progress as a veto. The Go evidence core normalizes whitespace,
deduplicates the same provider finding across push and pull, retains different or conflicting
provider findings, and refuses evidence marked as coming from an unselected provider.

A complete correctly versioned push, or a complete pull/workspace result with a result ID, is
authoritative. A successful configured project check is corroborating fallback evidence.
Missing/wrong versions, incomplete results, open progress, and bounded timeouts remain
provisional. Incomplete evidence cannot resolve an active diagnostic and cannot produce a
passed diagnostic verification stage.

Each item retains transaction, workspace state sequence, document revision/version, producer,
producer version, first/last seen times, evidence kinds and IDs. Culprit rank uses exact
sandbox provenance, unique postimage-version match, changed-symbol or bounded impact-path
facts; arrival time is not accepted as causal proof.

## Durable inbox and modern results

Diagnostic state is stored under the service state directory by workspace ID using an atomic
replacement. New and resolved items create compact notices. Later same-workspace results carry
unacknowledged notices; `diagnostics(since=cursor)` retrieves structured changes and
acknowledges the supplied cursor. `evidence_get` returns the detailed stored payload. Service
restart reloads items, evidence, notices, coverage and acknowledgement.

Modern direct text edits no longer return outcome `ok` while simultaneously admitting that
semantic diagnostics are unavailable. They return `provisional`, an evidence ID, missing
coverage, and the exact applied change. Prepared plans likewise enter `PROVISIONAL` unless
authoritative/corroborated diagnostic coverage exists; configured static checks may provide
the explicit corroborating fallback.

## Verification coverage

Focused coverage includes:

- push/pull deduplication and provider-conflict retention;
- wrong-version push, incomplete pull/workspace result, open progress, timeout and project
  check barrier permutations;
- durable evidence, inbox acknowledgement, restart recovery, resolution, and all culprit
  ranks;
- official MCP client retrieval, evidence IDs, and same-workspace compact notices;
- prepared-plan provisional behavior and configured-check corroboration;
- real gopls, typescript-language-server, pyright, lua-language-server and
  bash-language-server processes through the embedded provider. Each real case proves that
  provisional or unavailable evidence is never promoted to a clean verification result.

The exact commands and final results are recorded in `huyang-handoff.md`.

## Decisions and residual risks

- The provider wait for initial LSP attachment is bounded to 1.5 seconds. Slow or silent
  attachment is reported provisional and remains retrievable as evidence.
- Pull evidence supersedes an earlier incomplete push from the same provider; coverage still
  uses the weakest selected provider. Conflicting provider findings remain separate.
- A configured repository check can corroborate when LSP evidence is unavailable, but it is
  recorded as `project_check`, never relabeled as LSP evidence.
- The inbox currently uses response-carried notices as the reliable path. Transport-native
  subscription delivery can reuse the ledger later without changing identity or cursors.
- S18 must add language-specific dependency completeness, bounded transitive impact and
  variant-aware confidence. S17 does not claim those dimensions are complete.
