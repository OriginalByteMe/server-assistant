# The actor never grades its own homework; the v1 Status spine judges recovery

Status: accepted (closes the Action loop; extends ADR 0005's observer authority
to adjudicate the actor; reuses the ADR 0006 spine; consistent with Q4 cooldown
and Q7 no-auto-retry)

## Context

An Approved Action executes (e.g. `restart_container(plex)`) and the Actuator
returns "command dispatched." That is not recovery: dispatching a restart is
not the same as the Service doing its job again. Q7 covered the crash path; the
normal path's success-judgement was undecided. If the Actuator — or worse, the
LLM — may declare victory, the actor grades its own homework and a no-op "fix"
reads as a win. CONTEXT.md already names the sole authority: scheduled polling
is the authoritative source of Status.

## Decision

The actor never adjudicates its own success; the v1 observer spine does:

1. Actuator success means "command dispatched," nothing more. It never declares
   the Service recovered.
2. After an Action, the harness enters the per-subject cooldown, then reads
   **committed** Status (debounced) from the v1 spine.
3. Committed Status returns to UP within an outcome window ⇒ the Action worked;
   record it. v1's existing recovery-to-UP Alert informs the Operator — no new
   notification; the spine is reused.
4. Still committed-DOWN after the outcome window ⇒ the Action did not work.
   Exactly one one-way Alert ("Action X executed, Service still DOWN — human
   needed"). No auto-re-propose (consistent with Q7 no-auto-retry-mutation and
   the cooldown). The subject is escalated to the human; the harness backs off
   it until Status changes on its own or the Operator intervenes.
5. **A deny means the Operator has taken ownership of the incident**, not "skip
   once." A denied Approval immediately puts that subject into the same
   human-owned backoff as step 4: no re-Diagnose, no re-propose. Backoff
   releases only when the subject's committed Status changes on its own or the
   Operator explicitly re-arms it (an affirmative act, never a default timer).
   M2-v1 deliberately has no iterative "propose a different Action" loop — that
   is speculative complexity and a fatigue/cost vector (2am tiebreaker).

## Consequences

- A failed or no-op remediation cannot masquerade as success; the judge is the
  same debounced spine that detected the incident.
- No retry loop exists: at most one Action per incident, then the human owns it.
  This bounds blast radius and notification volume together.
- Outcome adjudication adds no new mechanism — it is the existing Probe →
  debounce → Status pipeline read after the cooldown.
