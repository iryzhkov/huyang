#!/usr/bin/env bash
# Queue the agent measurement: one t3-steward backlog task per scenario, language,
# tool family and repetition, run by the Muse free model through the opencode instance
# unless --model and --instance name another agent (for example --model
# claude-haiku-4-5-20251001 --instance claudeAgent).
#
# Usage: submit.sh BATCH [--scenarios "R1 E1"] [--languages "go python"]
#                        [--families "huyang builtin bash"] [--reps 3]
#                        [--model ID] [--instance ID] [--max-turns N] [--host NAME]
#                        [--dry-run]
#
# --host names the machine whose T3 server runs the tasks (an SSH alias the
# steward knows). A benchmark measures wall time as well as tokens, so run a
# batch on an idle machine and keep the host constant for every family of it.
#
# Every queued task is recorded in runs/BATCH.tsv (scenario, language, family,
# repetition, title, task file). The title is what score.py looks up in the T3 database,
# so it must stay unique per run. Commit the fixtures and prompts before submitting: the
# steward clones the repository at the branch head for each task.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
PROJECT="huyang development"
MODEL="opencode/muse-spark-1.3-contributor-free"
INSTANCE="opencode"

batch="${1:-}"
[ -n "$batch" ] || { echo "usage: submit.sh BATCH [options]" >&2; exit 2; }
shift
scenarios="R1 R2 R3 R4 E1 E2 E3 E4 E5 E6 E7 E8 V1"
languages="go"
families="huyang builtin bash"
reps=3
max_turns=4
host=""
dry_run=false
while [ $# -gt 0 ]; do
    case "$1" in
        --scenarios) scenarios="$2"; shift 2 ;;
        --languages) languages="$2"; shift 2 ;;
        --families) families="$2"; shift 2 ;;
        --reps) reps="$2"; shift 2 ;;
        --model) MODEL="$2"; shift 2 ;;
        --instance) INSTANCE="$2"; shift 2 ;;
        --max-turns) max_turns="$2"; shift 2 ;;
        --host) host="$2"; shift 2 ;;
        --dry-run) dry_run=true; shift ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done

count_runs() {
    t3-steward backlog list --project huyang --json 2>/dev/null \
        | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("workflows", [])))' || echo 0
}

mkdir -p "$HERE/runs"
log="$HERE/runs/$batch.tsv"
if [ ! -f "$log" ]; then
    printf 'scenario\tlanguage\tfamily\trep\ttitle\ttask_file\n' > "$log"
fi

before=$(count_runs)
queued=0
for scenario in $scenarios; do
    for language in $languages; do
        for family in $families; do
            for rep in $(seq 1 "$reps"); do
                title="bench $batch $scenario $language $family r$rep"
                name="bench-$batch-$scenario-$language-$family-r$rep"
                prompt="$(python3 "$HERE/scenarios/prompt.py" --scenario "$scenario" --language "$language" --family "$family")"
                if $dry_run; then
                    echo "would queue: $title"
                    continue
                fi
                host_option=()
                [ -n "$host" ] && host_option=(--host "$host")
                task_file="$(printf '%s\n' "$prompt" | t3-backlog --project "$PROJECT" --title "$title" --name "$name" \
                    --instance "$INSTANCE" --model "$MODEL" --max-turns "$max_turns" --importance 2 --difficulty 1 --ungated \
                    "${host_option[@]}" | tail -1)"
                printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$scenario" "$language" "$family" "$rep" "$title" "$task_file" >> "$log"
                queued=$((queued + 1))
                echo "queued: $title"
            done
        done
    done
done

if ! $dry_run; then
    sleep 20
    after=$(count_runs)
    echo "queued $queued tasks; workflow runs for project huyang went from $before to $after"
    echo "log: $log"
    if [ "$((after - before))" -ne "$queued" ]; then
        echo "warning: expected $queued new workflow runs, observed $((after - before)); inspect t3-steward backlog status before resubmitting" >&2
    fi
fi
