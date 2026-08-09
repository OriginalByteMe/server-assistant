#!/bin/bash
# Forced command for the server-assistant READ credential (ADR 0022).
#
# Installed on the Unraid Host as:
#   command="/bin/bash /boot/config/server-assistant-demo/read.sh",no-agent-forwarding,\
#   no-port-forwarding,no-pty,no-X11-forwarding <read pubkey>
#
# This is the outermost of the four defence layers in ADR 0021: even if the
# read key leaks, or a bug in the Diagnosis tools tried to run something else,
# the Host itself refuses anything that is not one of the read-only shapes
# below. The tool code must never be the only thing keeping this credential
# read-only.
#
# Shapes permitted here mirror, exactly, the commands built by
# internal/prober/ssh.go and internal/tools/tools.go. Change one and you must
# change the other.
set -u
c="${SSH_ORIGINAL_COMMAND:-}"
n='[A-Za-z0-9_.-]+'

log_refusal() {
  # Refusals are the interesting signal — a refusal means either a code change
  # drifted from this allowlist, or something is probing the credential.
  printf '%s refused: %s\n' "$(date -Is)" "$c" \
    >> /mnt/user/appdata/server-assistant-demo/refused.log 2>/dev/null || true
}

if   [[ "$c" =~ ^docker\ inspect\ (-f|--format)\ .+\ $n$ ]] \
  || [[ "$c" =~ ^docker\ logs\ --tail\ [0-9]+\ $n(\ 2\>\&1)?$ ]] \
  || [[ "$c" =~ ^docker\ ps(\ --format\ .+)?$ ]] \
  || [[ "$c" =~ ^docker\ version\ --format\ .+$ ]] \
  || [[ "$c" =~ ^(mdcmd\ status|cat\ /proc/loadavg|cat\ /proc/meminfo|uptime)$ ]] \
  || [[ "$c" =~ ^(mdcmd\ status;|cat\ /proc/loadavg;|cat\ /proc/meminfo;|echo\ .*;)+ ]]; then
  exec bash -c "$c"
fi

log_refusal
echo "refused: this credential permits read-only docker/host queries only" >&2
exit 77
