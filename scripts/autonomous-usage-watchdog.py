#!/usr/bin/env python3
"""Review candidate: live activity from bp schema 2; timestamp-window usage.

No transcript-directory-to-agent inference. Unknown/stale/incomplete observations
cannot trigger a current-work alarm. Existing delivery behavior is preserved;
this file has NOT replaced the live /srv/server-main/bin script.
Claude weighted thresholds remain; Codex raw totals need their own approved
threshold before becoming notifications. bp_runtime_usage exposes both providers.
"""
import datetime as dt
import json
import os
import shlex
import subprocess
import sys
import time

from bp_runtime_usage import inspect


# These external paths are retained because the live service already reads them.
STATE_FILE = "/srv/server-main/.otonom-bekci.json"
TARGET = "905380408148@s.whatsapp.net"  # Tuna's own chat; an undefined --from
BRIDGE_LOG = "/srv/whatsapp/bridge.log"  # requires an explicit target (otherwise to=null)
PROJECTS = "/root/.claude/projects"

# Two levels. One threshold is misleading: an agent may make one 900k turn or
# two hundred small turns.
CRITICAL_TOKENS = 20_000_000  # this much since the latest human input means a runaway
CRITICAL_GOALS_6H = 6         # this many goal triggers in 6h suggests a self-restarting loop
WARNING_TOKENS = 8_000_000    # this much plus a long period without human input
WARNING_HOURS = 24
COOLDOWN_HOURS = 6            # wait this long before warning again for the same agent
FRESHNESS_HOURS = 12          # event-time usage window, not mtime


def read_state():
    try:
        with open(STATE_FILE) as state_file:
            return json.load(state_file)
    except Exception:
        return {}


