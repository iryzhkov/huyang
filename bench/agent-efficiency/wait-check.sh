#!/usr/bin/env bash
# Exit 0 once every task of a submitted batch has finished, for a t3-steward wait.
#
# Usage: wait-check.sh BATCH [--db ~/.t3/userdata/state.sqlite]
#
# A task counts as finished when a thread with its title exists and its last turn is
# no longer running. The steward's protocol: exit 0 means done, exit 1 means not yet,
# exit 2 means give up. The check gives up when the batch log does not exist.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
batch="${1:-}"
db="${3:-$HOME/.t3/userdata/state.sqlite}"
log="$HERE/runs/$batch.tsv"
[ -n "$batch" ] && [ -f "$log" ] || { echo "no batch log for '$batch'"; exit 2; }

python3 - "$log" "$db" <<'EOF'
import csv, sqlite3, sys
log, db = sys.argv[1], sys.argv[2]
with open(log, encoding="utf-8") as handle:
    titles = [row["title"] for row in csv.DictReader(handle, delimiter="\t")]
connection = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
finished = 0
for title in titles:
    row = connection.execute(
        "select t.thread_id, (select state from projection_turns u where u.thread_id = t.thread_id "
        "order by requested_at desc limit 1) from projection_threads t where t.title = ? and t.deleted_at is null "
        "order by t.created_at desc limit 1", (title,)).fetchone()
    if row and row[1] not in (None, "running", "pending", "queued"):
        finished += 1
print(f"{finished} of {len(titles)} tasks finished")
sys.exit(0 if titles and finished == len(titles) else 1)
EOF
