# Split Host credentials: read-only Probe credential vs catalog-scoped write credential

Status: accepted (amends ADR 0001 — supersedes its "credential is reused by the
later LLM action harness" expectation)

## Context

ADR 0001 reserved a single long-lived Host credential and stated it was
"expected to be reused by the later LLM action harness." ADR 0003 hard-gates M2
on the Host-credential trust model and identifies "M2 turning it into write
authority" as the security trigger point. CONVENTIONS rule 7 requires least
privilege from day one. Reusing one credential for both Probes and Actions
means the read path carries write authority, a leaked poll-interval key grants
full Host write, and the Host cannot audit-split probe traffic from action
traffic.

## Decision

M2 introduces a **second, distinct Host credential** rather than reusing the
Probe credential. The v1 Probe credential stays frozen as specified (non-root,
read-only Unraid user, lives only behind `Prober`) and never gains write. The
Action credential is a separate non-root Unraid user scoped to exactly the
Action catalog's verbs (no shell, no arbitrary file read, no sudo), lives only
behind `Actuator`, and is never shared with the Probe path.

## Considered Options

- **Reuse one credential (ADR 0001 as written, rejected):** simpler, one
  secret to manage, but read path gains write authority, single leaked
  long-lived key = full Host read+write, no Host-side audit split.
- **Split read vs catalog-scoped write (chosen):** least-privilege per rule 7,
  Probe-key leak ≠ write, Action-key leak bounded to the catalog, clean
  Host-side audit split. Cost: two Unraid users to provision and rotate.

## Consequences

- ADR 0001's reuse expectation is revised; the agentless-pull credential
  remains read-only forever.
- This split is a precondition of the ADR 0003 hard gate, not the whole gate:
  harness authn/authz (who may approve, Approval lifecycle) is still owed.
