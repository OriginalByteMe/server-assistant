#!/bin/bash
# Orchestrates one dry-run attempt: read-only bind-mounted real filesystem,
# network-less namespace, shims first on PATH, execve tracing, and a verdict
# from predicate.sh. Throwaway prototype — see README.md.
#
# Usage: run.sh <script-to-dry-run> [scenario.env] [timeout-seconds]
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${1:?usage: run.sh <script> [scenario.env] [timeout-seconds]}"
SCRIPT="$(cd "$(dirname "$SCRIPT")" && pwd)/$(basename "$SCRIPT")"
SCENARIO="${2:-}"
TIMEOUT="${3:-15}"

[[ -n "$SCENARIO" ]] && source "$SCENARIO"

NAME="$(basename "$(dirname "$SCRIPT")")-$(date +%s)-$$"
mkdir -p "$HERE/transcripts"
TRANSCRIPT="$HERE/transcripts/${NAME}.log"
STRACE_LOG="$HERE/transcripts/${NAME}.strace"
: > "$TRANSCRIPT"

DRYRUN_SCRATCH="/tmp/dryrun-scratch-$$"
mkdir -p "$DRYRUN_SCRATCH"

echo "=== dry-run: $SCRIPT ===" >> "$TRANSCRIPT"
echo "=== scenario: ${SCENARIO:-<none>} ===" >> "$TRANSCRIPT"
echo "=== started: $(date -Is) ===" >> "$TRANSCRIPT"

REAL_PATH="$PATH"

# fd 3 (transcript) and fd 4 (strace log) are opened HERE, on the host, before
# the mount namespace exists — writes to an already-open fd don't re-check
# path permissions, so they keep working even once / is remounted read-only
# inside the namespace.
exec 3>>"$TRANSCRIPT"
exec 4>>"$STRACE_LOG"

DRYRUN_TRANSCRIPT_FD=3
DRYRUN_SCRATCH="$DRYRUN_SCRATCH"
# any DRYRUN_* scenario vars sourced above (must use `export` in the .env file)
# are already in this shell's environment and inherited below. PATH is set
# only for the unshare child (env VAR=val prefix), never exported into this
# outer shell — this used to leak into the outer shell, which made the
# cleanup `rm -f` below silently resolve to our OWN rm shim instead of the
# real one.
export DRYRUN_TRANSCRIPT_FD DRYRUN_SCRATCH

PATH="$HERE/bin:$REAL_PATH" DRYRUN_FIXTURE_MOUNTS="${DRYRUN_FIXTURE_MOUNTS:-}" unshare --user --map-root-user --mount --net --fork -- bash -c '
    set -uo pipefail
    ip link set lo up >/dev/null 2>&1
    # optional fixture mounts (e.g. a real Unraid path this dev box lacks,
    # such as /mnt/user, pointed at a local fixture dir) — applied while the
    # tree is still writable, then locked read-only along with everything else.
    IFS=";" read -ra _pairs <<< "${DRYRUN_FIXTURE_MOUNTS:-}"
    for _pair in "${_pairs[@]}"; do
      [ -z "$_pair" ] && continue
      _dest="${_pair%%:*}"; _src="${_pair#*:}"
      # the destination'\''s parent (e.g. /mnt) is usually root-owned on a
      # non-Unraid dev box; an unprivileged mount namespace may still mount a
      # *fresh filesystem* over an accessible mountpoint even though it can'\''t
      # write into the existing one, so tmpfs it first, then it'\''s ours to mkdir in.
      mount -t tmpfs tmpfs "$(dirname "$_dest")" 2>/dev/null
      mkdir -p "$_dest"
      mount --bind "$_src" "$_dest"
    done
    mount --bind / / 2>/dev/null
    mount -o remount,bind,ro / 2>/dev/null
    cd /
    if command -v strace >/dev/null 2>&1; then
      exec timeout -k 2 '"$TIMEOUT"' strace -f -qq -e trace=execve -o /proc/self/fd/4 bash '"$(printf '%q' "$SCRIPT")"'
    else
      exec timeout -k 2 '"$TIMEOUT"' bash '"$(printf '%q' "$SCRIPT")"'
    fi
  ' 3>&3 4>&4
RC=$?
exec 3>&-

{
  echo "=== finished: $(date -Is) ==="
  echo "=== script exit code: $RC (124 = killed by timeout after ${TIMEOUT}s) ==="
} >> "$TRANSCRIPT"

echo "--- transcript: $TRANSCRIPT ---"
cat "$TRANSCRIPT"
echo "---"
"$HERE/predicate.sh" "$TRANSCRIPT" "$STRACE_LOG" "$RC" "$HERE/bin"
