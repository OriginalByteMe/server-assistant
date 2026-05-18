#!/usr/bin/env bash
#
# ADR 0008 sandcastle: the ONE command that makes sandboxed execution the
# DEFAULT (ARK-21). It selects a container runtime — PREFERRING Docker Desktop
# — and then hands off to run.sh, which owns the copy-in / offline /
# branch-out contract. This script adds runtime selection only; it never
# weakens the sandbox and it has NO path that runs an agent on the host.
#
#   tools/agent/sandcastle.sh [--rebuild] [--keep] [--branch NAME] <ISSUE>
#
# The agent command is the same hook run.sh uses: export
#   AGENT_CMD="<cmd run inside the repo, at the repo root>"
# Unset = bootstrap/self-test mode (offline gate proof only).
#
# Runtime preference (ADR 0008; ARK-21 acceptance criteria):
#   1. Docker Desktop, if installed AND its daemon is reachable. This is the
#      preferred runtime and is tried first.
#   2. ONLY if Docker Desktop is unavailable: an EXPLICITLY configured
#      fallback runtime via  SANDCASTLE_FALLBACK_DOCKER="<docker-like cmd>"
#      (e.g. "docker" for a plain Engine, or "podman"). Used loudly, never
#      silently.
#   3. Otherwise: FAIL LOUDLY and exit non-zero. There is deliberately no
#      host-run fallback — running the agent on the host is exactly what the
#      sandcastle exists to prevent.
#
# Dev-time meta-tooling under tools/agent/. Never compiled into nor shipped
# with the static binary (CONVENTIONS rule 1; ADR 0008).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="${HERE}/run.sh"

die() { echo "sandcastle: $*" >&2; exit 3; }

[ -x "${RUN}" ] || die "run.sh not found or not executable at ${RUN}"

# An issue id is required: the command is '/sandcastle <issue>'. We do not
# parse run.sh's flags here — everything is forwarded verbatim — but we do
# insist at least one non-flag argument (the issue) is present, so an empty
# invocation fails fast instead of silently self-testing 'unknown'.
have_issue=0
for a in "$@"; do
	case "${a}" in
		--branch) ;;            # its value is the next arg, not an issue
		--*) ;;
		*) have_issue=1 ;;
	esac
done
[ "${have_issue}" -eq 1 ] || die "usage: sandcastle.sh [--rebuild] [--keep] [--branch NAME] <ISSUE>"

command -v docker >/dev/null 2>&1 \
	|| die "the 'docker' CLI is not installed. Sandcastle needs Docker Desktop; it will NOT run the agent on the host."

# A reachable, Docker-Desktop daemon reports OperatingSystem == "Docker
# Desktop". `timeout` keeps a dead daemon from hanging the command.
TO=""
command -v timeout >/dev/null 2>&1 && TO="timeout 20"

is_docker_desktop() {
	# $* is the candidate runtime invocation (e.g. `docker --context desktop-linux`)
	local os
	os="$(${TO} "$@" info --format '{{.OperatingSystem}}' 2>/dev/null)" || return 1
	case "${os}" in
		*"Docker Desktop"*) return 0 ;;
		*) return 1 ;;
	esac
}

SELECTED=""

# Preference 1a: the explicit Docker Desktop CLI context (how Docker Desktop
# exposes itself on Linux), even when it is not the current context.
if docker context inspect desktop-linux >/dev/null 2>&1 \
	&& is_docker_desktop docker --context desktop-linux; then
	SELECTED="docker --context desktop-linux"
# Preference 1b: the current context already IS Docker Desktop.
elif is_docker_desktop docker; then
	SELECTED="docker"
fi

if [ -n "${SELECTED}" ]; then
	echo ">> runtime: Docker Desktop (${SELECTED}) — preferred"
elif [ -n "${SANDCASTLE_FALLBACK_DOCKER:-}" ]; then
	# Explicit, opted-in fallback only. Still a container runtime, never the
	# host. Verify its daemon answers before we commit to it.
	echo ">> WARNING: Docker Desktop unavailable — using EXPLICIT fallback runtime: ${SANDCASTLE_FALLBACK_DOCKER}" >&2
	# shellcheck disable=SC2086
	${TO} ${SANDCASTLE_FALLBACK_DOCKER} info >/dev/null 2>&1 \
		|| die "explicit fallback runtime '${SANDCASTLE_FALLBACK_DOCKER}' is set but its daemon is not reachable. Refusing to run on the host."
	SELECTED="${SANDCASTLE_FALLBACK_DOCKER}"
else
	die "Docker Desktop is not available (not installed, or its daemon is not running).
       Sandcastle will NOT silently fall back to a host run (ADR 0008).
       Fix one of:
         - start Docker Desktop, then re-run; or
         - set SANDCASTLE_FALLBACK_DOCKER to an explicit container runtime
           (e.g. SANDCASTLE_FALLBACK_DOCKER=docker for a plain Engine)."
fi

# Hand off. run.sh owns the ADR 0008 contract (copy-in not bind-mount,
# --network=none, branch-out + RESULT.json, host tree untouched, no merge).
# DOCKER is read as words by run.sh, so the multi-flag Desktop context value
# survives intact.
echo ">> handing off to run.sh (sandboxed by default; host tree never mutated)"
exec env DOCKER="${SELECTED}" "${RUN}" "$@"
