# Two-way Approval, single-Operator authz, control surface + sticky halt

## What to build

Implement the **Approval** seam as a two-way exchange distinct from the
one-way `Notifier` (CONTEXT.md; ADR 0009), over Telegram long-poll
(`getUpdates`, never webhook — zero inbound surface; ADR 0009/0001/0003).
Single-**Operator** chat-ID allowlist authz; a response from any other chat ID
is ignored for the decision and raised as a one-way **Alert**. Default-deny on
timeout. The Approval message carries the exact typed **Action**, its blast
radius, and a harness-rendered facts block built directly from raw tool
returns, with any LLM narrative labelled secondary, and restates silence = deny
(ADR 0009). Implement the unified control surface — approve, deny, re-arm,
shadow→live, and a first-class sticky fail-closed halt — all over the same
channel behind the same authz, every control action audited (ADR 0020); a deny
means the Operator has taken ownership (ADR 0016). The `Actuator` remains a dry
no-op that records "would execute".

Conforms to ADR 0009/0016/0017/0020 and `docs/CONVENTIONS.md` (rules 2, 8, 10).

## Acceptance criteria

- [ ] Approval is a two-way seam separate from `Notifier`; `Notifier` stays one-way and unchanged
- [ ] Telegram transport is long-poll only; the box exposes no inbound endpoint
- [ ] Only the allowlisted Operator chat ID can decide; off-list responses are ignored and raise a one-way Alert
- [ ] No response within the window = deny; message restates silence = deny
- [ ] Approval facts block is harness-rendered from raw tool returns; the LLM cannot write it; LLM narrative is labelled and secondary
- [ ] Halt is one command, instantly stops all Diagnosis/Approval/Action, is sticky across restart, lifts only by explicit re-arm
- [ ] approve/deny/re-arm/shadow→live/halt are authz'd identically and written to the durable audit
- [ ] Actuator still dry; no Host mutation occurs; the approve path records "would execute"

## Blocked by

- 0011-read-only-diagnosis-vertical
