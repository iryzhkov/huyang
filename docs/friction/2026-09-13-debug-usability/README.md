# Debugging and usability workshop — 2026-09-13

In progress; baseline accepted with correct worker placement. Muse active/preparing and Haiku ready at the 14:59 UTC snapshot. Two initial hostname-mismatched submissions were confirmed cancelled before replacements. No participant outcome yet. Workshop files remain local/uncommitted while baseline work is pending. Coordinator T3 thread: 92bdd669-fcfc-4a2c-90b1-3addf4dbe039.
Original session prompt: /home/igor/Work/plans/huyang-debug-usability-session-prompt.md.
Baseline source and installed daemon: 2c22cbf24c4242eca17533866e86a9da1827e8c6 (clean; running binary inspected with go version -m /proc/1059200/exe).
Branch feature/huyang was clean at entry. No nonterminal Huyang backlog runs (237 historical runs).

Bounded waves: shared baseline, assigned cards, targeted fresh-context post-fix verification. Maximum three concurrent assignments; normal admission only. No shared restart before baseline finishes. Raw reports remain unedited under raw/; coordinator verdicts are in findings.md. Reports are collected through steward branch/artifact handoff.

Fixture delivery is a fresh synthetic Go program, independent of regression fixtures. Each participant copies it into its own steward-managed clone through Huyang. Ground truth stays in coordinator.md; do not supply it to participants.

Models discovered from current T3 provider caches and installed harnesses: OpenCode 1.18.30 / opencode/muse-spark-1.3-contributor-free (medium variant/build defaults); Claude Code 2.1.268 / claude-haiku-4-5 (default thinking setting); Codex CLI 0.154.0 / gpt-5.6-luna (medium/standard defaults).
Luna is advertised by the authenticated Codex instance and is cost-oriented according to [official model documentation](https://developers.openai.com/api/docs/models/gpt-5.6-luna) (API rates $0.20 input/$1.20 output per million tokens; these are not measured subscription costs). Normandy worker eligibility allows only gpt-5.6-sol for Codex, while Huyang project is pinned to Normandy. Luna coverage is blocked; no expensive substitution or worker-policy alteration.

The legacy backlog helper accepts at most 20 turns (40 rejected). Use 20 and a 15-minute task limit, with incremental checkpoints. --host normandy initially failed SSH validation with exit 64. --host omarchy-normandy passed local validation but produced ineligible routes because the configured worker ID is normandy. Two never-dispatched assignments were cancelled before replacement. Recovery uses a temporary invocation-only copy of the steward config at /tmp/huyang-workshop-20260913-intake, changing only backlog.host_name to normandy; XDG_CONFIG_HOME points the helper there and T3_BACKLOG_DIR explicitly names the original intake. This validates --host normandy without changing daemon/worker/admission configuration. Neither --ungated, near deadline, nor backlog start is authorized.
