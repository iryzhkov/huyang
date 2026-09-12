"""Tab-separated persistence for a ledger."""
from __future__ import annotations

from datetime import datetime
from typing import IO

from .ledger import Entry, Ledger, apply_entry, new_ledger


def save(stream: IO[str], ledger: Ledger) -> None:
    """Write the ledger as tab-separated lines: id, account, amount, currency,
    category, posted (ISO 8601), memo."""
    for entry in ledger.entries:
        assert entry.posted is not None
        fields = [
            entry.id, entry.account, str(entry.amount), entry.currency, entry.category,
            entry.posted.isoformat(), entry.memo,
        ]
        stream.write("\t".join(fields) + "\n")


def load(stream: IO[str]) -> Ledger:
    """Read lines written by save and apply them to a new ledger."""
    ledger = new_ledger()
    for line_no, line in enumerate(stream, start=1):
        fields = line.rstrip("\n").split("\t")
        if len(fields) != 7:
            raise ValueError(f"store: line {line_no} has {len(fields)} fields, want 7")
        entry = Entry(
            id=fields[0], account=fields[1], amount=int(fields[2]), currency=fields[3],
            category=fields[4], posted=datetime.fromisoformat(fields[5]), memo=fields[6],
        )
        apply_entry(ledger, entry)
    return ledger
