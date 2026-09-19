# Stage S21: compressed views that keep the logic

The 2026-09-13 efficiency session left one number standing: over the whole Haiku batch,
`read` is 52 percent of every response token the Huyang family spends, and `read` is the
most called tool on the fleet (3,936 calls in four days). Everything else has been trimmed
to its envelope; what is left is the file itself.

This stage is about giving an agent a cheaper way to look at source without lying to it.
Three rungs of a ladder, two of which exist:

1. `read view=outline` — the declarations of a file and nothing else. Exists.
2. **`read view=skeleton`** — the control flow of a declaration with its straight-line runs
   collapsed. New, and the subject of this stage.
3. `read` — the exact bytes. Exists, and stays the only thing an edit is composed from.

A fourth control cuts across all three: **eliding comment bodies and long literals**.

## What a skeleton is

One line per structural element, in file order, each prefixed with its real line number so
any part of it can be windowed with the `read` that already exists:

```
 73 func (l *Ledger) Balance(account string) int64 {
 74   var total int64
 75   for _, e := range l.entries {
 76     if e.Account != account { continue }
 79     total += e.Amount
 80   }
 81   if cached, ok := l.balances[account]; ok && cached != total {
 82     panic(...)
 83   }
 84   return total
 85 }
```

The rules that make it smaller than the source, in the order they pay:

- **Straight-line runs collapse.** A run of statements with no call and no control flow
  becomes one line naming what it binds: `88-96 ⟨sets summary.Entries, summary.Income,
  summary.Expense, 6 more⟩`. The agent keeps the names; it loses the arithmetic.
- **Calls stay.** A call to another declaration is the thing the reader is tracing, so it
  is kept with its callee and its arguments elided: `total := l.Balance(account)`.
- **Conditions keep their subject, not their length.** A condition over 80 characters is
  cut at its first operator with an ellipsis; the line number reaches the rest.
- **Go error handling collapses.** `if err != nil { return nil, err }` is one token of
  information and four lines of text; it becomes `⟨err⟩`. Any other branch is kept.
- **Comment bodies elide to their first line**, and string and byte literals over a bound
  become `"…[string, 4.1 KiB]"`. These two are also available on a plain `read` through
  `elide: ["comments", "long_literals"]`, because they pay there too.

## What it must not do

- **It is never the basis of an edit.** `replace_literal` matches exact bytes; a skeleton
  is not bytes. When an `old` that fails to match contains an elision marker, the refusal
  says so instead of only "not found".
- **It is never silent.** Every omission is one line that says what was left out, how many
  lines it covered and where they are. A view that quietly drops code is worse than no view.
- **It is opt-in.** Nothing changes for a caller that does not ask for it.

## Parser coverage

The native sectioner covers Go and Python, which is where this can ship without a language
server; every other language needs the provider, which the fleet spool shows as unavailable
on most hosts. The first version covers Go and Python and says `skeleton_unavailable` with
the reason elsewhere, the way the symbol read now does.

## How it gets decided

Not by argument. The benchmark already has the three shapes that matter and the harness to
run them on light models:

- **R1** (list every exported declaration with a one-line description) is the case a
  skeleton must not break: the descriptions come from doc comments, so it measures whether
  eliding comment bodies costs the answer.
- **R2** (read one function's body and explain what it aggregates) measures the skeleton
  against the exact read on the same question.
- **R4** (three files, describe the data flow) is where a skeleton should win biggest.

A scenario passes only if the answer is still correct; a token win that costs correctness is
the outcome this benchmark exists to catch. Compare `read`, `read view=skeleton` and
`read elide=[comments,long_literals]` as three families on the same scenarios.

## Relation to the execution tools

`execution_graph` and `path_explain` already answer a related question from static analysis.
The fleet spool has 14 calls between them and 10 that did not answer `ok`, but they are not
one failure: five are `graph_source_changed` (the source digest moved while the analysis was
acquiring it, which any unrelated save in the tree causes), three are cancellations or
deadlines on a large workspace, and five are `partial` replies that are the tool being
honest that static paths are candidates. The first two are defects; the third is the
contract. Before this stage adds a third way to look at a function, those defects are worth
fixing, because a skeleton view and a static path answer overlapping questions.

## Open questions

- Does a collapsed straight-line run name what it binds, or only count its statements? The
  names cost about a third of the run and are what makes it searchable.
- Does the skeleton keep the `if` bodies that are a single statement (`continue`, `break`,
  `return x`) inline, as the example above does, or one line each?
- Is `view=skeleton` a view, or a `detail` level on the existing outline view? The outline
  is a list of declarations; a skeleton is one declaration's inside. They may be one axis.
