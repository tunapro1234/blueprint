#!/usr/bin/env python3
import json
import sys


def main():
    with open(sys.argv[1], encoding="utf-8") as source:
        record = json.load(source)
    if not isinstance(record.get("slug"), str) or not record["slug"]:
        print("slug must be a nonempty string")
        return 1
    if not isinstance(record.get("score"), int) or not 0 <= record["score"] <= 100:
        print("score must be an integer from 0 through 100")
        return 1
    if not isinstance(record.get("rationale"), str) or not record["rationale"].strip():
        print("rationale must be a nonempty sentence")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
