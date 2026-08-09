#!/bin/bash
# Forced command for the server-assistant WRITE credential (ADR 0022).
#
# Installed on the Unraid Host as:
#   command="/bin/bash /boot/config/server-assistant-demo/write.sh",no-agent-forwarding,\
#   no-port-forwarding,no-pty,no-X11-forwarding <write pubkey>
#
# The entire M2-v1 Action catalog is restart_container (ADR 0011), and config
# narrows it to the sa-demo-* containers (ADR 0010). This script is the Host-side
# ceiling on that: it is physically incapable of restarting a production
# container, so a bug or a leaked key cannot touch Plex, tdarr, the arr stack,
# or anything else running on the box.
#
# Verified refusal (exit 77) against `docker restart tdarr`.
set -u
c="${SSH_ORIGINAL_COMMAND:-}"

log_refusal() {
  printf '%s refused: %s\n' "$(date -Is)" "$c" \
    >> /mnt/user/appdata/server-assistant-demo/refused.log 2>/dev/null || true
}

if   [[ "$c" =~ ^docker\ restart\ sa-demo-[A-Za-z0-9_.-]+$ ]] \
  || [[ "$c" =~ ^docker\ version\ --format\ .+$ ]]; then
  exec bash -c "$c"
fi

log_refusal
echo "refused: this credential permits 'docker restart sa-demo-*' only" >&2
exit 77
