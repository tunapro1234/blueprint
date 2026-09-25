#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
socket_tmp="$(mktemp -d)"
trap 'rm -rf "$tmp" "$socket_tmp"' EXIT
mkdir -p "$tmp/bin"
cat >"$tmp/bin/bp" <<'SH'
#!/usr/bin/env bash
if [ "$1" = book ]; then
  printf '{"agents":[]}\n'
  exit 0
fi
if [ "$1" = p2p ] && [ "$2" = lookup ]; then
  case "${BP_LOOKUP_MODE:-}" in
    hit)
      printf "tip: '%s' exists on peer laptop (live) — attach it on that machine\n" "$3"
      exit 0
      ;;
    miss) exit 1 ;;
    unknown)
      printf 'unknown command: p2p lookup\n' >&2
      exit 2
      ;;
    hang) exec sleep 10 ;;
  esac
fi
exit 64
SH
cat >"$tmp/bin/tmux" <<'SH'
#!/usr/bin/env sh
exit 1
SH
chmod +x "$tmp/bin/bp" "$tmp/bin/tmux"

run_case() {
  local mode="$1" rc=0
  if env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$socket_tmp" BP_LOOKUP_MODE="$mode" PATH="$tmp/bin:$PATH" \
      "$repo_root/scripts/bp-attach" sample-agent >"$tmp/out" 2>"$tmp/err"; then
    rc=0
  else
    rc=$?
  fi
  [ "$rc" -eq 1 ] || { printf 'bp-attach exit for %s was %s\n' "$mode" "$rc" >&2; return 1; }
}

run_case hit
grep -qF "tip: 'sample-agent' exists on peer laptop (live)" "$tmp/err"

run_case miss
! grep -qF 'tip:' "$tmp/err"

run_case unknown
! grep -qF 'tip:' "$tmp/err"

started="$(date +%s%N)"
run_case hang
elapsed_ms=$(( ($(date +%s%N) - started) / 1000000 ))
[ "$elapsed_ms" -lt 3800 ] || { printf 'lookup hang took %s ms\n' "$elapsed_ms" >&2; exit 1; }
! grep -qF 'tip:' "$tmp/err"

printf 'bp-attach lookup shell test: PASS (hang %s ms)\n' "$elapsed_ms"
