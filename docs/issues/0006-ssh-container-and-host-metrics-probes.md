# SSH container-state + Host-metrics probes

## What to build

Probes that SSH into Unraid (`golang.org/x/crypto/ssh`) to read container state
(running / restarting) and Host metrics (array status, disk health, parity,
CPU/RAM). Container-state feeds Service Status; Host metrics feed Host Status.
The credential is a scoped, non-root, read-only Unraid user, supplied via
env/file, never the committed YAML, never logged. All SSH calls take a context
with an explicit timeout. Probes feed the same debounce/commit/dashboard/alert
pipeline.

Conforms to ADR 0001/0003 and `docs/CONVENTIONS.md` (rules 4, 7).

## Acceptance criteria

- [ ] SSH adapter connects with a non-root read-only Unraid user; secret from env/file, never logged
- [ ] Container-state probe sets a Service DEGRADED/DOWN when its container is not running healthily
- [ ] Host-metrics probe surfaces array/disk/parity + CPU/RAM and can drive Host Status
- [ ] All SSH calls use a context with an explicit timeout
- [ ] Probes feed the same debounce/commit/dashboard/alert pipeline

## Blocked by

- 0004-host-reachability-gate-and-unknown

(External prerequisite for verification: a scoped, non-root, read-only Unraid SSH user.)
