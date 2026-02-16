"""File-based transport implementation."""

import contextlib
import fcntl
import re
import shutil
import sys
import tempfile
import time
from pathlib import Path
from typing import Iterator

import yaml

from .message import Message
from .transport import Transport

DEFAULT_BASE_DIR = "/tmp/bp-tunnel"

# Only alphanumeric, dash, underscore allowed (path traversal protection)
_SAFE_NAME = re.compile(r"^[a-zA-Z0-9_\-]+$")


def _validate_name(name: str, label: str = "name") -> None:
    if not name or not _SAFE_NAME.match(name):
        raise ValueError(f"invalid {label}: '{name}' (only alphanumeric, -, _ allowed)")


class FileTransport(Transport):
    def __init__(self, base_dir: str = DEFAULT_BASE_DIR):
        self.base_dir = Path(base_dir).resolve()

    def _tunnel_dir(self, tunnel: str) -> Path:
        return self.base_dir / tunnel

    def _agent_dir(self, tunnel: str, agent: str) -> Path:
        return self._tunnel_dir(tunnel) / agent

    def _meta_file(self, tunnel: str) -> Path:
        return self._tunnel_dir(tunnel) / "_meta.yaml"

    def _lock_file(self, tunnel: str) -> Path:
        return self._tunnel_dir(tunnel) / "_meta.lock"

    @contextlib.contextmanager
    def _locked_meta(self, tunnel: str):
        """Read/write _meta.yaml under an exclusive file lock."""
        self._tunnel_dir(tunnel).mkdir(parents=True, exist_ok=True)
        lock_path = self._lock_file(tunnel)
        f = open(lock_path, "w")
        try:
            fcntl.flock(f, fcntl.LOCK_EX)
            meta_file = self._meta_file(tunnel)
            if meta_file.exists():
                meta = yaml.safe_load(meta_file.read_text()) or {"admins": [], "members": []}
            else:
                meta = {"admins": [], "members": []}

            yield meta

            # Atomic write: temp file + rename
            fd, tmp = tempfile.mkstemp(dir=self._tunnel_dir(tunnel), suffix=".tmp")
            try:
                with open(fd, "w") as tf:
                    tf.write(yaml.dump(meta, default_flow_style=False, allow_unicode=True))
                Path(tmp).rename(meta_file)
            except Exception:
                Path(tmp).unlink(missing_ok=True)
                raise
        finally:
            fcntl.flock(f, fcntl.LOCK_UN)
            f.close()

    def _exists(self, tunnel: str) -> bool:
        return self._meta_file(tunnel).exists()

    def _require_tunnel(self, tunnel: str) -> None:
        """Raise if tunnel does not exist."""
        if not self._exists(tunnel):
            raise ValueError(f"tunnel '{tunnel}' does not exist")

    # --- tunnel management ---

    def create(self, tunnel: str, creator: str) -> None:
        _validate_name(tunnel, "tunnel")
        _validate_name(creator, "agent")
        if self._exists(tunnel):
            raise ValueError(f"tunnel '{tunnel}' already exists")
        self._tunnel_dir(tunnel).mkdir(parents=True, exist_ok=True)
        self._agent_dir(tunnel, creator).mkdir(exist_ok=True)
        with self._locked_meta(tunnel) as meta:
            meta["admins"] = [creator]
            meta["members"] = [creator]

    def join(self, tunnel: str, agent: str) -> None:
        _validate_name(agent, "agent")
        self._require_tunnel(tunnel)
        self._agent_dir(tunnel, agent).mkdir(parents=True, exist_ok=True)
        with self._locked_meta(tunnel) as meta:
            if agent not in meta["members"]:
                meta["members"].append(agent)

    def leave(self, tunnel: str, agent: str) -> None:
        self._require_tunnel(tunnel)
        with self._locked_meta(tunnel) as meta:
            if agent in meta["members"]:
                meta["members"].remove(agent)
            if agent in meta["admins"]:
                meta["admins"].remove(agent)
        agent_dir = self._agent_dir(tunnel, agent)
        if agent_dir.exists():
            shutil.rmtree(agent_dir)

    def destroy(self, tunnel: str) -> None:
        self._require_tunnel(tunnel)
        tunnel_dir = self._tunnel_dir(tunnel)
        if tunnel_dir.exists():
            shutil.rmtree(tunnel_dir)

    def promote(self, tunnel: str, agent: str) -> None:
        self._require_tunnel(tunnel)
        with self._locked_meta(tunnel) as meta:
            if agent not in meta["members"]:
                raise ValueError(f"{agent} is not a member")
            if agent not in meta["admins"]:
                meta["admins"].append(agent)

    def demote(self, tunnel: str, agent: str) -> None:
        self._require_tunnel(tunnel)
        with self._locked_meta(tunnel) as meta:
            if agent in meta["admins"]:
                meta["admins"].remove(agent)

    # --- messaging ---

    def send(self, tunnel: str, msg: Message) -> None:
        self._require_tunnel(tunnel)
        members = self.members(tunnel)
        if msg.from_ not in members:
            raise PermissionError(f"{msg.from_} is not a member of tunnel '{tunnel}'")
        if msg.to:
            _validate_name(msg.to, "recipient")
            if msg.to not in members:
                raise ValueError(f"recipient '{msg.to}' is not a member of tunnel '{tunnel}'")
            self._write_to_mailbox(tunnel, msg.to, msg)
        else:
            for member in members:
                if member != msg.from_:
                    self._write_to_mailbox(tunnel, member, msg)

    def _write_to_mailbox(self, tunnel: str, agent: str, msg: Message) -> None:
        d = self._agent_dir(tunnel, agent)
        d.mkdir(parents=True, exist_ok=True)
        target = d / msg.filename()
        fd, tmp = tempfile.mkstemp(dir=d, suffix=".tmp")
        try:
            with open(fd, "w") as f:
                f.write(msg.to_yaml())
            Path(tmp).rename(target)
        except Exception:
            Path(tmp).unlink(missing_ok=True)
            raise

    def receive(self, tunnel: str, agent: str, from_: str | None = None) -> Message | None:
        if from_ is not None:
            _validate_name(from_, "from filter")
        d = self._agent_dir(tunnel, agent)
        if not d.exists():
            return None
        files = sorted(
            [f for f in d.iterdir() if f.suffix == ".yaml" and not f.name.startswith("_")],
            key=lambda f: f.name,
        )
        for f in files:
            try:
                raw = f.read_text()
                msg = Message.from_yaml(raw)
            except FileNotFoundError:
                # Another consumer may have deleted this file
                continue
            except (yaml.YAMLError, KeyError, ValueError, TypeError) as e:
                print(f"bp-tunnel: corrupt message {f.name}, deleting: {e}", file=sys.stderr)
                f.unlink(missing_ok=True)
                continue
            if from_ is None or msg.from_ == from_:
                try:
                    f.unlink()
                except FileNotFoundError:
                    continue
                return msg
        return None

    def listen(self, tunnel: str, agent: str, from_: str | None = None) -> Iterator[Message]:
        while True:
            msg = self.receive(tunnel, agent, from_=from_)
            if msg:
                yield msg
            else:
                time.sleep(0.1)

    # --- queries ---

    def members(self, tunnel: str) -> list[str]:
        if not self._exists(tunnel):
            return []
        with self._locked_meta(tunnel) as meta:
            return list(meta.get("members", []))

    def admins(self, tunnel: str) -> list[str]:
        if not self._exists(tunnel):
            return []
        with self._locked_meta(tunnel) as meta:
            return list(meta.get("admins", []))

    def is_admin(self, tunnel: str, agent: str) -> bool:
        return agent in self.admins(tunnel)

    def tunnels(self) -> list[str]:
        if not self.base_dir.exists():
            return []
        return sorted(
            d.name for d in self.base_dir.iterdir()
            if d.is_dir() and (d / "_meta.yaml").exists()
        )
