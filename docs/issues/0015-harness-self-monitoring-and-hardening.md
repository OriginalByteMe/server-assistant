# Harness self-monitoring + multi-incident hardening + dashboard surfacing

## What to build

Make the **Harness** itself a monitored subject reusing the v1
**Probe**→debounce→**Status**→**Alert** spine (ADR 0015): probe its own
dependencies (Reasoner reachable, scrubbing operational, Telegram channel live,
write credential valid) to derive a harness Status; a single transient
**Diagnosis** miss stays silent (degrades to v1 monitoring), but a committed
harness-DOWN fires exactly one one-way Alert ("harness non-functional — live
Approvals will not fire"), never an **Approval**. Add the bounded queue with
drop-on-recover for concurrent committed-DOWN subjects so the **Operator**
faces at most one decision at a time (ADR 0017). Surface on the read-only
dashboard: the shadow track record, current backed-off subjects, halt state,
and the durable audit trail (consequences of ADR 0014/0019/0020).

Conforms to ADR 0015/0017/0019/0020 and `docs/CONVENTIONS.md` (rules 2, 5, 9).

## Acceptance criteria

- [ ] Harness dependencies are probed and reduced to a harness Status via the existing debounce/commit machinery
- [ ] A single transient Diagnosis miss is silent; a committed harness-DOWN fires exactly one one-way Alert and never an Approval
- [ ] Concurrent committed-DOWN subjects queue (bounded); a subject that recovers while queued is dropped silently; the Operator faces at most one decision at a time
- [ ] Dashboard surfaces shadow track record, backed-off subjects, halt state, and the audit trail; remains read-only / SSE
- [ ] No new persistence or notification mechanism is introduced — the v1 spine is reused reflexively

## Blocked by

- 0014-live-mutation-actuator-outcome-adjudication
