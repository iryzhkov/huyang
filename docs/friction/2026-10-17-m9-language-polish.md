# M9 language polish: source preparation

This change is prepared on local M7 `8ca0951c1f55dbd447b1dc3a08bcd6c004d48909`,
with docs-only `78334a2e1208fc27f9bf189b09c6b5eaba1ed3be` cherry-picked as
`ff53762`. This is a source stack, not acceptance against actual main M2/M3.
No tests, builds, language/environment commands, Chaldea jobs or live probes ran for
this preparation. The installed service and host configurations were not changed.

## Python source changes

`internal/workspace/python_interpreter.go` reuses `pythonEnvironment` from the
pipeline detector. The Lua-only detector was removed. Provider bootstrap configures
Python before a tool can load its first source buffer, closing the background-warmup
race. A sandbox provider receives its canonical Python environment root separately
from its sandbox source root; generation restarts keep that configuration.
Precedence is explicit LSP/project configuration, local `.venv` / `venv` / `env`,
executable inherited `VIRTUAL_ENV`, then the lockfile's manager (uv before Poetry).
Existing local venvs do not require either manager to be installed.

Discovery issues bounded metadata queries only, never a package runner or a project
script. Missing tools, failed queries and invalid output carry an unresolved reason.
The attached client's effective interpreter determines whether imports can be grouped.
Repeated Pyright/basedpyright `reportMissingImports` findings become one stable
warning per producer/document only when the interpreter is unknown. All other findings
survive. Immediate and late evidence retain their existing guards and remain provisional
after grouping. The canonical diagnostic schema and ledger version are unchanged.
See [the diagnostic contract](../diagnostics-contract.md#python-environments).

## Changed files

- Environment discovery and ledger coverage:
  `internal/workspace/python_interpreter.go`, `python_interpreter_test.go`,
  `python_evidence_test.go`, `evidence.go`.
- Provider bootstrap and canonical environment-root propagation:
  `internal/provider/embed/embed.go`, `python_interpreter_test.go`;
  `internal/providerpool/provider_factory.go`, `provider_limits.go`,
  `provider_pool.go`, `stagers.go`, `provider_call_test.go`, `python_environment_test.go`.
- Lua configuration and immediate/late grouping:
  `lua/huyang/python_environment.lua`, `install.lua`, `edit.lua`;
  `tests/unit_check.lua`, `tests/unit_edit.lua`.
- Public reason code and summary:
  `internal/mcpapi/diagnostic_codes.go`, `diagnostic_codes_test.go`;
  `internal/handlers/edit.go`, `late_evidence_test.go`.
- Documentation: `README.md`, `docs/agent-guide.md`,
  `docs/diagnostics-contract.md`, and this report.

## Go investigation: evidence

The cached inputs below are outside the repository, under
`~/.cache/huyang-friction/`; no private correlation keys or secrets were inspected.

- `codex-replies.json` → `edit_verdicts_by_ext.go` contains
  `provisional:lsp_not_configured = 152`.
- `claude-replies.json` → the same bucket contains 41. These sum to the proposal's
  193 Go edits. `mine_replies.py` derives language from edit-operation path extensions
  and classifies the first provisional code (or a matching summary). Mixed edits may
  therefore be counted in several language buckets.
- `codex-files.txt` and `claude-files.txt` point at local Codex and Claude transcript
  stores. The proposal identifies this transcript corpus as gaming-pc. The cached
  aggregate dictionaries do not retain per-case host, service PATH, Neovim init,
  workspace or provider startup metadata. Thus gaming-pc is corpus provenance,
  not a proven service-host attribution for each of the 193 calls.
- `fleet-14d.txt` is a separate Loki report for 2026-10-03 through 2026-10-17:
  homelab, omarchy-macbook-air-igor, omarchy-normandy and omarchy-pc. Its all-language
  edit total is 416 `lsp_not_configured` occurrences. It does not cross-tabulate
  host × language × reason, so those four names cannot be assigned to the 193.
- `probe-setup/report1/summary.txt` identifies pasta-agent-a and explicitly labels its
  2026-10-17 result as prerequisite checking of `6188357`, not M1 acceptance.
  Its Go prerequisite success does not explain historical failures or validate M9.

One incidental Huyang edit receipt during this source preparation also reported
`lsp_not_configured` for a Go source test in this isolated worktree; other edit
receipts carried Go findings. This is an observation from required source editing,
not a live acceptance probe. No status/setup/restart call was made to investigate
it. The service host, provider lifecycle and installed-runtime cause remain unverified.

## Go investigation: exact source behavior and confidence

High confidence from source: `lua/huyang/edit.lua` → `diagnostic_evidence`
returns `lsp_not_configured` when there is no attached client and
`core.enabled_lsp_configs_for(filetype)` returns no enabled configurations.
That helper in `lua/huyang/core.lua` scans `vim.lsp._enabled_configs`, resolves
each configuration and matches its `filetypes`. Navigation's `get_client` reports
the same distinction. An enabled config whose command is absent normally produces
`lsp_not_startable`, not `lsp_not_configured`. The code alone therefore does not
prove that those 193 cases lacked gopls on PATH.

Existing setup in `lua/huyang/install.lua` → `workspace_support`:
`ensure_mason_bin_on_path` restores Mason's launcher directory;
`enable_installed_servers` restores installed packages through mason-lspconfig;
the preferred system server for `go` and `gomod` is `gopls`.
If no configuration is enabled, `system_server` scans `lsp/*.lua` on runtimepath,
requires a matching filetype and executable command, and `enable_system_server`
enables it. A binary without its runtimepath configuration is insufficient.
Huyang no longer installs servers; setup only restarts the owned provider.

Medium confidence: the historical bucket describes a configuration/bootstrap/deployment
environment gap (disabled/missing config, absent runtimepath/Mason integration, or no
available system server). Low confidence: which concrete service hosts, PATH values,
init files or provider races caused it. The cached aggregates cannot distinguish them.
No concrete Go source defect within M9 is established, so no speculative Go behavior
change or host installation/configuration change is included.

## Exact follow-up for the root/operator

Runtime scheduling belongs to the root, on agent-a. After stack integration, run
source checks with `HUYANG_FRICTION=0` for every Go invocation and set `TMPDIR` to
an on-disk cache directory. Suggested commands (not run here):

```sh
export TMPDIR="$HOME/.cache/huyang-friction/m9/runtime"
mkdir -p "$TMPDIR"
HUYANG_FRICTION=0 go test ./internal/workspace ./internal/providerpool ./internal/provider/embed ./internal/handlers ./internal/mcpapi
HUYANG_FRICTION=0 go test ./...
HUYANG_FRICTION=0 go vet ./...
nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_check.lua
nvim --clean --headless -u tests/minimal_init.lua -l tests/unit_edit.lua
```

The focused source tests use stubs for manager queries and never run uv/Poetry.
After those pass, the root should schedule a private-binary service probe on agent-a
with its own socket/state/cache, using the fully integrated M1/M8 stack. Exercise:
an existing `.venv` import; uv and Poetry existing environments; explicit
`pythonPath`; missing manager/query failure; multiple missing imports plus a real
type error; immediate and late push/pull delivery; repeated note identities; sandbox
environment selection; current-version, quiet and progress vetoes; JSON and plain
rendering with all guard tokens present. Missing servers are a prerequisite gap.

For the historical Go question, first obtain a sanitized metadata-only report of
host × language × diagnostic reason × installed revision/Neovim init mode. Do not
export correlation keys, source bodies or full environments. Existing spool versions
may not have that language join; explicitly report that gap instead of guessing.
Then reproduce on each identified environment only with operator authorization:
use a private binary/socket/state and the same service PATH/init/runtimepath, inspect
`language_server_status` for Go and a guarded edit in a tiny Go module. Capture
whether gopls config exists, is enabled, is executable, is attached, and which
availability code results before and after the private support warmup. Compare
`--clean` and the configured init without installing, restarting or altering the
installed service. No cross-host work is authorized by this source-only task.

Remaining acceptance: integration with actual main M2/M3 and M8; independent review;
focused/full tests and CI; manager-version compatibility; private Python/Go probes;
historical host attribution; any rollout approval. No runtime acceptance is claimed.
