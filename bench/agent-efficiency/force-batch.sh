#!/usr/bin/env bash
# Drive a queued batch through the steward with operator-forced starts.
#
# Usage: force-batch.sh [--concurrency N] [--interval SECONDS] [--reason TEXT]
#
# The steward's scheduler admits backlog work only inside its quota forecast;
# a benchmark batch submitted for a quiet slot can therefore sit in `ready`
# for days. This script is the explicit operator override the t3-backlog
# skill describes: every INTERVAL seconds it counts the active tasks and,
# while fewer than CONCURRENCY run, force-starts the next ready workflow run
# of project huyang with `t3-steward backlog start`. It never bypasses a
# closed quota pool (the coordinator refuses that) and stops when no run is
# ready. Each decision is logged to runs/force-batch.log.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
concurrency=2
interval=30
reason="operator override: benchmark batch, user authorised"
while [ $# -gt 0 ]; do
    case "$1" in
        --concurrency) concurrency="$2"; shift 2 ;;
        --interval) interval="$2"; shift 2 ;;
        --reason) reason="$2"; shift 2 ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done
log="$HERE/runs/force-batch.log"
mkdir -p "$HERE/runs"

# The active count comes from the coordinator's task tally (a run's
# progress in the list does not change while its one task runs); the ready
# runs come from the list.
snapshot() {
    t3-steward backlog status --json 2>/dev/null | python3 -c '
import json, sys
print(int(json.load(sys.stdin)["status"]["tasks"].get("active", 0)))
'
    t3-steward backlog list --project huyang --progress ready --json 2>/dev/null | python3 -c '
import json, sys
for entry in json.load(sys.stdin).get("workflows", []):
    run, workflow, counts = entry.get("run", {}), entry.get("workflow", {}), entry.get("progress", {})
    # A run stays "ready" while its one task is preparing or active; the
    # per-run task counters tell the two apart.
    if run.get("progress") == "ready" and workflow.get("taskIds") and counts.get("ready", 1) > 0 and counts.get("active", 0) == 0:
        print(run["id"] + "/" + workflow["taskIds"][0])
'
}

while true; do
    mapfile -t lines < <(snapshot)
    active="${lines[0]:-0}"
    ready=("${lines[@]:1}")
    if [ "${#ready[@]}" -eq 0 ] || [ -z "${ready[0]}" ]; then
        echo "$(date -Is) no ready run left; active=$active; done" | tee -a "$log"
        exit 0
    fi
    slots=$((concurrency - active))
    index=0
    while [ "$slots" -gt 0 ] && [ "$index" -lt "${#ready[@]}" ]; do
        target="${ready[$index]}"
        index=$((index + 1))
        result="$(t3-steward backlog start "$target" --reason "$reason" --json 2>&1)"
        command_id="$(printf '%s' "$result" | python3 -c 'import json,sys; print(json.load(sys.stdin)["command"]["id"])' 2>/dev/null || true)"
        echo "$(date -Is) start $target -> ${command_id:-$result}" >> "$log"
        slots=$((slots - 1))
    done
    echo "$(date -Is) active=$active ready=${#ready[@]} started=$index" >> "$log"
    sleep "$interval"
done
