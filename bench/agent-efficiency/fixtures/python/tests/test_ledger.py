"""Unit tests for the ledger package (run with python3 -m unittest)."""
from __future__ import annotations

import io
import unittest
from datetime import datetime, timedelta, timezone

from ledger.fmt import format_amount
from ledger.ledger import DuplicateEntry, Entry, apply_entry, balance, between, new_ledger, transfer
from ledger.report import summarize_quarter
from ledger.store import load, save


def sample():
    ledger = new_ledger()
    apply_entry(ledger, Entry(id="e1", account="cash", amount=10000, category="salary"))
    apply_entry(ledger, Entry(id="e2", account="cash", amount=-2500, category="groceries"))
    apply_entry(ledger, Entry(id="e3", account="savings", amount=5000, category="transfer"))
    return ledger


class LedgerTests(unittest.TestCase):
    def test_apply_entry_rejects_duplicates(self):
        ledger = sample()
        with self.assertRaises(DuplicateEntry):
            apply_entry(ledger, Entry(id="e1", account="cash", amount=1))

    def test_balance_sums_account(self):
        self.assertEqual(balance(sample(), "cash"), 7500)

    def test_transfer_moves_amount(self):
        ledger = sample()
        transfer(ledger, "t1", "cash", "savings", 500)
        self.assertEqual(balance(ledger, "cash"), 7000)
        self.assertEqual(balance(ledger, "savings"), 5500)

    def test_save_load_round_trip(self):
        ledger = sample()
        buffer = io.StringIO()
        save(buffer, ledger)
        loaded = load(io.StringIO(buffer.getvalue()))
        self.assertEqual(balance(loaded, "cash"), balance(ledger, "cash"))

    def test_quarter_summary_counts_entries(self):
        now = datetime.now(timezone.utc)
        summary = summarize_quarter(sample(), now.year, (now.month - 1) // 3 + 1)
        self.assertEqual(summary.entries, 3)

    def test_format_amount_negative(self):
        # Documents a known gap: negative amounts below one major unit lose
        # their sign. Fails on purpose so the V1 scenario has a failure to report.
        self.assertEqual(format_amount(-5, "USD"), "-0.05 USD")

    def test_between_uses_half_open_range(self):
        ledger = new_ledger()
        day = datetime(2026, 3, 1, tzinfo=timezone.utc)
        apply_entry(ledger, Entry(id="a", account="x", amount=1, posted=day))
        apply_entry(ledger, Entry(id="b", account="x", amount=1, posted=day + timedelta(days=1)))
        self.assertEqual(len(between(ledger, day, day + timedelta(days=1))), 1)


if __name__ == "__main__":
    unittest.main()
