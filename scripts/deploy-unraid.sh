#!/usr/bin/env bash
# Builds the server-assistant Docker image and ships the Unraid-host stack
# (deploy/docker/) to rijkaardserver via `docker compose`. Idempotent: safe
# to re-run — `docker load` + `docker compose up -d --remove-orphans` only
# recreates what changed, and this script never touches anything on the
# host outside /mnt/user/appdata/server-assistant and the single
# `server-assistant` container/volume it owns.
#
# SAFETY GATE (required — this is a coordinator-only step, not something
# this script runs on its own): every action that reaches the host over SSH
# is skipped unless invoked with --yes-deploy (or CONFIRM_DEPLOY=1 in the
# environment). Without it, the script builds the image locally (safe, pure
# local action, useful on its own to validate the Dockerfile) and then
# prints exactly what it would do next, without touching the network or the
# host, and exits 0.
#
# SA_HOST is an ssh alias/hostname (default: rijkaardserver) — always call
# plain `ssh "$SA_HOST"` / `scp`, never pass extra flags here.

set -euo pipefail

SA_HOST="${SA_HOST:-rijkaardserver}"
COMPOSE_DIR="deploy/docker"
REMOTE_DIR="/mnt/user/appdata/server-assistant"
IMAGE_REF="server-assistant:local"

CONFIRM=0
for arg in "$@"; do
  case "$arg" in
    --yes-deploy) CONFIRM=1 ;;
    -h|--help)
      echo "usage: $0 [--yes-deploy]"
      echo "  without --yes-deploy (or CONFIRM_DEPLOY=1): builds the image locally and dry-runs the rest."
      echo "  with it: also ships the image + stack to \$SA_HOST (default: rijkaardserver) and brings it up."
      exit 0
      ;;
  esac
done
if [[ "${CONFIRM_DEPLOY:-0}" == "1" ]]; then
  CONFIRM=1
fi

log() { echo "[deploy-unraid] $*" >&2; }

log "building ${IMAGE_REF} from deploy/docker/Dockerfile"
docker build -f deploy/docker/Dockerfile -t "$IMAGE_REF" .

if [[ "$CONFIRM" -ne 1 ]]; then
  log "DRY RUN — pass --yes-deploy or set CONFIRM_DEPLOY=1 to actually deploy to ${SA_HOST}"
  log "would run: docker save ${IMAGE_REF} | ssh ${SA_HOST} docker load"
  log "would run: ssh ${SA_HOST} mkdir -p ${REMOTE_DIR}"
  log "would run: scp ${COMPOSE_DIR}/docker-compose.yml ${SA_HOST}:${REMOTE_DIR}/docker-compose.yml"
  log "would run: scp ${COMPOSE_DIR}/config.docker.yaml ${SA_HOST}:${REMOTE_DIR}/config.docker.yaml"
  log "would check: ${SA_HOST}:${REMOTE_DIR}/.env exists (never shipped by this script — provisioned by hand, see docs/DEPLOY-UNRAID.md)"
  log "would run: ssh ${SA_HOST} 'cd ${REMOTE_DIR} && docker compose up -d --remove-orphans'"
  log "would poll: docker inspect --format '{{.State.Health.Status}}' server-assistant on ${SA_HOST} until healthy"
  log "would check: curl -fsS http://localhost:8099/api/health on ${SA_HOST}"
  log "dry run OK — nothing on ${SA_HOST} was touched"
  exit 0
fi

log "saving image and loading it on ${SA_HOST}"
docker save "$IMAGE_REF" | ssh "$SA_HOST" docker load

log "ensuring remote stack directory ${REMOTE_DIR}"
ssh "$SA_HOST" "mkdir -p '${REMOTE_DIR}'"

log "shipping compose + config (never .env — that stays hand-provisioned on the host)"
scp "${COMPOSE_DIR}/docker-compose.yml" "${SA_HOST}:${REMOTE_DIR}/docker-compose.yml"
scp "${COMPOSE_DIR}/config.docker.yaml" "${SA_HOST}:${REMOTE_DIR}/config.docker.yaml"

if ! ssh "$SA_HOST" "test -f '${REMOTE_DIR}/.env'"; then
  log "ERROR: ${REMOTE_DIR}/.env is missing on ${SA_HOST}."
  log "This must hold UNRAID_API_KEY=<key>, provisioned by a human per docs/DEPLOY-UNRAID.md — this script never writes secrets."
  exit 1
fi

log "bringing the stack up on ${SA_HOST}"
ssh "$SA_HOST" "cd '${REMOTE_DIR}' && docker compose up -d --remove-orphans"

log "waiting for the container to report healthy"
status="starting"
for _ in $(seq 1 30); do
  status="$(ssh "$SA_HOST" "docker inspect -f '{{.State.Health.Status}}' server-assistant" 2>/dev/null || echo starting)"
  [[ "$status" == "healthy" ]] && break
  sleep 2
done
if [[ "$status" != "healthy" ]]; then
  log "ERROR: container did not report healthy (last status: ${status})"
  exit 1
fi

log "checking /api/health on port 8099"
ssh "$SA_HOST" "curl -fsS http://localhost:8099/api/health"

log "deploy OK"
