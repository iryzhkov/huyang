#!/usr/bin/env bash
# Fetch the nvim-dap checkout the debugger tests run against.
#
# nvim-dap (GPL-3.0) is a runtime dependency discovered from the host Neovim
# installation; it is not distributed with Huyang. The test harness
# (tests/minimal_init.lua) looks for it in HUYANG_NVIM_DAP_PATH, then in
# tests/.deps/nvim-dap (this clone, gitignored), then in the lazy.nvim clone
# of the user's Neovim. This script needs network access to github.com the
# first time; it does nothing when the clone already exists.
#
# Usage: tests/fetch-nvim-dap.sh
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="$REPO/tests/.deps/nvim-dap"
UPSTREAM="https://github.com/mfussenegger/nvim-dap"
# Pinned upstream commit the debugger suites were written against.
COMMIT="c9a0738e45f1bd41d792a126941348dce661cf9b"

if [ -f "$DEST/lua/dap.lua" ]; then
    echo "nvim-dap already present at $DEST"
    exit 0
fi

command -v git >/dev/null || { echo "git not found" >&2; exit 1; }
mkdir -p "$(dirname "$DEST")"
git clone --quiet --no-checkout "$UPSTREAM" "$DEST"
git -C "$DEST" checkout --quiet "$COMMIT"
echo "nvim-dap $COMMIT cloned into $DEST"
