#!/usr/bin/env bash
# Queue the agent measurement: one t3-steward backlog task per scenario, language,
# tool family and repetition, run by the Muse free model through the opencode instance
# unless --model and --instance name another agent (for example --model
# claude-haiku-4-5-20251001 --instance claudeAgent).
#
# Usage: submit.sh BATCH [--scenarios "R1 E1"] [--languages "go python"]
#                        [--families "huyang builtin bash"] [--reps 3]
#                        [--model ID] [--instance ID] [--max-turns N] [--dry-run]
#
# Every started run is recorded in runs/BATCH.tsv (scenario, language, family,
# repetition, title, run id). The title is what score.py looks up in the T3 database,
# so it must stay unique per run. Commit the fixtures and prompts before submitting: the
# steward clones the repository at the branch head for each task.
#
# t3-backlog is a wrapper over "t3-steward task run": the start is synchronous, it prints
# the run id, and its exit code is the outcome. There is nothing to poll afterwards, and a
# batch submitter has no thread to wake, so every start passes --no-notify.
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
        --dry-run) dry_run=true; shift ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done

# The route is named in one place, as --model [INSTANCE/]MODEL: passing --instance
# alongside --model is refused, because a route has to be chosen once. A model id that
# already carries its instance is left alone, which is what the default MODEL above is.
case "$MODEL" in
    */*) model_option="$MODEL" ;;
    *)
        if [ -n "$INSTANCE" ]; then
            model_option="$INSTANCE/$MODEL"
        else
            model_option="$MODEL"
        fi
        ;;
esac

mkdir -p "$HERE/runs"
log="$HERE/runs/$batch.tsv"
if [ ! -f "$log" ]; then
    printf 'scenario\tlanguage\tfamily\trep\ttitle\trun\n' > "$log"
fi

started=0
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
                # --title and --name are the same field to the wrapper, and the later one
                # wins, so only the slug is passed; $title stays the human label in the log.
                run="$(printf '%s\n' "$prompt" | t3-backlog --project "$PROJECT" --name "$name" \
                    --model "$model_option" --max-turns "$max_turns" --ungated --no-notify | tail -1)"
                printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$scenario" "$language" "$family" "$rep" "$title" "$run" >> "$log"
                started=$((started + 1))
                echo "started: $title as $run"
            done
        done
    done
done

if ! $dry_run; then
    echo "started $started runs"
    echo "log: $log"
fi
