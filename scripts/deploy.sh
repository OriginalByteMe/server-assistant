#!/usr/bin/env bash
# Deploys the server-assistant binary + demo config + systemd unit to the
# Mini Lab dev box (ADR 0023's demo deployment target). Idempotent: safe to
# re-run — it never clobbers an already-deployed config.yaml.
#
# SA_HOST is an ssh alias (default: sa-dev) that already resolves via
# ProxyJump in the operator's ssh config — always call plain `ssh "$SA_HOST"`
# / `scp`, never pass -J or a raw host here.
#
# The read/write SSH keys referenced by deploy/config.demo.yaml
# (/etc/server-assistant/{read,write}_key) are provisioned out of band and
# already exist on the remote box; this script only fixes their ownership
# and permissions so the service user can read them.

set -euo pipefail

SA_HOST="${SA_HOST:-sa-dev}"
REMOTE_USER="server-assistant"
REMOTE_BIN_DIR="/opt/server-assistant/bin"
REMOTE_ETC_DIR="/etc/server-assistant"
REMOTE_VAR_DIR="/var/lib/server-assistant"
REMOTE_BIN="${REMOTE_BIN_DIR}/server-assistant"
REMOTE_CONFIG="${REMOTE_ETC_DIR}/config.yaml"
REMOTE_UNIT="/etc/systemd/system/server-assistant.service"

log() { echo "[deploy] $*" >&2; }

# The deploy target is a root-shell LXC with no sudo package. Probe once and
# use the result everywhere rather than assuming either shape.
if ssh "$SA_HOST" "command -v sudo >/dev/null 2>&1 && [ \"\$(id -u)\" -ne 0 ]"; then
  SUDO="sudo"
else
  SUDO=""
fi

log "building linux/amd64 binary"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/server-assistant ./cmd/server-assistant

log "ensuring remote user/dirs on ${SA_HOST}"
ssh "$SA_HOST" "
  set -euo pipefail
  id -u '${REMOTE_USER}' >/dev/null 2>&1 || ${SUDO} useradd --system --no-create-home --shell /usr/sbin/nologin '${REMOTE_USER}'
  ${SUDO} mkdir -p '${REMOTE_BIN_DIR}' '${REMOTE_ETC_DIR}' '${REMOTE_VAR_DIR}'
  ${SUDO} chown -R '${REMOTE_USER}:${REMOTE_USER}' '${REMOTE_VAR_DIR}'
  ${SUDO} chown '${REMOTE_USER}:${REMOTE_USER}' '${REMOTE_BIN_DIR}'
"

log "stopping service if running"
ssh "$SA_HOST" "${SUDO} systemctl stop server-assistant 2>/dev/null || true"

log "copying binary"
scp bin/server-assistant "${SA_HOST}:/tmp/server-assistant.new"
ssh "$SA_HOST" "${SUDO} mv /tmp/server-assistant.new '${REMOTE_BIN}' && ${SUDO} chown '${REMOTE_USER}:${REMOTE_USER}' '${REMOTE_BIN}' && ${SUDO} chmod 755 '${REMOTE_BIN}'"

if ssh "$SA_HOST" "${SUDO} test -f '${REMOTE_CONFIG}'"; then
  log "notice: ${REMOTE_CONFIG} already exists on ${SA_HOST} — leaving it in place"
else
  log "copying deploy/config.demo.yaml -> ${REMOTE_CONFIG}"
  scp deploy/config.demo.yaml "${SA_HOST}:/tmp/config.yaml.new"
  ssh "$SA_HOST" "${SUDO} mv /tmp/config.yaml.new '${REMOTE_CONFIG}' && ${SUDO} chown '${REMOTE_USER}:${REMOTE_USER}' '${REMOTE_CONFIG}' && ${SUDO} chmod 640 '${REMOTE_CONFIG}'"
fi

log "copying systemd unit"
scp deploy/server-assistant.service "${SA_HOST}:/tmp/server-assistant.service.new"
ssh "$SA_HOST" "${SUDO} mv /tmp/server-assistant.service.new '${REMOTE_UNIT}'"

log "fixing read/write SSH key ownership + permissions"
ssh "$SA_HOST" "
  set -euo pipefail
  for k in read_key write_key; do
    f=\"${REMOTE_ETC_DIR}/\${k}\"
    if ${SUDO} test -f \"\$f\"; then
      ${SUDO} chown '${REMOTE_USER}:${REMOTE_USER}' \"\$f\"
      ${SUDO} chmod 600 \"\$f\"
    fi
  done
"

log "ensuring the pinned local Diagnosis model exists"
scp deploy/Modelfile.sa-triage "${SA_HOST}:/tmp/Modelfile.sa-triage"
ssh "$SA_HOST" "ollama show sa-triage >/dev/null 2>&1 || ollama create sa-triage -f /tmp/Modelfile.sa-triage"

log "reloading systemd + enabling service"
ssh "$SA_HOST" "${SUDO} systemctl daemon-reload && ${SUDO} systemctl enable --now server-assistant"

log "waiting for service to become active"
for i in $(seq 1 30); do
  if ssh "$SA_HOST" "systemctl is-active --quiet server-assistant"; then
    break
  fi
  sleep 1
  if [ "$i" -eq 30 ]; then
    log "service never became active"
    ssh "$SA_HOST" "${SUDO} journalctl -u server-assistant -n 50 --no-pager" || true
    exit 1
  fi
done

log "checking /api/health"
if ! ssh "$SA_HOST" "curl -fsS http://localhost:8080/api/health"; then
  log "health check failed"
  ssh "$SA_HOST" "${SUDO} journalctl -u server-assistant -n 50 --no-pager" || true
  exit 1
fi

log "deploy OK"
