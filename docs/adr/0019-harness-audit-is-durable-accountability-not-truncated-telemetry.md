# Harness audit is durable accountability, exempt from Probe-history truncation

Status: accepted (distinguishes accountability from telemetry; reuses the Store
seam per ADR 0006; carves an exception to ADR 0002 / Slice 5 rolling retention)

## Context

Q10 routed Diagnosis transcripts to Store (ADR 0006). ADR 0002 and the v1
PLAN's Slice 5 define a rolling Probe-history retention window — old samples are
truncated. It was never decided whether the harness audit trail is subject to
that same truncation. For a subsystem that mutates a production box on LLM
advice, the audit trail is the sole post-hoc accountability; ageing it out with
disposable latency samples would destroy the ability to answer what the harness
did and why.

## Decision

1. The per-cycle record — trigger Status; Diagnosis evidence and tool calls;
   proposed subject+intent; Approval decision, who, when; resolved
   implementation target; dispatch result; outcome adjudication (ADR 0016) — is
   **accountability**, categorically different from Probe samples.
2. It **does not share Probe-history rolling truncation** (ADR 0002 / Slice 5).
   Separate retention class, retained long by default, operator-configurable,
   but never silently truncated alongside Probe history.
3. **Append-only by discipline and schema:** no update/delete queries are
   generated for audit tables (sqlc insert-only); each transition is a new row.
   This is insert-only discipline, not cryptographic tamper-evidence — full
   tamper-proofing is deliberately out of M2-v1 scope (2am tiebreaker) and is
   not overclaimed.
4. Still behind the Store seam — no new persistence mechanism, just a distinct
   retention class within Store.

## Consequences

- "Why did my box get mutated at 2am" stays answerable for as long as the
  operator chose, independent of telemetry volume.
- The Store seam now carries two retention classes (truncated telemetry vs
  durable audit); this distinction is explicit, not incidental.
- Tamper-evidence is a known, stated limitation, available as a later
  hard-gated ADR if ever required.
