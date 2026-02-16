"""bp-tunnel CLI."""

import argparse
import json
import os
import sys

from .file_transport import FileTransport, DEFAULT_BASE_DIR
from .message import _rand_id
from .tunnel import Tunnel

_ENV_AGENT = "BP_TUNNEL_AGENT"


def _agent(args) -> str:
    """Resolve agent name from --as flag or BP_TUNNEL_AGENT env var."""
    name = args.as_ or os.environ.get(_ENV_AGENT)
    if not name:
        print(f"error: --as required (or set {_ENV_AGENT})", file=sys.stderr)
        sys.exit(1)
    return name


def _output(args, data: dict) -> None:
    """Print output as JSON or human-readable."""
    if args.json:
        print(json.dumps(data))
    else:
        for k, v in data.items():
            print(f"{k}: {v}")


def cmd_create(args):
    agent = _agent(args)
    name = args.name or _rand_id()
    t = FileTransport(args.base_dir)
    t.create(name, agent)
    if args.json:
        print(json.dumps({"tunnel": name, "agent": agent, "role": "admin"}))
    else:
        print(name)
        print(f"  join: bp-tunnel join {name} --as <agent>", file=sys.stderr)


def cmd_join(args):
    agent = _agent(args)
    t = FileTransport(args.base_dir)
    t.join(args.tunnel, agent)
    m = t.members(args.tunnel)
    if args.json:
        print(json.dumps({"tunnel": args.tunnel, "agent": agent, "members": m}))
    else:
        print(f"joined {args.tunnel} ({len(m)} members)")


def cmd_send(args):
    agent = _agent(args)
    t = FileTransport(args.base_dir)
    tun = Tunnel(args.tunnel, agent, t)
    msg = tun.send(args.message, to=args.to)
    if args.json:
        print(json.dumps({"status": "sent", "id": msg.id, "to": args.to or "broadcast"}))
    else:
        print("ok")


def cmd_recv(args):
    agent = _agent(args)
    t = FileTransport(args.base_dir)
    tun = Tunnel(args.tunnel, agent, t)

    if args.all:
        msgs = tun.receive_all(from_=args.from_)
        if args.json:
            print(json.dumps({"messages": [{"from": m.from_, "payload": m.payload, "ts": m.ts} for m in msgs], "count": len(msgs)}))
        else:
            if not msgs:
                print("[empty]")
            for m in msgs:
                print(f"{m.from_}: {m.payload}")
    elif args.wait:
        msg = tun.wait_for(from_=args.from_, timeout=args.timeout)
        if msg:
            if args.json:
                print(json.dumps({"from": msg.from_, "payload": msg.payload, "ts": msg.ts}))
            else:
                print(f"{msg.from_}: {msg.payload}")
        else:
            if args.json:
                print(json.dumps({"status": "timeout"}))
            else:
                print("[timeout]")
                sys.exit(1)
    else:
        msg = tun.receive(from_=args.from_)
        if msg:
            if args.json:
                print(json.dumps({"from": msg.from_, "payload": msg.payload, "ts": msg.ts}))
            else:
                print(f"{msg.from_}: {msg.payload}")
        else:
            if args.json:
                print(json.dumps({"status": "empty"}))
            else:
                print("[empty]")


def cmd_listen(args):
    agent = _agent(args)
    t = FileTransport(args.base_dir)
    tun = Tunnel(args.tunnel, agent, t)
    if not args.json:
        label = f"listening on {args.tunnel} as {agent}"
        if args.from_:
            label += f" (from {args.from_})"
        print(label, file=sys.stderr)
    try:
        for msg in tun.listen(from_=args.from_):
            if args.json:
                print(json.dumps({"from": msg.from_, "payload": msg.payload, "ts": msg.ts}), flush=True)
            else:
                print(f"{msg.from_}: {msg.payload}", flush=True)
    except KeyboardInterrupt:
        print()


def cmd_promote(args):
    agent = _agent(args)
    t = FileTransport(args.base_dir)
    tun = Tunnel(args.tunnel, agent, t)
    tun.promote(args.agent)
    print(f"{args.agent} is now admin")


def cmd_demote(args):
    agent = _agent(args)
    t = FileTransport(args.base_dir)
    tun = Tunnel(args.tunnel, agent, t)
    tun.demote(args.agent)
    print(f"{args.agent} is no longer admin")


def cmd_leave(args):
    agent = _agent(args)
    t = FileTransport(args.base_dir)
    tun = Tunnel(args.tunnel, agent, t)
    tun.leave()
    print(f"left {args.tunnel}")


