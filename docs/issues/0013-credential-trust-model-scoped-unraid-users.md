# Credential trust model: scoped read + write Unraid users (HITL)

## What to build

Provision the M2 Host-credential trust model that, together with the harness
design (ADR 0009), satisfies the ADR 0003 hard gate. Create two distinct
non-root Unraid users (ADR 0022): the existing read-only **Probe** user stays
frozen and never gains write; a separate write user scoped to exactly the
M2-v1 **Action** catalog verb — container restart of the allowlisted set,
nothing else: no shell, no arbitrary file read, no sudo (ADR 0011/0022). Wire
the read credential only behind `Prober` and the read-only tools, the write
credential only behind `Actuator`; they are never shared. Secrets come from
env/file, never committed YAML, never logs (rules 7/8).

This is a **Human-in-the-loop** slice: it requires creating and scoping real
users on the Unraid **Host** and a human review that the write scope equals the
catalog verb and no more — the ADR 0003 gate checkpoint.

Conforms to ADR 0003/0011/0022 and `docs/CONVENTIONS.md` (rules 7, 8).

## Acceptance criteria

- [ ] Two distinct non-root Unraid users exist; the Probe user is read-only and unchanged from v1
- [ ] The write user can restart exactly the allowlisted containers and nothing else (no shell, no file read, no sudo) — verified against the Host
- [ ] Read credential wired only behind `Prober`/read-tools; write credential only behind `Actuator`; no code path shares them
- [ ] Secrets supplied via env/file; absent from committed YAML and from all logs
- [ ] A human has reviewed and signed off that the write scope equals the M2-v1 catalog verb and no more (ADR 0003 gate checkpoint)

## Blocked by

- 0010-harness-rails-seams-enablement-audit
