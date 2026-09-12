"""Quarterly and sectioned reports over a ledger."""
from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone

from .fmt import format_amount, format_entry
from .ledger import DEFAULT_CURRENCY, Ledger, between


@dataclass
class QuarterSummary:
    """The aggregate a quarterly report is built from."""

    year: int
    quarter: int
    entries: int = 0
    income: int = 0
    expense: int = 0
    by_cat: dict[str, int] = field(default_factory=dict)
    accounts: list[str] = field(default_factory=list)


@dataclass
class Section:
    """One rendered block of a report."""

    title: str
    lines: list[str]


def quarter_bounds(year: int, quarter: int) -> tuple[datetime, datetime]:
    start = datetime(year, (quarter - 1) * 3 + 1, 1, tzinfo=timezone.utc)
    end_month = start.month + 3
    end = datetime(year + (1 if end_month > 12 else 0), end_month if end_month <= 12 else end_month - 12, 1, tzinfo=timezone.utc)
    return start, end


def report_section_01(ledger: Ledger) -> Section:
    """Render the salary view number 1 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "salary":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 1 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="salary #1", lines=lines)


def report_section_02(ledger: Ledger) -> Section:
    """Render the groceries view number 2 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "groceries":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 2 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="groceries #2", lines=lines)


def report_section_03(ledger: Ledger) -> Section:
    """Render the transport view number 3 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "transport":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 3 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="transport #3", lines=lines)


def report_section_04(ledger: Ledger) -> Section:
    """Render the utilities view number 4 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "utilities":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 4 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="utilities #4", lines=lines)


def report_section_05(ledger: Ledger) -> Section:
    """Render the leisure view number 5 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "leisure":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 5 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="leisure #5", lines=lines)


def report_section_06(ledger: Ledger) -> Section:
    """Render the rent view number 6 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "rent":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 6 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="rent #6", lines=lines)


def report_section_07(ledger: Ledger) -> Section:
    """Render the salary view number 7 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "salary":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 7 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="salary #7", lines=lines)


def report_section_08(ledger: Ledger) -> Section:
    """Render the groceries view number 8 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "groceries":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 8 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="groceries #8", lines=lines)


def report_section_09(ledger: Ledger) -> Section:
    """Render the transport view number 9 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "transport":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 9 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="transport #9", lines=lines)


def report_section_10(ledger: Ledger) -> Section:
    """Render the utilities view number 10 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "utilities":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 10 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="utilities #10", lines=lines)


def report_section_11(ledger: Ledger) -> Section:
    """Render the leisure view number 11 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "leisure":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 11 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="leisure #11", lines=lines)


def report_section_12(ledger: Ledger) -> Section:
    """Render the rent view number 12 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "rent":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 12 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="rent #12", lines=lines)


def report_section_13(ledger: Ledger) -> Section:
    """Render the salary view number 13 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "salary":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 13 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="salary #13", lines=lines)


def report_section_14(ledger: Ledger) -> Section:
    """Render the groceries view number 14 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "groceries":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 14 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="groceries #14", lines=lines)


def report_section_15(ledger: Ledger) -> Section:
    """Render the transport view number 15 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "transport":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 15 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="transport #15", lines=lines)


def report_section_16(ledger: Ledger) -> Section:
    """Render the utilities view number 16 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "utilities":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 16 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="utilities #16", lines=lines)


def report_section_17(ledger: Ledger) -> Section:
    """Render the leisure view number 17 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "leisure":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 17 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="leisure #17", lines=lines)


def report_section_18(ledger: Ledger) -> Section:
    """Render the rent view number 18 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "rent":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 18 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="rent #18", lines=lines)


def report_section_19(ledger: Ledger) -> Section:
    """Render the salary view number 19 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "salary":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 19 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="salary #19", lines=lines)


def report_section_20(ledger: Ledger) -> Section:
    """Render the groceries view number 20 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "groceries":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 20 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="groceries #20", lines=lines)


