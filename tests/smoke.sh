#!/usr/bin/env bash
# Fast Huyang kernel and service smoke gate.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
export PATH="$PATH:$HOME/.local/share/nvim/mason/bin"

command -v nvim >/dev/null || { echo "nvim not found"; exit 1; }

nvim --clean --headless -u "$REPO/tests/minimal_init.lua" -l "$REPO/tests/unit_edit.lua"
nvim --clean --headless -u "$REPO/tests/minimal_init.lua" -l "$REPO/tests/unit_testrun.lua"
nvim --clean --headless -u "$REPO/tests/minimal_init.lua" -l "$REPO/tests/unit_check.lua"
nvim --clean --headless -u "$REPO/tests/minimal_init.lua" -l "$REPO/tests/unit_index.lua"

go test ./internal/bridge ./internal/provider/...
echo "huyang smoke: OK"
