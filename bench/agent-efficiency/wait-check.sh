#!/usr/bin/env bash
# Exit 0 once every task of a submitted batch has finished, for a t3-steward wait.
#
# Usage: wait-check.sh BATCH [--db ~/.t3/userdata/state.sqlite]
#
# The steward titles the threads it starts itself, so a batch task is recognised
# the way score.py recognises it: by its first user message, which is the
# rendered benchmark prompt, created no earlier than the batch itself (the
# earliest started_at in runs/BATCH.tsv). A thread counts as finished when its
# last turn is no longer running or pending. The steward's protocol: exit 0 means
# done, exit 1 means not yet, exit 2 means give up. The check gives up when the
# batch log does not exist or does not carry a start time.
#
# The start time has no default on purpose. A missing bound would count every
# benchmark thread ever recorded, the count would reach the batch size within
# seconds, and the wait would report a batch finished that had barely started.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
batch="${1:-}"
db="${3:-$HOME/.t3/userdata/state.sqlite}"
log="$HERE/runs/$batch.tsv"
[ -n "$batch" ] && [ -f "$log" ] || { echo "no batch log for '$batch'"; exit 2; }

python3 - "$log" "$db" <<'EOF'
import csv, sqlite3, sys
from datetime import datetime, timezone
log, db = sys.argv[1], sys.argv[2]
with open(log, encoding="utf-8") as handle:
    rows = list(csv.DictReader(handle, delimiter="\t"))
stamps = []
for index, row in enumerate(rows, start=1):
    value = (row.get("started_at") or "").strip()
    if not value:
        print(f"row {index} of {log} has no started_at, so the batch has no start time; "
              "it was written by a submit.sh from before that column existed", file=sys.stderr)
        sys.exit(2)
    try:
        moment = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        print(f"row {index} of {log} has an unreadable started_at: {value!r}", file=sys.stderr)
        sys.exit(2)
    if moment.tzinfo is None:
        moment = moment.replace(tzinfo=timezone.utc)
    stamps.append(moment.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S"))
if not stamps:
    print(f"{log} has no rows, so there is nothing to wait for", file=sys.stderr)
    sys.exit(2)
since = min(stamps)
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