def report_section_21(ledger: Ledger) -> Section:
    """Render the transport view number 21 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "transport":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 21 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="transport #21", lines=lines)


def report_section_22(ledger: Ledger) -> Section:
    """Render the utilities view number 22 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "utilities":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 22 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="utilities #22", lines=lines)


def report_section_23(ledger: Ledger) -> Section:
    """Render the leisure view number 23 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "leisure":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 23 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="leisure #23", lines=lines)


def report_section_24(ledger: Ledger) -> Section:
    """Render the rent view number 24 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "rent":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 24 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="rent #24", lines=lines)


def report_section_25(ledger: Ledger) -> Section:
    """Render the salary view number 25 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "salary":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 25 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="salary #25", lines=lines)


def summarize_quarter(ledger: Ledger, year: int, quarter: int) -> QuarterSummary:
    """Aggregate the entries posted in one calendar quarter."""
    start, end = quarter_bounds(year, quarter)
    summary = QuarterSummary(year=year, quarter=quarter)
    seen: set[str] = set()
    for entry in between(ledger, start, end):
        summary.entries += 1
        if entry.amount >= 0:
            summary.income += entry.amount
        else:
            summary.expense -= entry.amount
        summary.by_cat[entry.category] = summary.by_cat.get(entry.category, 0) + entry.amount
        seen.add(entry.account)
    summary.accounts = sorted(seen)
    return summary


def report_section_26(ledger: Ledger) -> Section:
    """Render the groceries view number 26 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "groceries":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 26 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="groceries #26", lines=lines)


def report_section_27(ledger: Ledger) -> Section:
    """Render the transport view number 27 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "transport":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 27 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="transport #27", lines=lines)


def report_section_28(ledger: Ledger) -> Section:
    """Render the utilities view number 28 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "utilities":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 28 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="utilities #28", lines=lines)


def report_section_29(ledger: Ledger) -> Section:
    """Render the leisure view number 29 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "leisure":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 29 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="leisure #29", lines=lines)


def report_section_30(ledger: Ledger) -> Section:
    """Render the rent view number 30 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "rent":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 30 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="rent #30", lines=lines)


def validate_summary(summary: QuarterSummary) -> None:
    """Check a summary before it is rendered."""
    # Validation rules for a quarterly summary. Each rule is checked in
    # order and the first failure wins; the messages are user-facing.
    if summary.quarter < 1 or summary.quarter > 4:
        raise ValueError(f"report: quarter {summary.quarter} is outside 1-4")
    if summary.year < 1970:
        raise ValueError(f"report: year {summary.year} is before the epoch")
    if summary.income < 0 or summary.expense < 0:
        raise ValueError("report: income and expense must be non-negative")
    if not summary.by_cat and summary.entries > 0:
        raise ValueError(f"report: {summary.entries} entries but no categories")


def report_section_31(ledger: Ledger) -> Section:
    """Render the salary view number 31 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "salary":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 31 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="salary #31", lines=lines)


def report_section_32(ledger: Ledger) -> Section:
    """Render the groceries view number 32 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "groceries":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 32 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="groceries #32", lines=lines)


def report_section_33(ledger: Ledger) -> Section:
    """Render the transport view number 33 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "transport":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 33 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="transport #33", lines=lines)


def report_section_34(ledger: Ledger) -> Section:
    """Render the utilities view number 34 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "utilities":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 34 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="utilities #34", lines=lines)


def report_section_35(ledger: Ledger) -> Section:
    """Render the leisure view number 35 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "leisure":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 35 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="leisure #35", lines=lines)


def report_section_36(ledger: Ledger) -> Section:
    """Render the rent view number 36 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "rent":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 36 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="rent #36", lines=lines)


def report_section_37(ledger: Ledger) -> Section:
    """Render the salary view number 37 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "salary":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 37 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="salary #37", lines=lines)


def report_section_38(ledger: Ledger) -> Section:
    """Render the groceries view number 38 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "groceries":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 38 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="groceries #38", lines=lines)


