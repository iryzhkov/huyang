#!/usr/bin/env bash
# Huyang budget gate: turns service and repository growth into numbers.
#
# Usage: make budget   (or: bash tests/budget.sh)
#
# What it does: builds bin/huyang, starts `huyang serve` on disposable XDG
# directories, drives a fixed 20-round scenario through `huyang mcp --profile
# full` (workspace_open, workspace_inspect, search, read, edit_apply,
# diagnostics on a temporary copy of tests/testproj), prints one table, writes
# docs/plans/budget/<date>-<sha>.json and compares against
# docs/plans/budget/baseline.json. The first run writes the baseline.
#
# Numbers and tolerances (exceeding a tolerance fails the gate):
#   cold_rss_kb        VmRSS of `huyang serve` 2 s after the socket appears  +25%
#   post_rss_kb        VmRSS of `huyang serve` after the 20 rounds           +25%
#   state_bytes        size of the state directory after the rounds         +50%
#   long_functions     Go functions over BUDGET_FUNC_LINES (80) lines        +0
#   largest_go_file    line count of the largest Go file                     +10%
# Informational only:
#   provider_rss_kb    summed VmRSS of serve's children (embedded Neovim)
#   registry_bytes, receipts_bytes, plans_bytes, journals_bytes, diag_bytes
#                      sizes of registry.json, receipts/, plans/,
#                      commit-journals/, diagnostics/ under the state dir
#   go_lines, go_test_lines, lua_lines   tracked source line counts
#   tools_<profile>_bytes                byte size of the tools/list reply
# Provider-dependent calls run against the real Neovim when `nvim` is on PATH;
# otherwise their outcome is recorded but not required to be ok.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"
: "${BUDGET_FUNC_LINES:=80}"
: "${BUDGET_ROUNDS:=20}"
[ -z "${BUDGET_TRACE:-}" ] || set -x
command -v jq >/dev/null || { echo "budget: jq is required"; exit 1; }

make build >/dev/null
TMP="$(mktemp -d)"
export XDG_STATE_HOME="$TMP/state" XDG_RUNTIME_DIR="$TMP/run" XDG_CONFIG_HOME="$TMP/config"
mkdir -p "$XDG_RUNTIME_DIR" "$XDG_STATE_HOME" "$XDG_CONFIG_HOME"
SOCK="$XDG_RUNTIME_DIR/huyang/control.sock"
STATE="$XDG_STATE_HOME/huyang"
cp -r tests/testproj "$TMP/proj"
if command -v nvim >/dev/null; then PROVIDER=nvim; else PROVIDER=absent; echo "budget: nvim not on PATH; provider-dependent outcomes are not required"; fi

bin/huyang serve >"$TMP/serve.log" 2>&1 &
SERVE_PID=$!
cleanup() {
  kill "$SERVE_PID" 2>/dev/null || true
  wait "$SERVE_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT
for _ in $(seq 1 100); do [ -S "$SOCK" ] && break; sleep 0.1; done
[ -S "$SOCK" ] || { echo "budget: control socket did not appear"; cat "$TMP/serve.log"; exit 1; }
sleep 2

rss_kb() { awk '/^VmRSS/ {print $2}' "/proc/$1/status" 2>/dev/null || echo 0; }
children_rss_kb() {
  local total=0 pid
  for pid in $(pgrep -P "$1" 2>/dev/null || true); do
    total=$((total + $(rss_kb "$pid") + $(children_rss_kb "$pid")))
  done
  echo "$total"
}
bytes_of() { [ -e "$1" ] && du -sb "$1" | cut -f1 || echo 0; }

# --- MCP stdio session helpers -------------------------------------------------
session_start() {   # $1 profile
  coproc MCP { bin/huyang mcp --profile "$1" --socket "$SOCK"; }
  exec 3<&"${MCP[0]}" 4>&"${MCP[1]}"   # plain descriptors survive $(...) subshells
  rpc initialize '{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"budget","version":"0"}}' >/dev/null
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}' >&4
}
session_stop() {   # close both the dup and the coproc's own write end so the adapter sees EOF
  exec 4>&- 3<&-
  eval "exec ${MCP[1]}>&-"
  wait "$MCP_PID" 2>/dev/null || true
}
rpc() {   # $1 method, $2 params JSON -> prints the "result" object
  local id line
  id=$((RANDOM * 32768 + RANDOM))   # rpc runs inside $(...), so a counter would not persist
  printf '{"jsonrpc":"2.0","id":%d,"method":"%s","params":%s}\n' "$id" "$1" "$2" >&4
  while IFS= read -r -t 120 line <&3; do
    if [ "$(printf '%s' "$line" | jq -r '.id // empty')" = "$id" ]; then
      printf '%s' "$line" | jq -c 'if .error then error(.error|tostring) else .result end'
      return
    fi
  done
  echo "budget: no reply to $1 (id $id)" >&2; return 1
}
call() {   # $1 tool, $2 arguments JSON -> prints structuredContent
  rpc tools/call "$(jq -cn --arg name "$1" --argjson args "$2" '{name:$name,arguments:$args}')" | jq -c '.structuredContent'
}
require_ok() {   # $1 label, $2 result; accepts ok or provisional
  local outcome; outcome="$(printf '%s' "$2" | jq -r '.outcome')"
  case "$outcome" in ok|provisional) ;; *) echo "budget: $1 outcome=$outcome: $2" >&2; exit 1;; esac
}

