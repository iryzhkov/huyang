#!/usr/bin/env bash
# A caller, so an inline and a rename have something to move.

source "$(dirname "${BASH_SOURCE[0]}")/ledger.sh"

report() {
    echo $(($(total) + 1))
}
