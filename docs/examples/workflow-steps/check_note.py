#!/usr/bin/env python3
import sys


def main():
    with open(sys.argv[1], encoding="utf-8") as source:
        lines = [line.strip() for line in source.read().splitlines() if line.strip()]
    if not lines or not lines[0].startswith("# "):
        print("the note must start with a '# <key>' heading")
        return 1
    if len(lines) < 2:
        print("the note needs at least one sentence after the heading")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
