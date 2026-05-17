# Unified Telegram control surface; first-class sticky fail-closed halt

Status: accepted (extends the fail-closed spine to the human-control layer;
reuses Q6 authz and ADR 0019 audit; consistent with ADR 0014/0016 affirmative-
enable and Q7 no-auto-resume)

## Context

The design accumulated several Operator→harness control actions — approve, deny
(Q24), re-arm a backed-off subject (ADR 0016), promote shadow→live (ADR 0014) —
without deciding how they are issued. One control was never raised and is the
most important: an emergency stop. An LLM-advised production-mutation system
with no instant, unambiguous halt is indefensible, and a halt that requires a
config-file edit is useless at 2am from a phone during the incident it is
needed for.

## Decision

1. **One control surface.** All Operator→harness control (approve, deny,
   re-arm, shadow→live, halt) is inbound over the same Telegram channel, behind
   the same single-Operator chat-ID authz as Approval (Q6). Never a second,
   weaker control path. Every control action is an audited accountability event
   (ADR 0019): who, what, when.
2. **Channel-native first-class halt.** A single command from the channel the
   Operator always has instantly puts the entire harness into hard stop: no
   Diagnosis, no Approval, no Action. Any in-flight cycle aborts; any in-flight
   Action of unknown fate takes the Q7 Unresolved+Alert path, never
   auto-resumed.
3. **Halt is sticky and fail-closed.** It persists across restart/crash/reboot
   (safety state must survive — consistent with rule 6 restart-safety) and is
   lifted only by an explicit Operator re-arm, never a timer.
4. **Asymmetry principle.** Stopping is always one tap, sticky, instant;
   starting/continuing (enable, shadow→live, re-arm, lift-halt) is always an
   explicit affirmative act. The system is biased toward inaction at every
   control point — the fail-closed spine extended to the human-control layer.

## Consequences

- The Operator can always, instantly, from the device they have, guarantee the
  harness takes no further Action — the ultimate fail-closed backstop.
- Control authz has a single throat to secure and audit; no privileged action
  bypasses the chat-ID allowlist.
- Every control transition is in the durable audit trail, so "who halted/armed
  it and when" is answerable like every other accountability question.
