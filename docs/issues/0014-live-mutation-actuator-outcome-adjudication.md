# Live mutation vertical: real Actuator + outcome adjudication + shadow→live

## What to build

Make the `Actuator` really perform the M2-v1 **Action** — restart the
allowlisted container via the scoped write credential — using deterministic,
config-driven Service→container resolution owned by the harness, not the LLM
(ADR 0011/0018/0022). Mutation runs only after an approved **Approval**, under
global single-flight (at most one Approval/cycle outstanding harness-wide) with
the serialized **Diagnosis** cycle, per-subject one-in-flight + cooldown, never
on UNKNOWN, only on committed DOWN (ADR 0017). A crash with an Action of
unknown fate is marked Unresolved with a one-way **Alert** and never
auto-resumed. The Actuator never declares success: recovery is judged solely by
the v1 committed-**Status** spine after cooldown — UP within the outcome window
reuses v1's existing recovery Alert; still-DOWN fires one Alert, no auto-retry,
the subject backs off to human ownership; a deny is the same human-ownership
backoff (ADR 0016). Implement the explicit Operator-driven off→shadow→live
promotion (ADR 0014).

Conforms to ADR 0011/0014/0016/0017/0018/0022 and `docs/CONVENTIONS.md` (rules
4, 6, 10).

## Acceptance criteria

- [ ] An approved Approval restarts exactly the resolved allowlisted container via the write credential; Service→container resolution is deterministic config, never LLM-chosen
- [ ] No mutation on UNKNOWN; only on committed DOWN; global single-flight + serialized cycle + per-subject one-in-flight and cooldown enforced
- [ ] Crash with an Action of unknown fate → Unresolved + one-way Alert, never auto-resumed
- [ ] Actuator never reports recovery; the v1 committed-Status spine adjudicates after cooldown: UP→reuse existing recovery Alert, still-DOWN→one Alert + no retry + human-ownership backoff
- [ ] A deny puts the subject into the same human-ownership backoff, released only by Status change or explicit re-arm
- [ ] off→shadow→live promotion is an explicit Operator action; live behaviour matches the shadow track record
- [ ] Full cycle (trigger, Diagnosis, Approval decision + who/when, resolved target, dispatch, outcome) written to the durable audit

## Blocked by

- 0012-two-way-approval-authz-control-surface
- 0013-credential-trust-model-scoped-unraid-users
