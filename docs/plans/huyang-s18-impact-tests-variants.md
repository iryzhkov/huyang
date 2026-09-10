# Huyang S18 impact graph, targeted tests and variants

Captured: 2026-09-10
Stage: S18 — Impact graph, targeted tests and variants
Starting commit: `60e38abc1a310f954e6879f7c7c299999cf2a1e8`

## Scope and result

S18 adds bounded impact analysis and advisory affected-test execution to S16's exact
sandbox verification pipeline. It does not migrate legacy wrappers, add the debugger
facade, or change apply/commit behavior.

The impact graph is keyed to the exact canonical or prepared revision under verification.
It records changed and transitively affected paths, file nodes, directed dependency edges,
the language adapter that produced each edge, confidence, adapters consulted, graph
coverage, affected nodes without colocated tests, uncertainty risks, included and omitted
project variants, and whether a broader full gate is recommended.

Built-in adapters recognize local dependencies in Go, TypeScript/JavaScript, Python, and
Lua. The graph is bounded by configurable file and edge caps. Unresolved dependencies,
reflection or dynamic loading, generated files, configuration/schema files, unreadable
files, caps, and omitted required variants remain explicit risks; none are promoted to
complete coverage.

## Project policy

Existing `[[tests]]` commands accept the additive fields:

- `name`: stable suite identity used in results and history;
- `covers`: path patterns associating the suite with affected nodes;
- `variants`: configured variants exercised by the suite;
- `required`: always select the suite for affected verification.

`[[variants]]` entries declare a stable `name`, affected-file patterns in `files`,
and whether omission is a required-variant risk. `[impact]` may tighten `max_files`
and `max_edges`; defaults are 2,000 files and 5,000 edges.

Affected selection combines configured coverage, affected/colocated tests, configured
required suites and variants, and revision-keyed prior failures. Each selected suite
carries its reasons. Selection remains advisory when the graph is incomplete.

## Verification and history

`verify_run(..., test_scope="affected")` now runs only selected configured suites against
the exact verification sandbox. Results separately expose selected and executed suites,
the complete impact report, and the verdict `affected_tests_passed`,
`affected_tests_failed`, or `affected_tests_unavailable`.

The affected path never sets `full_test_gate`. A full run alone reports
`full_tests_passed`, `full_tests_failed`, or `not_configured`. Thus targeted success
cannot imply full-suite success, even when graph coverage is complete.

Test results are atomically persisted under the service state directory by workspace ID.
Every entry names its exact revision, scope, stable test name, variants, status, duration,
and timestamp. History is bounded to the most recent 1,000 entries and prior failures feed
future advisory selection.

## Coverage and residual risks

Focused tests cover language-adapter transitive edges, dynamic/reflection risk, generated
and configuration files, graph caps, included and omitted required variants, prior-failure
selection, revision-keyed history, no-association unavailability, targeted/full verdict
separation, and the official MCP prepared-sandbox path.

Static dependency discovery deliberately does not claim whole-program completeness.
Package/module aliases, runtime loading, code generation, reflection, build tools, and
configuration can create edges beyond the built-in adapters. These facts recommend the
full gate and remain visible rather than causing targeted success to be relabeled.

No installation, deployment, installed-plugin update, live MCP restart, live
configuration/state mutation, push, or pull request is part of S18.
