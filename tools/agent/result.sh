#!/usr/bin/env bash
# ADR 0008 sandbox gate runner. Invoked by `make agent-result` (cwd = repo
# root). Runs every gate in order, streams output, then writes RESULT.json
# beside the repo and prints the completion-signal marker on its own line.
#
# This is dev-time meta-tooling under tools/agent/ (ADR 0008): never compiled
# into nor shipped with the static binary (CONVENTIONS rule 1).
#
# RESULT.json + MARKER replace iteration: the sandcastle runner uses
# maxIterations===1, so it cannot poll. It greps one MARKER line to know the
# loop finished and reads one structured file for the verdict (ADR 0008).
set -u

MARKER="SANDCASTLE_RESULT_COMPLETE"

# sqlc first: internal/store/db/ is gitignored and regenerated, so a clean
# copy-in has no generated code until `make sqlc` runs (CONVENTIONS rule 9).
GATES="sqlc build test lint smoke"

overall=pass
declare -A status
for g in $GATES; do
	echo "=== gate: ${g} ==="
	if make "${g}"; then
		status[$g]=pass
	else
		status[$g]=fail
		overall=fail
	fi
done

commit=$(git rev-parse HEAD 2>/dev/null || echo unknown)
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
ok=false
[ "${overall}" = pass ] && ok=true

cat > RESULT.json <<EOF
{
  "issue": "${ISSUE:-unknown}",
  "branch": "${branch}",
  "commit": "${commit}",
  "gates": {
    "sqlc": "${status[sqlc]}",
    "build": "${status[build]}",
    "test": "${status[test]}",
    "lint": "${status[lint]}",
    "smoke": "${status[smoke]}"
  },
  "ok": ${ok},
  "completed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

echo "${MARKER}"
[ "${overall}" = pass ]
