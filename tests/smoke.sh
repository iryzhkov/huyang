#!/usr/bin/env bash
# Fast Huyang kernel and service smoke gate.
#
# Usage: tests/smoke.sh [lua] [go]
# With no arguments every suite runs. Naming suites narrows the run while
# iterating: "lua" runs the Neovim unit tests, "go" runs the Go packages.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
export PATH="$PATH:$HOME/.local/share/nvim/mason/bin"

run_lua=false
run_go=false
if [ "$#" -eq 0 ]; then
    run_lua=true
    run_go=true
fi
for suite in "$@"; do
    case "$suite" in
        lua) run_lua=true ;;
        go) run_go=true ;;
        *) echo "unknown suite: $suite (want lua or go)" >&2; exit 2 ;;
    esac
done

if $run_lua; then
    command -v nvim >/dev/null || { echo "nvim not found"; exit 1; }
    for unit in unit_edit unit_check unit_index unit_dap; do
        nvim --clean --headless -u "$REPO/tests/minimal_init.lua" -l "$REPO/tests/$unit.lua"
    done
fi

if $run_go; then
    go test ./internal/service ./internal/handlers ./internal/mcpapi ./internal/providerpool ./internal/provider/...
fi
echo "huyang smoke: OK"
