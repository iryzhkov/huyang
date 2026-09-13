#!/usr/bin/env bash
# The ledger the live fixtures share.

# total prints the amount left in the ledger.
total() {
    echo 7
}

# unused is called from nowhere, so a reference-checked deletion may remove it.
unused() {
    echo 0
}