COLD_RSS="$(rss_kb "$SERVE_PID")"
RUN_TAG="$(date +%s)"

# --- scenario -----------------------------------------------------------------
session_start full
DIAG_OUTCOME=unknown
for round in $(seq 1 "$BUDGET_ROUNDS"); do
  opened="$(call workspace_open "$(jq -cn --arg root "$TMP/proj" '{kind:"project",root:$root}')")"
  require_ok workspace_open "$opened"
  WS="$(printf '%s' "$opened" | jq -r '.workspace.id')"
  require_ok workspace_inspect "$(call workspace_inspect "$(jq -cn --arg ws "$WS" '{workspace_id:$ws,view:"status"}')")"
  if [ $((round % 2)) -eq 1 ]; then needle=8080; content=8081; else needle=8081; content=8080; fi
  searched="$(call search "$(jq -cn --arg ws "$WS" --arg q "$needle" '{workspace_id:$ws,query:$q,mode:"literal"}')")"
  require_ok search "$searched"
  target="$(printf '%s' "$searched" | jq -c '[.data.hits[] | select(.path == "config.toml")][0] | if . then {handle:.handle} else null end')"
  [ "$target" != "null" ] || { echo "budget: search found no config.toml hit for $needle: $searched" >&2; exit 1; }
  read_result="$(call read "$(jq -cn --arg ws "$WS" --arg p "$TMP/proj/config.toml" '{workspace_id:$ws,target:{path:$p},view:"source",start_line:1,end_line:5}')")"
  require_ok read "$read_result"
  edited="$(call edit_apply "$(jq -cn --arg ws "$WS" --arg key "budget-$RUN_TAG-$round" --argjson t "$target" --arg c "$content" \
    '{workspace_id:$ws,idempotency_key:$key,operation:{kind:"replace_range",target:$t,content:$c}}')")"
  require_ok edit_apply "$edited"
  diag="$(call diagnostics "$(jq -cn --arg ws "$WS" '{workspace_id:$ws}')")"
  DIAG_OUTCOME="$(printf '%s' "$diag" | jq -r '.outcome')"
  # "unavailable" is honest for the fixture: no language server is configured for TOML.
  case "$DIAG_OUTCOME" in ok|provisional|unavailable) ;; *) echo "budget: diagnostics outcome=$DIAG_OUTCOME: $diag" >&2; exit 1;; esac
done
session_stop
sleep 1
POST_RSS="$(rss_kb "$SERVE_PID")"
PROVIDER_RSS="$(children_rss_kb "$SERVE_PID")"

# --- tool descriptor sizes per profile ------------------------------------------
TOOLS_JSON='{}'
for profile in full orient edit debug; do
  session_start "$profile"
  size="$(rpc tools/list '{}' | jq -c '.tools' | wc -c)"
  session_stop
  TOOLS_JSON="$(printf '%s' "$TOOLS_JSON" | jq -c --arg p "$profile" --argjson n "$size" '. + {($p): $n}')"
done

# --- state directory sizes ------------------------------------------------------
STATE_BYTES="$(bytes_of "$STATE")"
REGISTRY_BYTES="$(bytes_of "$STATE/registry.json")"
RECEIPTS_BYTES="$(bytes_of "$STATE/receipts")"
PLANS_BYTES="$(bytes_of "$STATE/plans")"
JOURNALS_BYTES="$(bytes_of "$STATE/commit-journals")"
DIAG_BYTES="$(bytes_of "$STATE/diagnostics")"

