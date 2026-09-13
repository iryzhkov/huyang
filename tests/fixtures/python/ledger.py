"""The ledger the live fixtures share."""


def total() -> int:
    """Return the amount left in the ledger."""
    return 7


def unused() -> int:
    """Nothing calls this, so a reference-checked deletion may remove it."""
    return 0
