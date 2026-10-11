#!/usr/bin/env bash
# console_smoke.sh — end-to-end evidence that `bp console` serves the fleet
# console: the token-injected SPA shell, the real Vite asset bundle, and the
# /v1 JSON endpoints (fleet, tasks, workflows, usage).
#
# It runs against a throwaway BP_HOME and an isolated tmux environment, so it
# never reads or writes a live fleet. The console is read-only over live state,
# so an empty isolated home yields empty-but-valid responses, which is exactly
# what proves the wiring without needing a seeded book.
#
# Usage:
#   scripts/console_smoke.sh            # builds bp from the current tree
#   BP_BIN=/path/to/bp scripts/console_smoke.sh   # uses an existing binary
#
# Exit code 0 means every check passed; the evidence is printed to stdout.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/bp-console-smoke.XXXXXX")"
server_pid=""
cleanup() {
  [ -n "$server_pid" ] && kill "$server_pid" 2>/dev/null || true
  rm -rf "$work" 2>/dev/null || true
}
trap cleanup EXIT

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

# 1. Resolve the bp binary (build from this tree unless BP_BIN is given).
bp="${BP_BIN:-}"
if [ -z "$bp" ]; then
  bp="$work/bp"
  echo "building bp -> $bp (nice, GOMAXPROCS=${GOMAXPROCS:-2})"
  ( cd "$root" && GOMAXPROCS="${GOMAXPROCS:-2}" nice -n 15 go build -o "$bp" ./cmd/bp )
fi
[ -x "$bp" ] || fail "bp binary not executable: $bp"

# 2. Fully isolated environment: throwaway home, no inherited tmux/agent vars,
#    a short tmux socket dir, and no update checks.
export HOME="$work/home"
export BP_HOME="$work/home/.blueprint"
export TMUX_TMPDIR="$work/tmux"
export BP_NO_UPDATE_CHECK=1
mkdir -p "$BP_HOME" "$TMUX_TMPDIR"
unset TMUX TMUX_PANE AGENT CODEX_THREAD_ID BP_SESSION 2>/dev/null || true

# 3. Start the console on an OS-assigned loopback port; parse the printed URL.
log="$work/console.log"
"$bp" console --port 0 --no-open >"$log" 2>&1 &
server_pid=$!

url=""
for _ in $(seq 1 100); do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    echo "--- console output ---"; cat "$log"; fail "bp console exited before serving"
  fi
  url="$(grep -oE 'http://127\.0\.0\.1:[0-9]+' "$log" | head -1 || true)"
  [ -n "$url" ] && break
  sleep 0.1
done
[ -n "$url" ] || { echo "--- console output ---"; cat "$log"; fail "console never printed its URL"; }
echo "console serving at $url"

c() { curl -sS -m 10 "$@"; }
status() { c -o /dev/null -w '%{http_code}' "$1"; }

# 4. The SPA shell: 200, token injected (empty, not the raw placeholder), root div.
echo; echo "== GET / =="
root_body="$(c "$url/")"
root_code="$(status "$url/")"
[ "$root_code" = 200 ] || fail "GET / status $root_code"
case "$root_body" in
  *'__BP_CONSOLE_TOKEN__'*) fail "GET / still served the raw token placeholder" ;;
esac
case "$root_body" in
  *'window.__BP_TOKEN__ = "";'*) : ;;
  *) fail "GET / did not inject an empty token" ;;
esac
case "$root_body" in
  *'<div id="root">'*) : ;;
  *) fail "GET / missing the SPA root element" ;;
esac
echo "  200, token injected empty, SPA root present"
echo "$root_body" | grep -nE 'window.__BP_TOKEN__|<div id="root"|index-.*\.js' | sed 's/^/    /'

# 5. The real Vite bundle (not the 700B placeholder): 200, JS content-type, big.
echo; echo "== GET the hashed JS asset =="
asset_rel="assets/$(basename "$(ls "$root"/ui/console/dist/assets/*.js | head -1)")"
asset_code="$(status "$url/$asset_rel")"
asset_ct="$(c -o /dev/null -w '%{content_type}' "$url/$asset_rel")"
asset_len="$(c -o /dev/null -w '%{size_download}' "$url/$asset_rel")"
[ "$asset_code" = 200 ] || fail "GET /$asset_rel status $asset_code"
case "$asset_ct" in *javascript*) : ;; *) fail "asset content-type '$asset_ct' not javascript" ;; esac
[ "$asset_len" -gt 100000 ] || fail "asset only $asset_len bytes — looks like the placeholder, not a real bundle"
echo "  /$asset_rel -> 200, $asset_ct, $asset_len bytes"

# 6. The /v1 JSON endpoints: 200 and parseable JSON (empty is fine in isolation).
json_ok() {
  local path="$1" code body
  code="$(status "$url$path")"
  [ "$code" = 200 ] || fail "GET $path status $code"
  body="$(c "$url$path")"
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "$body" | python3 -c 'import sys,json; json.load(sys.stdin)' \
      || fail "GET $path did not return valid JSON: $body"
  else
    case "$body" in [\{\[]*) : ;; *) fail "GET $path not JSON: $body" ;; esac
  fi
  echo "  $path -> 200, valid JSON: $(printf '%s' "$body" | head -c 160)"
}
echo; echo "== GET /v1 endpoints =="
json_ok "/v1/fleet/tree"
json_ok "/v1/tasks"
json_ok "/v1/workflows"
json_ok "/v1/usage/accounts"

echo
echo "SMOKE PASS: bp console serves the SPA shell, the real Vite bundle, and all /v1 endpoints."
