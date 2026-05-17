# The harness is a monitored subject; a broken harness is a first-class Alert

Status: accepted (extends ADR 0005 / ADR 0001's "blindness is a first-class
signal" to the harness; reuses the ADR 0006 Status/Probe spine; refines the Q9
best-effort rule)

## Context

After the enablement ramp (ADR 0014) the Operator can run the harness live and
will trust it as a guardrail. But the harness has dependencies that fail
silently: Reasoner unreachable, local model server crashed, scrubbing failing,
Telegram long-poll disconnected, write credential expired. ADR 0009/Q9 made a
failed Diagnosis best-effort and silently degrading to plain v1 monitoring —
correct for a single transient miss, but if the harness is *persistently*
broken while the Operator believes it is guarding, the Operator is trusting a
guardrail that is not there and does not know it. That silent-missing-guardrail
is exactly the failure ADR 0005 ("the observer never lies") and ADR 0001
("Unraid-unreachable is itself a first-class signal") exist to forbid.

## Decision

The harness is itself a monitored subject, reusing the existing Status/Probe
spine (ADR 0006 said the harness *consumes* that model; it is also *subject
to* it):

- Probe the harness's own dependencies — Reasoner reachable, scrubbing
  operational, Telegram channel live, write credential valid — and derive a
  harness Status (UP/DEGRADED/DOWN/UNKNOWN).
- **Transient vs committed split via the existing debounce/commit machinery.** A
  single failed Diagnosis (LLM hiccup, ceiling hit, cooldown) stays silent and
  degrades to v1 monitoring (Q9 unchanged). A *committed* harness-health DOWN
  (debounced, N agreeing Probes) fires exactly **one one-way Alert**: "harness
  non-functional — live Approvals will not fire."
- **Alert, never Approval.** It is informational and fail-safe; the CONTEXT.md
  Alert≠Approval distinction holds. No new mechanism — the v1 Alert spine
  pointed at the harness.

## Consequences

- A persistently broken guardrail is loud, not silent; the Operator cannot
  unknowingly trust an absent gate.
- Transient best-effort misses remain silent, so this does not reintroduce
  notification noise the debounce was built to prevent.
- The harness gains no new subsystem for self-monitoring; it is the existing
  Probe → debounce → Status → Alert pipeline applied reflexively.
