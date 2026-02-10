"""Minimal cron expression parser. Supports: *, */N, N, N-M, N,M,O"""

from __future__ import annotations

import time
from dataclasses import dataclass
from typing import Optional


@dataclass
class CronExpr:
    minute: list[int]
    hour: list[int]
    day: list[int]
    month: list[int]
    weekday: list[int]  # 0=Mon, 6=Sun

    def matches(self, t: time.struct_time) -> bool:
        return (
            t.tm_min in self.minute
            and t.tm_hour in self.hour
            and t.tm_mday in self.day
            and t.tm_mon in self.month
            and (t.tm_wday in self.weekday)
        )

    def next_run(self, after: Optional[float] = None) -> float:
        """Find next matching timestamp after given time (or now)."""
        ts = after or time.time()
        # Start from next minute
        ts = ts - (ts % 60) + 60

        # Search up to 366 days ahead
        for _ in range(366 * 24 * 60):
            t = time.localtime(ts)
            if self.matches(t):
                return ts
            ts += 60

        raise ValueError("No matching time found within a year")


def parse_cron(expr: str) -> CronExpr:
    """Parse '*/5 * * * *' style cron expression."""
    parts = expr.strip().split()
    if len(parts) != 5:
        raise ValueError(f"Cron expression must have 5 fields, got {len(parts)}: {expr}")

    return CronExpr(
        minute=_parse_field(parts[0], 0, 59),
        hour=_parse_field(parts[1], 0, 23),
        day=_parse_field(parts[2], 1, 31),
        month=_parse_field(parts[3], 1, 12),
        weekday=_parse_field(parts[4], 0, 6),
    )


def _parse_field(field: str, min_val: int, max_val: int) -> list[int]:
    """Parse a single cron field into list of matching values."""
    values: set[int] = set()

    for part in field.split(","):
        part = part.strip()

        if part == "*":
            values.update(range(min_val, max_val + 1))
        elif part.startswith("*/"):
            step = int(part[2:])
            values.update(range(min_val, max_val + 1, step))
        elif "-" in part:
            start, end = part.split("-", 1)
            values.update(range(int(start), int(end) + 1))
        else:
            values.add(int(part))

    return sorted(values)
