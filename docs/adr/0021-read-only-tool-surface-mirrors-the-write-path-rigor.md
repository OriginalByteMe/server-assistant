# The read-only tool surface gets the same rigor as the write path

Status: accepted (closes the read/write asymmetry; mirrors ADR 0022/0010/0011
onto the Diagnosis read path; adds the missing upstream layer to the ADR
0012/0013/Q16 read-path defenses)

## Context

The design hardened the write path thoroughly — ADR 0022 (scoped credential
ceiling), 0010 (catalog is code, config narrows), 0011 (minimal tracer-bullet
set), 0018 (domain-only). The read path — the tools the agentic Diagnosis loop
calls freely and without Approval by design — got only 0012 (add typed
read-only tools), 0013 (scrubbing), and the Q16 injection cap on output.
Nobody bounded what the read tools may read in the first place. Because the
loop calls read tools freely, the read tool surface is the only thing between
an agentic LLM loop and reading arbitrary secrets/paths; unbounded read turns
the Q16 injection ingress into an exfiltration ingress, sharpest under the
cloud opt-in (ADR 0013).

## Decision

Mirror the write-path rigor onto the read path:

1. **Read tool definitions are code, ADR-grade** to add a new kind of read
   tool (mirror of ADR 0010 layer 1).
2. **The read credential (ADR 0022) is the hard ceiling** — no tool reads what
   the read Unraid user cannot (mirror of the write-credential ceiling;
   defense in depth).
3. **Config activates and narrows** which tools/paths/scopes are in play per
   deployment ("logs of allowlisted containers only", specific metric reads),
   never "read any file" (mirror of ADR 0010 layer 3).
4. **M2-v1 read tools are the minimum the `restart_container` Diagnosis
   actually needs** — container status, allowlisted-container logs, the
   Status/Probe history already in Store. Everything else is a later ADR-grade
   addition (ADR 0011's tracer-bullet logic, mirrored for reads).

## Consequences

- The read path now has four independent layers: bounded tool surface (this) →
  read-credential ceiling (ADR 0022) → Reasoner scrubbing (ADR 0013) → no
  code-exec (ADR 0012).
- "Reads can't mutate, so the read surface is harmless" is rejected on purpose:
  it is the exfiltration and injection ingress and is bounded accordingly.
- Read and write catalogs now evolve under identical discipline; neither grows
  without a deliberate ADR.
