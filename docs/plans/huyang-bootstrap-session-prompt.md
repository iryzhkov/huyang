# Huyang serial bootstrap successor prompt

You are running unattended from a task backlog while the user is away. Work autonomously:
do not ask questions and do not wait for confirmation. If a decision genuinely needs the
user, do everything independent of it, leave an exact handoff, queue no successor, and end
with `BACKLOG STATUS: needs-input`.

Continue the serial Huyang implementation chain in the clean standalone Git checkout
`/home/igor/Work/huyang` only. Never use or modify the deployed/local Neovim plugin
checkout. The repository is `https://github.com/iryzhkov/agent99`, the implementation
branch is `feature/huyang`, and its reviewed base is
`1c5302efe9ca51e1701e73df72d903f225fefe04`.

Read these tracked authorities completely before changing anything:

- `docs/plans/huyang-transactional-semantic-workspace-implementation-plan.md`
- `docs/plans/huyang-tools-v1alpha1.md`
- `docs/plans/huyang-handoff.md`
- every predecessor artifact named by the handoff

Fetch origin. Reconcile the actual branch, history, worktree, checklist, handoff, code, and
test state; do not trust a prior chat summary. Confirm the pinned base is in branch history.
Select exactly the first incomplete named stage in the committed checklist. Complete only
that stage. If a predecessor is incomplete, repair that predecessor only or stop
`needs-input`; never build on a false checkpoint and never absorb successor work.

Before editing, update the handoff with the selected stage, starting commit, and exact exit
gates. During the stage, preserve unexplained changes and follow repository `AGENTS.md`
instructions. Add the stage's required tests and artifacts. Run its specific gates plus
`make smoke`, `go test ./...`, `go vet ./...`, and `git diff --check` unless the
authoritative plan explicitly makes one inapplicable. Record exact commands and results,
decisions, risks, and the exact next stage in the handoff. Mark only the completed checklist
item. Commit code, tests, documents, plan progress, and handoff together on
`feature/huyang`, then confirm the worktree is clean.

Never install or deploy Huyang, update the pinned agent99 plugin, restart a live MCP service,
mutate live configuration or state, push, or create a pull request.

If the stage is blocked, any required verification fails, the commit fails, or the worktree
is not clean, queue no successor. Leave the precise recovery state and exact user question in
`docs/plans/huyang-handoff.md`, save or commit safe completed work when appropriate, and end
with exactly:

`BACKLOG STATUS: needs-input`

If and only if the selected stage is fully complete, committed, and clean, queue exactly one
successor by running this command from the repository root:

```sh
t3-backlog \
  --project "huyang development" \
  --title "Huyang serial implementation successor" \
  --model "gpt-5.6-sol" \
  --instance "codex" \
  --importance 5 \
  --difficulty 5 \
  --max-turns 6 \
  --ungated \
  < docs/plans/huyang-bootstrap-session-prompt.md
```

Record the queued backlog path in the final response, do not begin the next stage in the
current thread, and end with exactly:

`BACKLOG STATUS: done`
