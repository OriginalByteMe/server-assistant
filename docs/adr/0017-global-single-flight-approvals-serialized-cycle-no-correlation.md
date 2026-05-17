# Global single-flight Approvals; serialized Diagnosis cycle; no correlation engine

Status: accepted (protects the ADR 0014 gate under multi-incident load; reuses
Q4 cooldown / Q7 stale-Approval / Q9 ceiling philosophy)

## Context

Q4 bounded the harness per subject (one in-flight Action + cooldown each).
Cross-subject was open. A bad deploy or Host blip can drop several Services
committed-DOWN simultaneously (Host-gated ones become UNKNOWN and are not acted
on per Q4). A naive harness would run parallel Diagnoses and fire several
Approvals to the Operator at once — the exact notification storm ADR 0014's
enablement ramp exists to prevent, reintroduced through the side door — plus a
parallel LLM cost/load spike on an already-degraded environment. N-down-at-once
is also often one root cause, but root-cause correlation is speculative M2
complexity the 2am tiebreaker rejects.

## Decision

1. **Global single-flight Approvals.** At most one Approval is outstanding to
   the Operator at any time, across the whole harness. One human, one decision
   at a time.
2. **Serialize the whole cycle, not just the prompt.** One Diagnosis →
   Approval → outcome cycle at a time, globally. No parallel LLM runs (matches
   the Q9 ceiling/cost philosophy). Other committed-DOWN subjects queue.
3. **Bounded queue; recover-while-queued ⇒ drop silently.** A queued subject
   that returns to UP before its turn is dropped — no stale Approval for a
   self-resolved incident (same spirit as Q7 stale-Approval forced-Expire).
4. **No root-cause correlation in M2-v1.** The serialized queue plus the
   dashboard showing all current DOWNs lets the human spot "these are all one
   thing" and deny the noise. Correlation is a later deliberate ADR if ever.

## Consequences

- The Operator faces at most one decision at a time regardless of how many
  Services fail together; the ADR 0014 gate survives multi-incident load.
- Harness cost and load stay bounded under a broad outage.
- The system never auto-reasons about shared causes; that judgement stays with
  the human, by deliberate omission.