def cmd_destroy(args):
    agent = _agent(args)
    t = FileTransport(args.base_dir)
    tun = Tunnel(args.tunnel, agent, t)
    tun.destroy()
    print(f"destroyed {args.tunnel}")


def cmd_info(args):
    t = FileTransport(args.base_dir)
    members = t.members(args.tunnel)
    admins = t.admins(args.tunnel)
    if args.json:
        print(json.dumps({"tunnel": args.tunnel, "members": members, "admins": admins}))
    else:
        print(f"tunnel: {args.tunnel}")
        print(f"members ({len(members)}):")
        for m in members:
            suffix = " [admin]" if m in admins else ""
            print(f"  {m}{suffix}")


def cmd_ls(args):
    t = FileTransport(args.base_dir)
    tunnels = []
    for tid in t.tunnels():
        m = t.members(tid)
        tunnels.append({"id": tid, "members": m, "count": len(m)})
    if args.json:
        print(json.dumps({"tunnels": tunnels}))
    else:
        for tun in tunnels:
            print(f"{tun['id']}  ({tun['count']} members)")


def main():
    # Shared flags for all subcommands
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--json", action="store_true", help="machine-readable JSON output")
    common.add_argument("--base-dir", default=DEFAULT_BASE_DIR)
    common.add_argument("--as", dest="as_", default=None)

    p = argparse.ArgumentParser(
        prog="bp-tunnel",
        description="Inter-agent tunnel messaging",
        epilog=f"Set {_ENV_AGENT} to avoid passing --as every time.",
    )
    p.add_argument("--version", action="version", version=f"%(prog)s {_get_version()}")
    sub = p.add_subparsers(dest="cmd", required=True)

    # create [name] --as agent
    s = sub.add_parser("create", parents=[common], help="Create a new tunnel")
    s.add_argument("name", nargs="?", default=None, help="tunnel name (random if omitted)")

    # join <tunnel> --as agent
    s = sub.add_parser("join", parents=[common], help="Join an existing tunnel")
    s.add_argument("tunnel")

    # send <tunnel> <message> --as agent [--to agent]
    s = sub.add_parser("send", parents=[common], help="Send a message")
    s.add_argument("tunnel")
    s.add_argument("message")
    s.add_argument("--to", default=None, help="direct message to agent")

    # recv <tunnel> --as agent [--from agent] [--all] [--wait] [--timeout N]
    s = sub.add_parser("recv", parents=[common], help="Receive messages")
    s.add_argument("tunnel")
    s.add_argument("--from", dest="from_", default=None, help="filter by sender")
    s.add_argument("--all", action="store_true", help="read all messages")
    s.add_argument("--wait", action="store_true", help="block until a message arrives")
    s.add_argument("--timeout", type=float, default=30, help="timeout for --wait (seconds)")

    # listen <tunnel> --as agent [--from agent]
    s = sub.add_parser("listen", parents=[common], help="Listen for messages (blocking)")
    s.add_argument("tunnel")
    s.add_argument("--from", dest="from_", default=None, help="filter by sender")

    # promote <tunnel> <agent> --as agent
    s = sub.add_parser("promote", parents=[common], help="Promote agent to admin")
    s.add_argument("tunnel")
    s.add_argument("agent")

    # demote <tunnel> <agent> --as agent
    s = sub.add_parser("demote", parents=[common], help="Remove admin from agent")
    s.add_argument("tunnel")
    s.add_argument("agent")

    # leave <tunnel> --as agent
    s = sub.add_parser("leave", parents=[common], help="Leave a tunnel")
    s.add_argument("tunnel")

    # destroy <tunnel> --as agent
    s = sub.add_parser("destroy", parents=[common], help="Delete a tunnel (admin only)")
    s.add_argument("tunnel")

    # info <tunnel>
    s = sub.add_parser("info", parents=[common], help="Show tunnel info")
    s.add_argument("tunnel")

    # ls
    sub.add_parser("ls", parents=[common], help="List all tunnels")

    args = p.parse_args()
    try:
        {
            "create": cmd_create, "join": cmd_join,
            "send": cmd_send, "recv": cmd_recv, "listen": cmd_listen,
            "promote": cmd_promote, "demote": cmd_demote, "leave": cmd_leave,
            "destroy": cmd_destroy, "info": cmd_info, "ls": cmd_ls,
        }[args.cmd](args)
    except (ValueError, PermissionError) as e:
        print(f"error: {e}", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print()


def _get_version() -> str:
    from . import __version__
    return __version__


if __name__ == "__main__":
    main()
