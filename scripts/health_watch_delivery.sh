# Source from a Bash health watcher. Log bp's evidence instead of discarding it.
# A nonzero exit with RESULT=unverified is pending evidence, not failed delivery.
bp_health_send() {
  local target="$1" message="$2" log="$3" output rc=0
  output=$(bp msg "$target" "$message" 2>&1) || rc=$?
  printf '[%s] BP target=%s exit=%s\n%s\n' "$(date '+%F %T')" "$target" "$rc" "$output" >> "$log"
  case "${output##*$'\n'}" in
    RESULT=delivered|RESULT=delivered\ CHANNEL=*|RESULT=queued|RESULT=queued\ CHANNEL=*|RESULT=unverified|RESULT=unverified\ CHANNEL=*) return 0 ;;
  esac
  return "$rc"
}
