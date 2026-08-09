# Dashboard is the Approval surface for the demo milestone

Status: accepted (scopes ADR 0009's Approval channel for this milestone only;
Telegram remains the target production channel per ADR 0009)

## Context

ADR 0009 names Telegram as the Approval channel: a one-Operator, long-poll
(never webhook) transport carrying the Diagnosis facts block and the
Approve/Deny decision. No Telegram bot is provisioned for this milestone —
the M2 harness demo is a self-contained Mini Lab exercise (an LXC dev box
running the harness against Ollama, and an Unraid host with a throwaway
`sa-demo-web` container, both on a private network) whose purpose is proving
the harness contract end to end (ADR 0009/0011/0016/0017/0018/0019/0020/0022):
Diagnosis, a proposed Action, a real Approval decision, Actuation, and outcome
judgement. Blocking this milestone on Telegram bot provisioning would stall a
demo about the harness wiring, not about the notification transport.

## Decision

For this milestone the web dashboard (built in v1 Slice 1) carries three
things it did not carry before:

- **Alert presentation** — unchanged, already dashboard-visible.
- **Approval** — a pending Diagnosis renders its proposed Action and the
  harness-rendered evidence facts block (ADR 0009: LLM never composes this
  block) with Approve/Deny controls.
- **The sticky halt/re-arm controls** from ADR 0020's unified control surface.

This is exposed through a `web.HarnessSource` interface — a seam in the same
spirit as `core.Notifier`: `internal/harness` never talks to the dashboard
directly, and the dashboard never talks to the harness core directly, both
go through the seam. The semantics ADR 0009 (default-deny Approval,
quarantined mutation), ADR 0014 (default-off, shadow before live), ADR 0017
(single-flight, no correlation-id ambiguity), and ADR 0020 (sticky halt is
unified and never auto-clears) already fixed are **unchanged** — this ADR is
presentation-layer only. Telegram remains ADR 0009's eventual channel; when
it is provisioned it attaches behind `HarnessSource` the same way the
dashboard does now, per ADR 0006 (stable core, richer backends attach behind
it).

## Considered Options

- **Block the demo on Telegram bot provisioning (rejected):** blocks proving
  the harness wiring on an unrelated dependency (bot registration, chat-ID
  allowlisting) that has nothing to do with what this milestone verifies.
- **Stub/auto-approve Approval for the demo (rejected):** does not exercise
  the actual default-deny Approval decision the whole ADR 0009 spine exists
  to gate; a rehearsed demo, not a proof that the contract works.
- **Dashboard as the real Approval surface behind `HarnessSource` (chosen):**
  exercises the full default-deny → Approval → Actuator → audit path for
  real, reuses UI infrastructure the v1 spine already has, and the seam
  gives Telegram a clean, non-disruptive later attach point.

## Consequences

- The dashboard's HTTP surface has no authentication in this milestone.
  ADR 0003 already defers authn/hardening until before M2 ships to
  *production*; this ADR does not loosen that gate — it explicitly scopes
  dashboard-as-Approval-surface to this LAN/Tailscale-only Mini Lab
  deployment (Unraid at 192.168.68.57, dev box at 192.168.68.61), never a
  public or production boundary.
- Anyone who can reach the dashboard's HTTP port can Approve, Deny, Halt, or
  Re-arm. This is acceptable only because the demo network has no untrusted
  party with access. **This is explicitly NOT a production Approval
  boundary** and must never be read as satisfying ADR 0003's hard gate.
- Before Telegram (or any other real Approval channel) ships, `HarnessSource`
  must not grow any behavior a future Telegram implementation cannot also
  honor — no dashboard-only escape hatches into the Approval semantics.
- Supersedes nothing. ADR 0009 remains the canonical Approval-channel
  decision; this is a milestone-scoped presentation choice made under it.
