#!/usr/bin/env python3
import json
import os
import sys


def main():
    with open(sys.argv[1], encoding="utf-8") as source:
        review = json.load(source)
    if not os.path.isfile(sys.argv[2]):
        print("the reviewed note is missing")
        return 2
    if review.get("verdict") not in ("accept", "revise"):
        print('verdict must be "accept" or "revise"')
        return 1
    comments = review.get("comments")
    if not isinstance(comments, list) or not all(isinstance(c, str) for c in comments):
        print("comments must be a list of strings")
        return 1
    if review["verdict"] == "revise":
        if not comments:
            print("a revise verdict needs at least one comment")
            return 1
        # Exit 4 sends the unit back to the code step (send_back in
        # workflow.yaml); stdout becomes .Problems of the coder's redo.
        print("The reviewer asked for changes:")
        for comment in comments:
            print("- " + comment)
        return 4
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
