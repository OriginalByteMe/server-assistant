#!/bin/bash
# ponytail: one engine, thin per-command wrappers exec this with $1=command name.
# Not production code — see prototypes/dry-run/README.md.
#
# Every shimmed binary funnels through here. It:
#  1. logs the exact argv it was called with to the real transcript file (via an
#     fd opened by run.sh BEFORE the mount namespace went read-only, so logging
#     keeps working even though / is read-only inside the sandbox);
#  2. classifies whether the call's target path(s) fall inside the allowed
#     scratch scope or would touch /boot or anything else on the real host;
#  3. returns a canned exit status (0 unless overridden by a
#     DRYRUN_EXITCODE_<CMD> env var) — it NEVER performs the real operation.

CMD="$1"; shift
FD="${DRYRUN_TRANSCRIPT_FD:-1}"
SCRATCH="${DRYRUN_SCRATCH:-/nonexistent-scratch}"

quote_argv() {
  local out="" a
  for a in "$@"; do
    out+=" $(printf '%q' "$a")"
  done
  echo "$out"
}

log_line() {
  local ts
  ts="$(date '+%Y-%m-%dT%H:%M:%S.%N%z')"
  printf '[%s] %s CMD %s%s\n' "$ts" "$$" "$CMD" "$(quote_argv "$@")" >&"$FD"
}

violation() {
  local target="$1" reason="$2"
  printf '[%s] %s VIOLATION cmd=%s target=%s reason=%s\n' \
    "$(date '+%Y-%m-%dT%H:%M:%S.%N%z')" "$$" "$CMD" "$target" "$reason" >&"$FD"
}
# Which argv positions are "write targets" varies by command. This is a textual
# heuristic (documented limitation: `cp -t DEST a b` or indirect paths built by
# the script at runtime are exactly what it sees post-expansion, so those are
# fine; a flag form it doesn't special-case, e.g. `cp -t`, is not).
extract_targets() {
  local targets=()
  case "$CMD" in
    dd)
      local a
      for a in "$@"; do
        [[ "$a" == of=* ]] && targets+=("${a#of=}")
      done
      ;;
    mv|cp|ln|rsync)
      local last=""
      local a
      for a in "$@"; do
        [[ "$a" != -* ]] && last="$a"
      done
      [[ -n "$last" ]] && targets+=("$last")
      ;;
    rm|tee|truncate|mkfs)
      local a
      for a in "$@"; do
        [[ "$a" != -* ]] && targets+=("$a")
      done
      ;;
    chmod|chown)
      local first=1 a
      for a in "$@"; do
        [[ "$a" == -* ]] && continue
        if [[ $first -eq 1 ]]; then first=0; continue; fi
        targets+=("$a")
      done
      ;;
  esac
  printf '%s\n' "${targets[@]}"
}

check_targets() {
  local t resolved
  while IFS= read -r t; do
    [[ -z "$t" ]] && continue
    resolved=$(realpath -m -- "$t" 2>/dev/null || echo "$t")
    if [[ "$resolved" == /boot* ]]; then
      violation "$resolved" "boot-write-forbidden"
    elif [[ "$resolved" != "$SCRATCH"* ]]; then
      violation "$resolved" "outside-sandbox-scope"
    fi
  done < <(extract_targets "$@")
}

log_line "$@"
check_targets "$@"

var="DRYRUN_EXITCODE_$(echo "$CMD" | tr '[:lower:]' '[:upper:]')"
exit "${!var:-0}"