# --- repository sizes -----------------------------------------------------------
GO_FILES="$(git ls-files '*.go' | grep -v '_test\.go$' || true)"
GO_LINES="$(echo "$GO_FILES" | xargs cat 2>/dev/null | wc -l)"
GO_TEST_LINES="$(git ls-files '*_test.go' | xargs cat 2>/dev/null | wc -l)"
LUA_LINES="$(git ls-files '*.lua' | xargs cat 2>/dev/null | wc -l)"
LARGEST_GO_FILE="$(echo "$GO_FILES" | xargs wc -l 2>/dev/null | grep -v ' total$' | sort -n | tail -1 | awk '{print $1}')"
LONG_FUNCTIONS="$(echo "$GO_FILES" | xargs awk -v max="$BUDGET_FUNC_LINES" '
  /^func / { start = FNR }
  /^}/ && start { if (FNR - start + 1 > max) count++; start = 0 }
  END { print count + 0 }')"

# --- report ---------------------------------------------------------------------
SHA="$(git rev-parse --short HEAD)"
DATE="$(date +%Y-%m-%d)"
OUT_DIR="docs/plans/budget"
mkdir -p "$OUT_DIR"
OUT="$OUT_DIR/$DATE-$SHA.json"
jq -n --arg date "$DATE" --arg sha "$SHA" --arg provider "$PROVIDER" --arg diag "$DIAG_OUTCOME" \
  --argjson cold "$COLD_RSS" --argjson post "$POST_RSS" --argjson prov "$PROVIDER_RSS" \
  --argjson state "$STATE_BYTES" --argjson registry "$REGISTRY_BYTES" --argjson receipts "$RECEIPTS_BYTES" \
  --argjson plans "$PLANS_BYTES" --argjson journals "$JOURNALS_BYTES" --argjson diagb "$DIAG_BYTES" \
  --argjson go "$GO_LINES" --argjson gotest "$GO_TEST_LINES" --argjson lua "$LUA_LINES" \
  --argjson largest "$LARGEST_GO_FILE" --argjson longf "$LONG_FUNCTIONS" --argjson rounds "$BUDGET_ROUNDS" \
  --argjson tools "$TOOLS_JSON" --argjson maxf "$BUDGET_FUNC_LINES" '{
  date:$date, sha:$sha, rounds:$rounds, provider:$provider, diagnostics_outcome:$diag,
  cold_rss_kb:$cold, post_rss_kb:$post, provider_rss_kb:$prov,
  state_bytes:$state, registry_bytes:$registry, receipts_bytes:$receipts, plans_bytes:$plans,
  journals_bytes:$journals, diag_bytes:$diagb,
  go_lines:$go, go_test_lines:$gotest, lua_lines:$lua, largest_go_file:$largest,
  long_functions:$longf, long_function_threshold:$maxf, tools_bytes:$tools }' >"$OUT"

BASELINE="$OUT_DIR/baseline.json"
STATUS=0
printf '%-20s %12s %12s %s\n' metric value baseline tolerance
check() {   # $1 key, $2 tolerance percent
  local value base limit
  value="$(jq -r ".$1" "$OUT")"
  if [ -f "$BASELINE" ]; then
    base="$(jq -r ".$1 // empty" "$BASELINE")"
    if [ -n "$base" ]; then
      limit=$((base + base * $2 / 100))
      if [ "$value" -gt "$limit" ]; then printf '%-20s %12s %12s +%s%%  EXCEEDED\n' "$1" "$value" "$base" "$2"; STATUS=1; return; fi
    fi
  fi
  printf '%-20s %12s %12s +%s%%\n' "$1" "$value" "${base:--}" "$2"
}
check cold_rss_kb 25
check post_rss_kb 25
check state_bytes 50
check long_functions 0
check largest_go_file 10
for key in provider_rss_kb registry_bytes receipts_bytes plans_bytes journals_bytes diag_bytes go_lines go_test_lines lua_lines; do
  printf '%-20s %12s %12s\n' "$key" "$(jq -r ".$key" "$OUT")" -
done
for profile in full orient edit debug; do
  printf '%-20s %12s %12s\n' "tools_${profile}_bytes" "$(jq -r ".tools_bytes.$profile" "$OUT")" -
done
echo "budget: wrote $OUT (provider=$PROVIDER, diagnostics=$DIAG_OUTCOME)"
if [ ! -f "$BASELINE" ]; then cp "$OUT" "$BASELINE"; echo "budget: wrote first baseline $BASELINE"; fi
[ "$STATUS" -eq 0 ] && echo "huyang budget: OK" || echo "huyang budget: FAILED"
exit "$STATUS"
