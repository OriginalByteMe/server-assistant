#!/bin/bash
# Evaluates the mechanical "cannot complete a dry run" predicate against one
# run's transcript + execve trace. See FINDINGS.md "The rejection predicate"
# for the prose version of exactly what this checks.
#
# REJECTED iff any of:
#   (a) the script's own exit code is nonzero, OR it never terminated
#       (timeout, exit 124) — a dry run that doesn't finish cannot be evidence
#       of anything.
#   (b) an execve() happened against a binary that resolves outside the shim
#       directory AND is not on the fixed read-only-safe allowlist
#       (safe-readonly.list) — i.e. a mutation-risk binary this shim set does
#       not know about was invoked for real.
#   (c) a shimmed mutating call's target path resolved outside the sandbox
#       scratch scope (or under /boot) — logged by the shims themselves as a
#       VIOLATION line.
#
# Usage: predicate.sh <transcript> <strace-log-or-empty> <exit-code> <bindir>
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TRANSCRIPT="$1"
STRACE="$2"
RC="$3"
BINDIR="$4"

reasons=()

# --- branch (a): exit status ---
if [[ "$RC" == "124" ]]; then
  reasons+=("(a) did not terminate within the timeout (script was killed) — treated as a nonzero-exit rejection: a dry run that never finishes proves nothing")
elif [[ "$RC" != "0" ]]; then
  reasons+=("(a) script exited nonzero: $RC")
fi

# --- branch (b): unshimmed mutation-risk binary ---
if [[ -s "$STRACE" ]]; then
  mapfile -t exec_paths < <(grep -oP 'execve\("\K[^"]+' "$STRACE" | sort -u)
  mapfile -t safe < "$HERE/safe-readonly.list"
  mapfile -t mutating < "$HERE/mutating.list"
  for p in "${exec_paths[@]}"; do
    base="$(basename "$p")"
    is_shim=0; is_safe=0
    [[ "$p" == "$BINDIR"/* ]] && is_shim=1
    for s in "${safe[@]}"; do
      [[ -z "$s" || "$s" == \#* ]] && continue
      [[ "$base" == "$s" ]] && { is_safe=1; break; }
    done
    if [[ $is_shim -eq 0 && $is_safe -eq 0 ]]; then
      is_mutating_name=0
      for m in "${mutating[@]}"; do [[ "$base" == "$m" ]] && is_mutating_name=1; done
      if [[ $is_mutating_name -eq 1 ]]; then
        reasons+=("(b) shimmed command '$base' was invoked via a path outside the shim dir ($p) — PATH-shim bypass")
      else
        reasons+=("(b) unshimmed binary invoked: $p (not on safe-readonly.list, not a known-mutating shim)")
      fi
    fi
  done
fi

# --- branch (c): write attempts outside sandbox scope ---
if [[ -f "$TRANSCRIPT" ]]; then
  while IFS= read -r line; do
    reasons+=("(c) $line")
  done < <(grep '^\[.*VIOLATION' "$TRANSCRIPT")
fi

if [[ ${#reasons[@]} -eq 0 ]]; then
  echo "VERDICT: APPROVED-FOR-REVIEW — dry run completed cleanly, no violation, exit $RC. Evidence, not proof: see FINDINGS.md honest-ceiling section."
  exit 0
else
  echo "VERDICT: REJECTED — cannot complete a dry run"
  for r in "${reasons[@]}"; do
    echo "  - $r"
  done
  exit 1
fi
