#!/usr/bin/env bash
# Provisions/resets the throwaway demo container on the Unraid Mini Lab box
# used by `make demo-e2e` and the manual demo walkthrough. Only ever touches
# containers named sa-demo-* — see the hard refusal below.

set -euo pipefail

SA_UNRAID="${SA_UNRAID:-rijkaardserver}"
SA_DEMO_CONTAINER="${SA_DEMO_CONTAINER:-sa-demo-web}"
APPDATA_DIR="/mnt/user/appdata/server-assistant-demo"

# Hard safety rail: this script may NEVER touch a container outside the
# sa-demo- namespace, no matter what SA_DEMO_CONTAINER is overridden to.
case "$SA_DEMO_CONTAINER" in
  sa-demo-*) ;;
  *)
    echo "[demo-setup] refusing: SA_DEMO_CONTAINER='${SA_DEMO_CONTAINER}' does not start with 'sa-demo-'" >&2
    exit 1
    ;;
esac

log() { echo "[demo-setup] $*" >&2; }

log "ensuring ${APPDATA_DIR} exists on ${SA_UNRAID}"
ssh "$SA_UNRAID" "mkdir -p '${APPDATA_DIR}'"

log "recreating throwaway container ${SA_DEMO_CONTAINER} on ${SA_UNRAID}"
ssh "$SA_UNRAID" "docker rm -f '${SA_DEMO_CONTAINER}' >/dev/null 2>&1 || true"
ssh "$SA_UNRAID" "docker run -d --name '${SA_DEMO_CONTAINER}' --memory 64m --cpus 0.25 --restart=no busybox sleep infinity"

log "done: ${SA_DEMO_CONTAINER} is running on ${SA_UNRAID}"
log "REMINDER: this script and every operator action against ${SA_UNRAID} must only ever touch sa-demo-* containers"
