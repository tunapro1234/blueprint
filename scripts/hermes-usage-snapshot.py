#!/usr/bin/env python3
"""Append one Hermes usage snapshot to state/hermes-usage.jsonl.

WHY THIS EXISTS. The two halves of Hermes' cost are measured in places with
different memories:

  - Token volume lives in ~/.hermes/state.db (session_model_usage). It
    ACCUMULATES, so it can be read retroactively.
  - The dollar figure comes from OpenRouter's /api/v1/key, whose usage_daily
    counter RESETS at midnight UTC. Nobody was reading it, so every day's real
    spend was being lost at 00:00 — the 22 Aug figure (1.0486 USD) survives
    only because it was queried by hand that evening.

One snapshot an hour keeps both halves on disk, which is what turns the
2026-08-22 budget page (docs/hermes-model-butce-2026-08.md, N=1) into a
series. That page's biggest caveat is that it rests on a single day of a
single workload; this file is how that stops being true.

It is a READER. It opens state.db read-only, sends no message, changes no
config, and never touches an agent. A failure here must never be able to
affect delivery, so every error is caught and written into the snapshot line
instead of raised.
"""

import json
import os
import sqlite3
import time
import urllib.request

STATE_DB = os.path.expanduser("~/.hermes/state.db")
ENV_FILE = os.path.expanduser("~/.hermes/.env")
OUT = "/srv/blueprint/state/hermes-usage.jsonl"
KEY_URL = "https://openrouter.ai/api/v1/key"


def totals(group_by):
    """Cumulative counters from Hermes' own accounting, grouped by one column.
    input_tokens EXCLUDES cache; cache_read_tokens is separate (verified
    against a bg-review record on 2026-08-22: 7746 + 47360 = 55106 prompt
    tokens)."""
    if not os.path.exists(STATE_DB):
        return {"error": "state.db yok"}
    connection = sqlite3.connect(f"file:{STATE_DB}?mode=ro", uri=True)
    try:
        rows = connection.execute(
            f"select {group_by}, sum(api_call_count), sum(input_tokens),"
            " sum(cache_read_tokens), sum(output_tokens)"
            f" from session_model_usage group by {group_by}"
        ).fetchall()
    finally:
        connection.close()
    return {
        key or "(bilinmiyor)": {
            "calls": calls or 0,
            "fresh_in": fresh or 0,
            "cache_read": cached or 0,
            "out": out or 0,
        }
        for key, calls, fresh, cached, out in rows
    }


def openrouter_usage():
    """The counter that forgets. usage_daily resets at midnight UTC, so this
    value is only ever available on the day it describes."""
    key = ""
    try:
        with open(ENV_FILE, encoding="utf-8") as handle:
            for line in handle:
                if line.startswith("OPENROUTER_API_KEY="):
                    key = line.split("=", 1)[1].strip()
                    break
    except OSError as error:
        return {"error": f".env okunamadi: {error}"}
    if not key:
        return {"error": "anahtar yok"}
    request = urllib.request.Request(KEY_URL, headers={"Authorization": f"Bearer {key}"})
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            data = json.load(response)["data"]
    except Exception as error:  # noqa: BLE001 - a watchdog never raises
        return {"error": str(error)}
    return {
        field: data.get(field)
        for field in ("usage_daily", "usage_weekly", "usage_monthly", "usage", "limit", "limit_remaining")
    }


def main():
    # Per-SESSION as well as per-model. The model view alone could not answer
    # the first real question asked of this data (2026-08-23): "what did
    # salvo #1 cost, separately from salvo #2?" Hermes panes are long-lived —
    # probot-outreach ran a second day of work inside the SAME eight sessions
    # opened on 22 Aug — so a per-model total attributes a whole day to one
    # undifferentiated number, and hourly diffs cannot separate two salvos that
    # share an hour. Session rows make attribution possible whenever the work
    # is split across panes, which is how this fleet actually runs it.
    snapshot = {
        "ts": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "models": totals("model"),
        "sessions": totals("session_id"),
        "openrouter": openrouter_usage(),
    }
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(snapshot, ensure_ascii=False) + "\n")
    print(json.dumps(snapshot, ensure_ascii=False))


if __name__ == "__main__":
    main()
