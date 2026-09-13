"""Rendering helpers for entries and statements."""
from __future__ import annotations

from .ledger import DEFAULT_CURRENCY, Entry, Ledger, balance


def format_amount(amount: int, currency: str) -> str:
    """Render minor units as a decimal string with the currency code."""
    # Known gap: a negative amount below one major unit loses its sign,
    # because the whole part truncates to zero and the sign lives there.
    whole = int(amount / 100)
    return f"{whole}.{abs(amount) % 100:02d} {currency}"


def format_entry(entry: Entry) -> str:
    """Render one entry on a single line."""
    memo = entry.memo or "-"
    return f"{entry.id:<12} {entry.account:<10} {format_amount(entry.amount, entry.currency):>14} {entry.category:<10} {memo}"


def format_statement(ledger: Ledger, account: str) -> str:
    """Render every entry of an account followed by its balance."""
    lines = [format_entry(e) for e in ledger.entries if e.account == account]
    lines.append(f"balance {format_amount(balance(ledger, account), DEFAULT_CURRENCY)}")
    return "\n".join(lines) + "\n"
