"""Core ledger types and operations."""
from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone

# Currency assumed for entries that do not name one.
DEFAULT_CURRENCY = "USD"

# Bound on the number of entries so an unbounded import cannot exhaust memory.
MAX_ENTRIES = 10000


class DuplicateEntry(Exception):
    """Raised when an entry id was already applied."""


class LedgerFull(Exception):
    """Raised when the ledger holds MAX_ENTRIES entries."""


@dataclass
class Entry:
    """One posted amount, in minor units (cents)."""

    id: str
    account: str
    amount: int
    currency: str = ""
    category: str = ""
    posted: datetime | None = None
    memo: str = ""


@dataclass
class Ledger:
    """An append-only list of entries with a per-account balance cache."""

    entries: list[Entry] = field(default_factory=list)
    balances: dict[str, int] = field(default_factory=dict)
    seen: set[str] = field(default_factory=set)


def new_ledger() -> Ledger:
    """Return an empty ledger."""
    return Ledger()


def apply_entry(ledger: Ledger, entry: Entry) -> None:
    """Validate and append one entry, updating the account balance."""
    if not entry.id:
        raise ValueError("ledger: entry needs an id")
    if not entry.account:
        raise ValueError("ledger: entry needs an account")
    if entry.id in ledger.seen:
        raise DuplicateEntry(entry.id)
    if len(ledger.entries) >= MAX_ENTRIES:
        raise LedgerFull()
    if not entry.currency:
        entry.currency = DEFAULT_CURRENCY
    if entry.posted is None:
        entry.posted = datetime.now(timezone.utc)
    ledger.entries.append(entry)
    ledger.seen.add(entry.id)
    ledger.balances[entry.account] = ledger.balances.get(entry.account, 0) + entry.amount


def balance(ledger: Ledger, account: str) -> int:
    """Return the balance of one account, summed from the entries so the
    cache can be cross-checked."""
    total = 0
    for entry in ledger.entries:
        if entry.account != account:
            continue
        total += entry.amount
    cached = ledger.balances.get(account)
    if cached is not None and cached != total:
        raise RuntimeError(f"ledger: balance cache drift for {account}: {cached} != {total}")
    return total


def accounts(ledger: Ledger) -> list[str]:
    """List the accounts that have at least one entry, sorted."""
    return sorted(ledger.balances)


def by_category(ledger: Ledger) -> dict[str, int]:
    """Group entry amounts by category."""
    out: dict[str, int] = {}
    for entry in ledger.entries:
        out[entry.category] = out.get(entry.category, 0) + entry.amount
    return out


def between(ledger: Ledger, start: datetime, end: datetime) -> list[Entry]:
    """Return the entries posted in [start, end)."""
    out = []
    for entry in ledger.entries:
        assert entry.posted is not None
        if start <= entry.posted < end:
            out.append(entry)
    return out


def transfer(ledger: Ledger, id: str, source: str, target: str, amount: int) -> None:
    """Post a matching pair of entries moving amount between two accounts."""
    if amount <= 0:
        raise ValueError("ledger: transfer amount must be positive")
    apply_entry(ledger, Entry(id=f"{id}:out", account=source, amount=-amount, category="transfer"))
    apply_entry(ledger, Entry(id=f"{id}:in", account=target, amount=amount, category="transfer"))


def import_entries(ledger: Ledger, entries: list[Entry]) -> int:
    """Apply every entry and stop at the first error, returning how many were applied."""
    for index, entry in enumerate(entries):
        try:
            apply_entry(ledger, entry)
        except Exception as error:  # noqa: BLE001 - wrap any failure with its index
            raise RuntimeError(f"ledger: import entry {index}: {error}") from error
    return len(entries)
