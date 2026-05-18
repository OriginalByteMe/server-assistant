#!/usr/bin/env bash
#
# ADR 0008 agent-dev sandbox: copy-in / branch-out.
#
# Runs one issue's agent work inside a disposable, OFFLINE, local container,
# then copies ONLY a git branch (as a bundle) plus RESULT.json back out for
# human review. The host working tree is never mutated and auto-merge never
# happens (ADR 0008; CONVENTIONS treats changes as deliberate decisions).
#
#   tools/agent/run.sh [--rebuild] [--keep] [--branch NAME] [ISSUE]
#
#     --rebuild     rebuild the image first (the ONE network step; do this
#                   when the CONVENTIONS dep table or a pinned tool changes)
#     --keep        leave the container up for post-mortem (else removed)
#     --branch NAME work branch the agent commits on (default: agent/<ISSUE>)
#     ISSUE         issue id, e.g. ARK-20 (default: unknown)
#
# The agent command is a hook: export AGENT_CMD="<cmd run inside the repo>".
# Unset = bootstrap/self-test mode: no agent, just the offline gate proof
# (this is the only mode possible for ARK-20 itself — the harness cannot run
# inside the not-yet-existing sandbox; ADR 0008 bootstrap caveat).
#
# Dev-time meta-tooling under tools/agent/. Never compiled into nor shipped
# with the static binary (CONVENTIONS rule 1; ADR 0008).

set -euo pipefail

IMAGE="server-assistant-sandbox:latest"

# Container runtime is an indirection, not a hardcode, so the sandcastle skill
# (ARK-21) can PREFER Docker Desktop (DOCKER="docker --context desktop-linux")
# or an explicitly configured fallback. It is read as words so a multi-flag
# value (the Docker Desktop context) survives. Default stays plain `docker`.
# This never enables a host run — the agent only ever runs inside the
# disposable container this script creates (ADR 0008).
read -r -a DOCKER <<< "${DOCKER:-docker}"

REBUILD=0
KEEP=0
BRANCH=""
ISSUE="unknown"

while [ $# -gt 0 ]; do
	case "$1" in
		--rebuild) REBUILD=1 ;;
		--keep)    KEEP=1 ;;
		--branch)  BRANCH="${2:?--branch needs a value}"; shift ;;
		--*)       echo "unknown flag: $1" >&2; exit 2 ;;
		*)         ISSUE="$1" ;;
	esac
	shift
done
[ -n "${BRANCH}" ] || BRANCH="agent/${ISSUE}"

REPO="$(git rev-parse --show-toplevel)"
cd "${REPO}"

STAMP="$(date -u +%Y%m%d-%H%M%S)"
OUT="${REPO}/tools/agent/out/${ISSUE}-${STAMP}"
STAGING="$(mktemp -d)"
CID=""

cleanup() {
	[ -n "${CID}" ] && [ "${KEEP}" -eq 0 ] && "${DOCKER[@]}" rm -f "${CID}" >/dev/null 2>&1 || true
	[ -n "${CID}" ] && [ "${KEEP}" -eq 1 ] && echo "container kept: ${CID}"
	rm -rf "${STAGING}"
}
trap cleanup EXIT

# 1. Image: build only if missing or --rebuild. This is the single, deliberate
#    network step (prebakes toolchain + the full module cache; ADR 0008).
if [ "${REBUILD}" -eq 1 ] || ! "${DOCKER[@]}" image inspect "${IMAGE}" >/dev/null 2>&1; then
	echo ">> building sandbox image (network: one-time cache warm)"
	"${DOCKER[@]}" build -f tools/agent/Dockerfile -t "${IMAGE}" .
fi

# 2. Copy-in source: a clean local clone (full .git, clean tree), NOT a
#    bind-mount. --no-hardlinks so tearing down staging can never reach the
#    host git objects: a botched run cannot touch the host tree (ADR 0008).
echo ">> staging clean clone (copy-in, never bind-mount)"
git clone --quiet --local --no-hardlinks "${REPO}" "${STAGING}/repo"

# 3. Disposable container, ZERO network, ZERO secrets: no -e, no volumes,
#    --network=none. Offline is structural, not policy (CONVENTIONS rule 9).
CID="$("${DOCKER[@]}" run -d --network=none --workdir /workspace "${IMAGE}" sleep infinity)"
echo ">> sandbox container ${CID:0:12} (network: none, secrets: none)"

# Copy the staged repo INTO the container (snapshot, not a mount). STAGING is
# torn down by the EXIT trap, never mid-run.
"${DOCKER[@]}" cp "${STAGING}/repo/." "${CID}:/workspace"

# 4. Inside the sandbox: a sandbox git identity, the work branch, the optional
#    agent, then the make gate that writes RESULT.json + the marker.
#    docker cp preserves host uids; the container runs as root, so git sees
#    "dubious ownership". The tree is a disposable copy — trust it explicitly.
"${DOCKER[@]}" exec "${CID}" git config --global --add safe.directory /workspace
"${DOCKER[@]}" exec "${CID}" git config --global user.email "agent@sandbox.local"
"${DOCKER[@]}" exec "${CID}" git config --global user.name  "Sandbox Agent"
"${DOCKER[@]}" exec "${CID}" git checkout -q -b "${BRANCH}"

if [ -n "${AGENT_CMD:-}" ]; then
	echo ">> running agent: ${AGENT_CMD}"
	"${DOCKER[@]}" exec -e ISSUE="${ISSUE}" "${CID}" bash -lc "${AGENT_CMD}"
	"${DOCKER[@]}" exec "${CID}" bash -lc 'git add -A && git diff --cached --quiet || git commit -q -m "'"${ISSUE}"': agent changes"'
else
	echo ">> no AGENT_CMD: bootstrap/self-test mode (offline gate proof only)"
fi

# maxIterations===1: the runner cannot iterate, so it confirms one marker line
# and reads one structured verdict file instead of polling (ADR 0008).
MARKER="SANDCASTLE_RESULT_COMPLETE"
set +e
GATE_LOG="$("${DOCKER[@]}" exec -e ISSUE="${ISSUE}" "${CID}" make agent-result 2>&1)"
GATE_RC=$?
set -e
printf '%s\n' "${GATE_LOG}"
if printf '%s\n' "${GATE_LOG}" | grep -q "^${MARKER}\$"; then
	echo ">> completion marker seen (single-iteration loop finished)"
else
	echo ">> WARNING: completion marker '${MARKER}' absent — run did not finish cleanly" >&2
fi

# 5. Branch-out: copy ONLY a bundle of the work branch + RESULT.json into a
#    gitignored review dir. No merge. The host working tree is untouched.
mkdir -p "${OUT}"
"${DOCKER[@]}" exec "${CID}" git bundle create /workspace/work.bundle "${BRANCH}" >/dev/null
"${DOCKER[@]}" cp "${CID}:/workspace/work.bundle" "${OUT}/work.bundle"
"${DOCKER[@]}" cp "${CID}:/workspace/RESULT.json" "${OUT}/RESULT.json" 2>/dev/null \
	|| echo '{"ok":false,"error":"RESULT.json not produced"}' > "${OUT}/RESULT.json"

echo
echo ">> branch-out complete (host tree untouched, no auto-merge):"
echo "   verdict : ${OUT}/RESULT.json"
echo "   branch  : git fetch \"${OUT}/work.bundle\" ${BRANCH}"
echo "   review  : inspect, then merge by hand if good"
echo ">> gate exit: ${GATE_RC} (0 = all gates green offline)"
exit "${GATE_RC}"
