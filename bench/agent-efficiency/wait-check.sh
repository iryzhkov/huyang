#!/usr/bin/env bash
# Exit 0 once every task of a submitted batch has finished, for a t3-steward wait.
#
# Usage: wait-check.sh BATCH [--db ~/.t3/userdata/state.sqlite]
#
# The steward titles the threads it starts itself, so a batch task is recognised
# the way score.py recognises it: by its first user message, which is the
# rendered benchmark prompt, created after the batch was submitted (the earliest
# task-file timestamp in runs/BATCH.tsv). A thread counts as finished when its
# last turn is no longer running or pending. The steward's protocol: exit 0 means
# done, exit 1 means not yet, exit 2 means give up. The check gives up when the
# batch log does not exist.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
batch="${1:-}"
db="${3:-$HOME/.t3/userdata/state.sqlite}"
log="$HERE/runs/$batch.tsv"
[ -n "$batch" ] && [ -f "$log" ] || { echo "no batch log for '$batch'"; exit 2; }

python3 - "$log" "$db" <<'EOF'
import csv, re, sqlite3, sys
from datetime import datetime, timezone
log, db = sys.argv[1], sys.argv[2]
with open(log, encoding="utf-8") as handle:
    rows = list(csv.DictReader(handle, delimiter="\t"))
stamps = []
for row in rows:
    match = re.search(r"-(\d{8}-\d{6})\.md$", row.get("task_file", ""))
    if match:
        local = datetime.strptime(match.group(1), "%Y%m%d-%H%M%S").astimezone()
        stamps.append(local.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S"))
since = min(stamps) if stamps else "1970-01-01T00:00:00"
prefix = "You are running one scenario of the Huyang agent-efficiency benchmark"
connection = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
threads = connection.execute(
    "select distinct m.thread_id from projection_thread_messages m join projection_threads t "
    "on t.thread_id = m.thread_id where m.role = 'user' and m.text like ? and m.created_at >= ? "
    "and t.deleted_at is null", (prefix + "%", since)).fetchall()
finished = 0
for (thread_id,) in threads:
    state = connection.execute(
        "select state from projection_turns where thread_id = ? order by requested_at desc limit 1",
        (thread_id,)).fetchone()
    if state and state[0] not in (None, "running", "pending", "queued"):
        finished += 1
print(f"{finished} of {len(rows)} tasks finished ({len(threads)} threads seen since {since})")
sys.exit(0 if rows and finished >= len(rows) else 1)
EOF
