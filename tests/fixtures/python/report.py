"""A caller, so an inline and a rename have something to move."""

from ledger import total


def report() -> int:
    """Return the ledger total plus one."""
    return total() + 1
