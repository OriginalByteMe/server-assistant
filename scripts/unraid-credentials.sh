#!/usr/bin/env bash
# Installs the split read/write SSH credentials on the Unraid Host (ADR 0022).
#
# Idempotent. Run from the repo root:
#   bash scripts/unraid-credentials.sh
#
# What it does, and nothing else:
#   1. generates the two keypairs on the deploy box if absent (private keys
#      never leave it),
#   2. copies the two forced-command wrappers from deploy/unraid/ to the Unraid
#      flash config dir so they survive a reboot,
#   3. appends the two restricted authorized_keys lines to BOTH the live
#      /root/.ssh/authorized_keys and the persisted /boot/config/ssh/root/ copy,
#   4. creates the single demo appdata directory.
#
# It appends; it never rewrites or removes an existing key. It touches no
# Unraid array, share, container, or system setting.
set -euo pipefail

SA_HOST="${SA_HOST:-sa-dev}"                   # where the private keys live
SA_UNRAID="${SA_UNRAID:-rijkaardserver}"       # the Unraid Host
ETC="${SA_ETC:-/etc/server-assistant}"
FLASH="/boot/config/server-assistant-demo"     # persists across Unraid reboots
APPDATA="/mnt/user/appdata/server-assistant-demo"
KEYOPTS="no-agent-forwarding,no-port-forwarding,no-pty,no-X11-forwarding"

log() { echo "[creds] $*" >&2; }

log "generating keypairs on ${SA_HOST} if absent"
ssh "$SA_HOST" "
  set -euo pipefail
  mkdir -p '${ETC}'
  cd '${ETC}'
  [ -f read_key  ] || ssh-keygen -q -t ed25519 -N '' -C sa-read  -f read_key
  [ -f write_key ] || ssh-keygen -q -t ed25519 -N '' -C sa-write -f write_key
  chmod 600 read_key write_key
"
READ_PUB=$(ssh "$SA_HOST" "cat '${ETC}/read_key.pub'")
WRITE_PUB=$(ssh "$SA_HOST" "cat '${ETC}/write_key.pub'")

log "installing forced-command wrappers on ${SA_UNRAID}"
ssh "$SA_UNRAID" "mkdir -p '${FLASH}' '${APPDATA}'"
scp -q deploy/unraid/read-only-command.sh  "${SA_UNRAID}:${FLASH}/read.sh"
scp -q deploy/unraid/write-only-command.sh "${SA_UNRAID}:${FLASH}/write.sh"

log "authorising the two restricted keys (append-only)"
ssh "$SA_UNRAID" "
  set -euo pipefail
  for f in /root/.ssh/authorized_keys /boot/config/ssh/root/authorized_keys; do
    mkdir -p \"\$(dirname \"\$f\")\"
    touch \"\$f\"; chmod 600 \"\$f\"
    grep -q ' sa-read\$'  \"\$f\" || echo 'command=\"/bin/bash ${FLASH}/read.sh\",${KEYOPTS} ${READ_PUB}'  >> \"\$f\"
    grep -q ' sa-write\$' \"\$f\" || echo 'command=\"/bin/bash ${FLASH}/write.sh\",${KEYOPTS} ${WRITE_PUB}' >> \"\$f\"
  done
"

log "verifying the credential split actually holds"
ssh "$SA_HOST" "
  set -u
  ok=0
  ssh -i '${ETC}/read_key' -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@\$(echo '${SA_UNRAID_ADDR:-192.168.68.57}') \
    'docker version --format {{.Server.Version}}' >/dev/null || ok=1
  # The read key must NOT be able to mutate anything.
  if ssh -i '${ETC}/read_key' -o BatchMode=yes root@'${SA_UNRAID_ADDR:-192.168.68.57}' 'docker restart sa-demo-web' >/dev/null 2>&1; then
    echo 'FAIL: read key was able to restart a container' >&2; exit 1
  fi
  # The write key must NOT be able to touch a non-demo container.
  if ssh -i '${ETC}/write_key' -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@'${SA_UNRAID_ADDR:-192.168.68.57}' 'docker restart tdarr' >/dev/null 2>&1; then
    echo 'FAIL: write key was able to restart a production container' >&2; exit 1
  fi
  exit \$ok
"

log "credentials installed and scoping verified"
