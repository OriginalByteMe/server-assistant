# Harness ships default-off; enablement ramps shadow → live, never a switch

Status: accepted (protects the human gate the whole fail-closed model rests on;
relates to ADR 0003 posture, ADR 0009 architecture, ADR 0010 config-narrows,
ADR 0011 narrow catalog)

## Context

Every fail-closed guarantee in ADR 0009/0011 ultimately rests on the Operator
being a *meaningful* Approval gate (Q5/Q6). Humans rubber-stamp under
notification fatigue: a harness that produces false-positive proposals trains
the Operator to ignore or reflexively approve, which hollows out every
guarantee built on the gate. A harness that never proposes anything useful is
dead weight nobody trusts. Nothing in the rest of the design protects the gate
itself.

## Decision

Enablement is a ramp, not a switch:

1. **The harness ships disabled by default.** M2 being built (the ADR 0003
   gate) is not the same as the harness being on. Enabling is an explicit,
   deliberate config act (consistent with ADR 0010 config-narrows).
2. **Enablement begins in shadow mode.** Full Diagnosis runs; the would-be
   Action is recorded to Store and surfaced on the dashboard (audited), but **no
   Approval is sent and no Action is taken** — zero blast radius. This proves
   Diagnosis quality before any mutation is proposed to the human.
3. **Promotion to live is a second explicit config step**, taken only after the
   Operator has reviewed the shadow track record. First live Approvals are then
   high-signal and low-stakes (one reversible action per ADR 0011,
   harness-rendered facts per ADR 0009, cooldown anti-storm per the act-on-DOWN
   rule).
4. **No confidence-threshold or trust-scoring machinery in M2-v1.** Shadow-mode
   review is the calibration mechanism. Per the 2am tiebreaker, speculative
   scoring is not pre-built; if fatigue still emerges in practice it is a later
   deliberate ADR.

## Consequences

- The Operator's trust is earned against a real, auditable shadow track record
  before the gate carries any weight.
- Two explicit config transitions exist (off → shadow → live); each is a
  visible, deliberate operator decision, not a default.
- "It's enabled but won't act" is intended behaviour in shadow mode and is
  documented at the enablement point so it does not read as a bug.
