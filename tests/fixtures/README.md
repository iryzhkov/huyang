# Live fixture repositories

One small, real project per language the live harness exercises. They exist so the
service-boundary tests run against something a language server will actually index, in
every language Huyang claims to serve, rather than against a single Go file.

Each fixture holds the same shape, so one scenario table covers all five:

- a declaration with a documentation comment (`Total`), referenced from a second file, so
  outline, references, rename and safe delete all have something to find;
- a second declaration nobody calls (`Unused`), so a reference-checked deletion has a
  positive case;
- a call site inside a third declaration (`Report`), so an inline has a target.

They are deliberately tiny. A live test that takes a minute to index is a live test nobody
runs, and every claim these fixtures support is about the shape of an answer rather than
the size of a repository.