def main():
    # Keep accepting --kuru because deployed copies and operator habits may use it.
    dry_run = "--dry-run" in sys.argv or "--kuru" in sys.argv
    now = dt.datetime.now(dt.timezone.utc)
    state = read_state()
    # bp owns agent/thread identity and live activity. No cwd basename guesses.
    try:
        report = json.loads(subprocess.check_output(
            ["/usr/local/bin/bp", "status", "--json"], text=True, timeout=30))
        now = dt.datetime.now(dt.timezone.utc)
        observations = inspect(report, now, FRESHNESS_HOURS)
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        print(f"watchdog runtime data is unknown: {exc}", file=sys.stderr)
        return 1

    findings = []
    inspected = len(observations)
    skipped = sum(not row["running"] for row in observations)
    incomplete = sum(not row.get("usage", {}).get("complete", False) for row in observations)
    for row in observations:
        if not row["can_alert"]:
            continue
        usage = row["usage"]
        # Existing weighted thresholds apply only to Claude. Codex token totals
        # are a different unit: expose them in bp, never apply a Claude budget.
        if not usage["weighted_units"]:
            continue
        weighted_units, goals_6h = usage["weighted_units"], usage["goal_6h"]
        human_age = row.get("last_human_age_seconds")
        hours = human_age / 3600 if human_age is not None else 0
        if goals_6h >= CRITICAL_GOALS_6H or weighted_units >= CRITICAL_TOKENS:
            level = "CRITICAL"
        elif weighted_units >= WARNING_TOKENS and human_age is not None and hours >= WARNING_HOURS:
            level = "warning"
        else:
            continue
        findings.append(dict(agent=row["agent"], weighted_units=weighted_units,
                             turns=usage["messages"], goals=goals_6h,
                             goals_6h=goals_6h, hours=hours, condition="",
                             level=level, uncertain=human_age is None))

    if not dry_run:
        # Heartbeat: distinguish "no warning" from "watchdog died". health-watch
        # checks this file's freshness. (The same lesson applies to the duplicate
        # WhatsApp report timer.)
        try:
            # Retain the deployed external filename for service compatibility.
            open("/srv/server-main/.otonom-bekci.kalp", "w").write(str(int(time.time())))
        except OSError:
            pass

    coverage = (f"coverage: {inspected} real agents; {skipped} inactive/unknown; "
                f"{incomplete} incomplete/unmatched measurements. Usage window: "
                f"the latest {FRESHNESS_HOURS} hours; active work and historical "
                "usage were checked separately")

    if not dry_run:
        # Retain the deployed external filename for service compatibility.
        with open("/srv/server-main/otonom-bekci.log", "a") as log_file:
            log_file.write(f"[{dt.datetime.now().isoformat(timespec='seconds')}] "
                           f"{len(findings)} findings · {coverage}\n")

    if not findings:
        if dry_run:
            print(f"no threshold was exceeded in active, matched observations\n{coverage}")
        return

    findings.sort(key=lambda finding: -finding["weighted_units"])
    to_send = []
    for finding in findings:
        previous = state.get(finding["agent"], {})
        latest = previous.get("ts", 0)
        # The persisted `yakit` key is retained for compatibility with state
        # written and read by older deployed copies of this script.
        previous_usage = previous.get("yakit", 0)
        if (time.time() - latest < COOLDOWN_HOURS * 3600
                and finding["weighted_units"] < previous_usage * 2):
            continue
        to_send.append(finding)

    if not to_send:
        if dry_run:
            print("threshold exceeded, but every finding is in its cooldown window:",
                  ", ".join(finding["agent"] for finding in findings))
        return

    # Put the measurement time first: this warning may wait in the queue and be
    # delivered STALE, so its turn/token counts may no longer be current.
    lines = [f"⚠️ AUTONOMOUS USAGE [autonomous-watchdog] (measured: "
             f"{now.astimezone().strftime('%d %b %H:%M')}) "
             f"— time-windowed usage for active agents:", ""]
    for finding in to_send:
        name = finding["agent"]
        duration = ("unknown" if finding["uncertain"]
                    else f"{finding['hours']:.0f} hours since")
        marker = "🔴" if finding["level"] == "CRITICAL" else "•"
        lines.append(f"{marker} {name}: {duration} latest user input, "
                     f"{finding['turns']} assistant messages, "
                     f"~{finding['weighted_units']/1e6:.0f}M weighted units in "
                     f"the latest {FRESHNESS_HOURS} hours")
        if finding["goals_6h"]:
            lines.append(f"  {finding['goals_6h']} goal_status records in the "
                         "latest 6 hours; not proof of a restart by itself")
        else:
            lines.append("  this measurement does not identify the trigger")

    lines += ["", "To inspect, write to server-main: 'stop <name>'."]
    message = "\n".join(lines)

    if dry_run:
        print(message)
        return

    # DELIVERY VERIFICATION: rc=0 from bp does not prove that the message was
    # delivered. An early test was quarantined because it lacked a target, but
    # the script recorded a warning and started its cooldown anyway. A real
    # runaway could therefore have been swallowed silently. Always provide the
    # target explicitly and verify bridge output in its log.
    # shlex.quote, not json.dumps: json.dumps converts non-ASCII characters to
    # \uXXXX escapes, which arrive in WhatsApp as a pile of backslashes.
    # --from server-main must name a reachable identity. The earlier
    # autonomous-watchdog signature had no tmux session, so failure notices were
    # queued forever to a nonexistent target. Reachability matters more than a
    # familiar signature, which now appears in the message body instead.
    rc = os.system(f"/usr/local/bin/bp wa send --to {TARGET} --from server-main "
                   + shlex.quote(message))
    delivered = False
    if rc == 0:
        for _ in range(10):
            time.sleep(1)
            try:
                with open(BRIDGE_LOG, errors="replace") as bridge_file:
                    tail = bridge_file.readlines()[-40:]
            except OSError:
                break
            # Older bridge versions emit Turkish status tokens; accept both.
            if any(("SENT" in line or "GONDERILDI" in line)
                   and ("AUTONOMOUS USAGE" in line or "OTONOM YANMA" in line)
                   for line in tail):
                delivered = True
                break
            if any(("QUARANTINED" in line or "KARANTINA" in line)
                   and ("autonomous-watchdog" in line or "otonom-bekci" in line)
                   for line in tail):
                break
    if not delivered:
        # Retain the deployed external filename for service compatibility.
        with open("/srv/server-main/otonom-bekci.log", "a") as log_file:
            log_file.write(f"[{dt.datetime.now().isoformat(timespec='seconds')}] "
                           f"WARNING NOT DELIVERED (rc={rc}) — state not written; "
                           "the next run will retry\n")
        sys.exit(1)

    for finding in to_send:
        # `yakit` is a persisted compatibility field read by older copies.
        state[finding["agent"]] = {"ts": time.time(), "yakit": finding["weighted_units"]}
    temp_file = STATE_FILE + ".tmp"
    with open(temp_file, "w") as state_file:
        json.dump(state, state_file)
    os.replace(temp_file, STATE_FILE)


if __name__ == "__main__":
    sys.exit(main() or 0)
