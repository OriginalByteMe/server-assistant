# Harness rails: new seams, default-off enablement ramp, durable audit

## What to build

Bootstrap the M2 **Harness** as a new pipeline that *consumes* committed
**Status** from the v1 spine without reshaping it (ADR 0009/0006). Define the
two new M2 seams — `Reasoner` and `Actuator` — as interfaces with stub/no-op
implementations sufficient to wire. Add config that gates the Harness
**default-OFF**, with a sticky, restart-safe enablement state machine
off→shadow→live (ADR 0014/0020): enablement is safety state persisted in the
**Store** and survives restart/crash/reboot; only an explicit transition
advances it, never a timer. Add a durable, append-only **audit** record class
behind the Store seam, retained independently of rolling **Probe**-history
truncation (ADR 0019), insert-only by sqlc discipline. In shadow mode a
committed-DOWN Status produces an audited "would-have-diagnosed" record only —
no Reasoner call, no **Action**, no Telegram.

Conforms to ADR 0006/0009/0014/0019/0020 and `docs/CONVENTIONS.md` (rules 2, 6,
8, 9, 10).

## Acceptance criteria

- [ ] `Reasoner` and `Actuator` defined as interfaces with no-op stubs; the Harness consumes committed Status and no v1 seam is reshaped
- [ ] Harness is default-OFF; nothing runs until explicitly enabled via config
- [ ] Enablement is off→shadow→live, sticky and restart-safe; only an explicit transition advances it, never a timer
- [ ] Audit records are append-only (no update/delete queries generated) and a distinct retention class exempt from Probe-history truncation
- [ ] In shadow mode a committed-DOWN Status writes a durable audited "would-have" record; no Reasoner/Actuator/Telegram invoked
- [ ] Unit tests use in-memory SQLite and fakes; no network (rule 9)

## Blocked by

- v1 monitoring spine: 0001-skeleton-and-seams through 0009-systemd-unit-and-deploy-doc
