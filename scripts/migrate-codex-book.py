#!/usr/bin/env python3
"""One-time authority/launch migration. Default: write review copies only."""
import argparse
import contextlib
import copy
import datetime
import fcntl
import json
import os
from pathlib import Path
import tempfile
import uuid

ROOT = "server-main"
OLD = "server-main-claude"
BOOKS = [Path("/srv/server-main/agentbook.json"),
         Path("/srv/probot/.orchestration/agentbook.json")]


def migrate(books, thread, bindings=None):
    uuid.UUID(thread)
    result = copy.deepcopy(books)
    if result[0].get("orchestrator") not in (OLD, ROOT):
        raise ValueError("unexpected fleet root; refusing migration")
    if not any(a["name"] == ROOT for a in result[0]["agents"]):
        raise ValueError("server-main must already exist in the primary book")
    result[0]["orchestrator"] = ROOT
    for book in result:
        if book.get("parent") == OLD:
            book["parent"] = ROOT
        for agent in book["agents"]:
            if agent.get("parent") == OLD or agent["name"] == OLD:
                agent["parent"] = ROOT
            if agent["name"] == ROOT:
                if agent.get("folder") != "/srv":
                    raise ValueError("unexpected server-main cwd")
                agent.pop("parent", None)
                # Model, effort and tier stay with the existing Codex thread.
                agent["identityThreadId"] = thread
                agent["launch"] = {"codex": True, "remote": "unix://",
                                   "resume": True, "resumeId": thread,
                                   "noSandbox": True}
    bindings = bindings or {}
    names = {a["name"] for b in result for a in b["agents"]}
    if any(name not in names for name in bindings):
        raise ValueError("identity binding names an unknown agent")
    if len(set(bindings.values())) != len(bindings):
        raise ValueError("thread identity must be unique")
    for book in result:
        for agent in book["agents"]:
            if agent["name"] in bindings:
                identity_thread = bindings[agent["name"]]
                uuid.UUID(identity_thread)
                pin = agent.get("launch", {}).get("resumeId")
                if pin and pin != identity_thread:
                    raise ValueError("identity and launch thread disagree")
                agent["identityThreadId"] = identity_thread
    owners = {}
    for book in result:
        for agent in book["agents"]:
            pin = agent.get("identityThreadId")
            if pin:
                if pin in owners and owners[pin] != agent["name"]:
                    raise ValueError("thread identity is already bound to another agent")
                owners[pin] = agent["name"]
    return result


def atomic_write(path, data, mode):
    fd, name = tempfile.mkstemp(prefix=".agentbook-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as f:
            os.fchmod(f.fileno(), mode)
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--thread", required=True, help="verified existing server-main thread UUID")
    parser.add_argument("--output", type=Path, default=Path("/srv/blueprint/dist/codex-books"))
    parser.add_argument("--apply", action="store_true", help="back up and update live books; stop blueprint.service first")
    parser.add_argument("--bind", action="append", default=[], metavar="AGENT=THREAD", help="explicitly verified identity binding; never changes launch isolation")
    args = parser.parse_args()
    bindings = dict(value.split("=", 1) for value in args.bind)
    with contextlib.ExitStack() as stack:
        if args.apply:
            for path in sorted(BOOKS):
                lock = stack.enter_context(open(str(path) + ".lock", "a"))
                fcntl.flock(lock, fcntl.LOCK_EX)
        originals = [path.read_bytes() for path in BOOKS]
        updated = migrate([json.loads(data) for data in originals], args.thread, bindings)
        if not args.apply:
            args.output.mkdir(parents=True, exist_ok=True, mode=0o700)
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
        # Back up every source before changing either book. Re-running is safe.
        if args.apply:
            for path, data in zip(BOOKS, originals):
                backup = path.with_name(path.name + ".before-codex-" + stamp)
                with open(backup, "xb") as f:
                    os.fchmod(f.fileno(), 0o600)
                    f.write(data)
                    f.flush()
                    os.fsync(f.fileno())
        for index, (path, book) in enumerate(zip(BOOKS, updated)):
            target = path if args.apply else args.output / (str(index) + "-agentbook.json")
            data = (json.dumps(book, ensure_ascii=False, indent=2) + "\n").encode()
            atomic_write(target, data, path.stat().st_mode & 0o777 if args.apply else 0o600)
            print(f"{'Updated' if args.apply else 'Review copy'}: {target}; {len(book['agents'])} agents")


if __name__ == "__main__":
    main()
