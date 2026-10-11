#!/usr/bin/env bash
# console_render.sh — render proof for `bp console`. Serves the console over a
# seeded, throwaway BP_HOME and runs the headless render check, producing
# screenshots and DOM evidence that the Org / Tasks / Usage / Workflows pages
# actually render. It is pure verification: the seeded home is a temp dir, so no
# live fleet is read or written.
#
# Usage:  scripts/console_render.sh [out-dir]
#   BP_BIN=/path/to/bp   use an existing binary instead of building
#   PW_CHROMIUM=/path     system Chromium for Playwright, if it ships none
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="${1:-$root/console-shots}"
work="$(mktemp -d "${TMPDIR:-/tmp}/bp-console-render.XXXXXX")"
server_pid=""
cleanup() {
  [ -n "$server_pid" ] && kill "$server_pid" 2>/dev/null || true
  rm -rf "$work" 2>/dev/null || true
}
trap cleanup EXIT
fail() { echo "RENDER WRAP FAIL: $*" >&2; exit 1; }

bp="${BP_BIN:-}"
if [ -z "$bp" ]; then
  bp="$work/bp"
  echo "building bp -> $bp (nice, GOMAXPROCS=${GOMAXPROCS:-2})"
  ( cd "$root" && GOMAXPROCS="${GOMAXPROCS:-2}" nice -n 15 go build -o "$bp" ./cmd/bp )
fi
[ -x "$bp" ] || fail "no bp binary at $bp"
mkdir -p "$out"

# Isolated throwaway home; AGENTBOOK points the fleet at our seed file.
export HOME="$work/home"
export BP_HOME="$work/home/.blueprint"
export TMUX_TMPDIR="$work/tmux"
export BP_NO_UPDATE_CHECK=1
export AGENTBOOK="$BP_HOME/book.json"
mkdir -p "$BP_HOME" "$TMUX_TMPDIR"
unset TMUX TMUX_PANE AGENT CODEX_THREAD_ID BP_SESSION 2>/dev/null || true

# Seed a 3-level hierarchy so Org draws a real tree with reporting edges.
cat > "$AGENTBOOK" <<'JSON'
{
  "orchestrator": "server-main",
  "agents": [
    {"name": "server-main", "role": "orchestrator"},
    {"name": "blueprint", "parent": "server-main", "role": "bp infrastructure"},
    {"name": "probot-egitim", "parent": "server-main", "role": "curriculum lead"},
    {"name": "probot-egitim-hat1", "parent": "probot-egitim", "role": "unit worker"},
    {"name": "probot-egitim-hat2", "parent": "probot-egitim", "role": "unit worker"},
    {"name": "akbank-main", "parent": "server-main", "role": "jury lead"}
  ]
}
JSON

# Seed the usage/cost view: an owner pricing block (config.json), four Team
# premium seats and a token store, so the Usage page renders its full cost strip
# (subscription, "if it were API" spend, value ratio) rather than empty panels.
# This writes config.json first, so the CLI seeds below use the same stateDir.
node "$root/scripts/console_render_seed.mjs" "$BP_HOME" "$BP_HOME/state" || echo "note: usage seed skipped"

# Seed a couple of tasks (CLI writes <StateDir>/tasks/*.json; no daemon needed).
"$bp" task add "Ship fleet console MVP" --owner blueprint >/dev/null 2>&1 || echo "note: task seed 1 skipped"
"$bp" task add "Wire usage token calc" --owner server-main >/dev/null 2>&1 || echo "note: task seed 2 skipped"

# Seed one workflow (best-effort: if the schema drifts, the Workflows page just
# renders its empty-state, which is still a valid render).
wfdir="$work/wf"
mkdir -p "$wfdir"
cat > "$wfdir/workflow.yaml" <<'YAML'
name: egitim-supervised
description: Multi-agent supervised curriculum build
units:
  file: units.json
prompt:
  template: "Build curriculum unit {{.key}} with peer review."
YAML
echo '[{"key":"unit-1"},{"key":"unit-2"}]' > "$wfdir/units.json"
"$bp" workflow add "$wfdir" >/dev/null 2>&1 || echo "note: workflow seed skipped (schema drift; page shows empty-state)"

# Serve on an OS-assigned loopback port and parse the printed URL.
log="$work/console.log"
"$bp" console --port 0 --no-open >"$log" 2>&1 &
server_pid=$!
url=""
for _ in $(seq 1 100); do
  kill -0 "$server_pid" 2>/dev/null || { echo "--- console log ---"; cat "$log"; fail "console exited early"; }
  url="$(grep -oE 'http://127\.0\.0\.1:[0-9]+' "$log" | head -1 || true)"
  [ -n "$url" ] && break
  sleep 0.1
done
[ -n "$url" ] || { echo "--- console log ---"; cat "$log"; fail "console never printed its URL"; }
echo "console serving (seeded home) at $url — rendering to $out"

node "$root/scripts/console_render_check.mjs" "$url" "$out"