def report_section_39(ledger: Ledger) -> Section:
    """Render the transport view number 39 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "transport":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 39 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="transport #39", lines=lines)


def report_section_40(ledger: Ledger) -> Section:
    """Render the utilities view number 40 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "utilities":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 40 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="utilities #40", lines=lines)


def report_section_41(ledger: Ledger) -> Section:
    """Render the leisure view number 41 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "leisure":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 41 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="leisure #41", lines=lines)


def report_section_42(ledger: Ledger) -> Section:
    """Render the rent view number 42 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "rent":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 42 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="rent #42", lines=lines)


def report_section_43(ledger: Ledger) -> Section:
    """Render the salary view number 43 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "salary":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 43 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="salary #43", lines=lines)


def report_section_44(ledger: Ledger) -> Section:
    """Render the groceries view number 44 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "groceries":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 44 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="groceries #44", lines=lines)


def report_section_45(ledger: Ledger) -> Section:
    """Render the transport view number 45 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "transport":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 45 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="transport #45", lines=lines)


def report_section_46(ledger: Ledger) -> Section:
    """Render the utilities view number 46 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "utilities":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 46 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="utilities #46", lines=lines)


def report_section_47(ledger: Ledger) -> Section:
    """Render the leisure view number 47 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "leisure":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 47 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="leisure #47", lines=lines)


def report_section_48(ledger: Ledger) -> Section:
    """Render the rent view number 48 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "rent":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 48 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="rent #48", lines=lines)


def report_section_49(ledger: Ledger) -> Section:
    """Render the salary view number 49 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "salary":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 49 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="salary #49", lines=lines)


def report_section_50(ledger: Ledger) -> Section:
    """Render the groceries view number 50 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "groceries":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 50 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="groceries #50", lines=lines)


def report_section_51(ledger: Ledger) -> Section:
    """Render the transport view number 51 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "transport":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 51 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="transport #51", lines=lines)


def report_section_52(ledger: Ledger) -> Section:
    """Render the utilities view number 52 of the ledger."""
    lines: list[str] = []
    total = 0
    for entry in ledger.entries:
        if entry.category != "utilities":
            continue
        total += entry.amount
        lines.append(format_entry(entry))
    lines.append(f"section 52 total {format_amount(total, DEFAULT_CURRENCY)}")
    return Section(title="utilities #52", lines=lines)


def render(ledger: Ledger) -> str:
    """Render every section in order."""
    sections = [
        report_section_01(ledger),
        report_section_02(ledger),
        report_section_03(ledger),
        report_section_04(ledger),
        report_section_05(ledger),
        report_section_06(ledger),
        report_section_07(ledger),
        report_section_08(ledger),
        report_section_09(ledger),
        report_section_10(ledger),
        report_section_11(ledger),
        report_section_12(ledger),
        report_section_13(ledger),
        report_section_14(ledger),
        report_section_15(ledger),
        report_section_16(ledger),
        report_section_17(ledger),
        report_section_18(ledger),
        report_section_19(ledger),
        report_section_20(ledger),
        report_section_21(ledger),
        report_section_22(ledger),
        report_section_23(ledger),
        report_section_24(ledger),
        report_section_25(ledger),
        report_section_26(ledger),
        report_section_27(ledger),
        report_section_28(ledger),
        report_section_29(ledger),
        report_section_30(ledger),
        report_section_31(ledger),
        report_section_32(ledger),
        report_section_33(ledger),
        report_section_34(ledger),
        report_section_35(ledger),
        report_section_36(ledger),
        report_section_37(ledger),
        report_section_38(ledger),
        report_section_39(ledger),
        report_section_40(ledger),
        report_section_41(ledger),
        report_section_42(ledger),
        report_section_43(ledger),
        report_section_44(ledger),
        report_section_45(ledger),
        report_section_46(ledger),
        report_section_47(ledger),
        report_section_48(ledger),
        report_section_49(ledger),
        report_section_50(ledger),
        report_section_51(ledger),
        report_section_52(ledger),
    ]
    out: list[str] = []
    for section in sections:
        out.append(section.title)
        out.extend(section.lines)
    return "\n".join(out) + "\n"
