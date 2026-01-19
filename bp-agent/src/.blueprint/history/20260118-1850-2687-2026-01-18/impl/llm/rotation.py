"""Shared rotation and rate-limit policy."""

from __future__ import annotations

import random
import time
from dataclasses import dataclass, field
from typing import Optional


@dataclass
class RotationPolicy:
    max_retries: int = 3
    backoff_base_ms: int = 500
    backoff_max_ms: int = 8000
    jitter: bool = True
    cooldown_seconds: int = 60
    rotate_on: list[str] = field(default_factory=lambda: ["rate_limit", "quota", "auth_error"])


@dataclass
class RotationSlot:
    id: str
    state: str = "healthy"
    last_error: Optional[str] = None
    cooldown_until: Optional[float] = None
    weight: int = 1


class RotationManager:
    def __init__(self, policy: RotationPolicy | None = None):
        self.policy = policy or RotationPolicy()
        self._slots: dict[str, RotationSlot] = {}
        self._rr_index = 0

    def add_slot(self, slot: RotationSlot):
        self._slots[slot.id] = slot

    def select_slot(self) -> RotationSlot:
        self._refresh_cooldowns()
        pool = self._eligible_pool()
        if not pool:
            raise RuntimeError("No available slots")

        slot_id = pool[self._rr_index % len(pool)]
        self._rr_index += 1
        return self._slots[slot_id]

    def report_success(self, slot_id: str):
        slot = self._slots[slot_id]
        slot.state = "healthy"
        slot.last_error = None
        slot.cooldown_until = None

    def report_rate_limit(self, slot_id: str, reason: str | None = None):
        slot = self._slots[slot_id]
        slot.state = "cooldown"
        slot.last_error = reason or "rate_limit"
        slot.cooldown_until = time.time() + self.policy.cooldown_seconds

    def report_auth_error(self, slot_id: str):
        slot = self._slots[slot_id]
        slot.state = "disabled"
        slot.last_error = "auth_error"

    def disable_slot(self, slot_id: str):
        slot = self._slots[slot_id]
        slot.state = "disabled"

    def backoff(self, attempt: int):
        base = min(self.policy.backoff_max_ms, self.policy.backoff_base_ms * (2 ** max(attempt - 1, 0)))
        delay_ms = base
        if self.policy.jitter:
            delay_ms = random.randint(int(base * 0.5), base)
        time.sleep(delay_ms / 1000.0)

    def _eligible_pool(self) -> list[str]:
        pool: list[str] = []
        for slot in self._slots.values():
            if slot.state != "healthy":
                continue
            weight = max(slot.weight, 1)
            pool.extend([slot.id] * weight)
        return pool

    def _refresh_cooldowns(self):
        now = time.time()
        for slot in self._slots.values():
            if slot.state == "cooldown" and slot.cooldown_until is not None:
                if now >= slot.cooldown_until:
                    slot.state = "healthy"
                    slot.cooldown_until = None
                    slot.last_error = None
