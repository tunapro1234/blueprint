"""Standard message format."""

import os
import time
from dataclasses import dataclass

import yaml


def _rand_id() -> str:
    """Generate a random 8-char hex ID."""
    return os.urandom(4).hex()


@dataclass
class Message:
    id: str
    from_: str
    payload: str
    ts: int
    to: str | None = None  # None = broadcast
    type: str | None = None  # optional message type (e.g. "request", "result", "error")

    def to_yaml(self) -> str:
        d = {"id": self.id, "from": self.from_, "payload": self.payload, "ts": self.ts}
        if self.to is not None:
            d["to"] = self.to
        if self.type is not None:
            d["type"] = self.type
        return yaml.dump(d, default_flow_style=False, allow_unicode=True)

    @classmethod
    def from_yaml(cls, raw: str) -> "Message":
        d = yaml.safe_load(raw)
        return cls(
            id=str(d["id"]),
            from_=str(d["from"]),
            payload=str(d["payload"]),
            ts=int(d["ts"]),
            to=d.get("to"),
            type=d.get("type"),
        )

    def filename(self) -> str:
        return f"{time.time_ns()}-{self.id}.yaml"


def new_message(from_: str, payload: str, to: str | None = None, type: str | None = None) -> Message:
    return Message(
        id=_rand_id(),
        from_=from_,
        payload=payload,
        ts=int(time.time()),
        to=to,
        type=type,
    )
