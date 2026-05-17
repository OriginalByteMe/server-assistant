# The LLM reasons only in domain terms; the harness owns implementation resolution

Status: accepted (makes the CONTEXT.md Service≠container boundary load-bearing
for the harness; sharpens ADR 0011; generalizes to every future Action)

## Context

The M2-v1 catalog is `restart_container(name∈allowlist)` (ADR 0011). It was
unspecified whether the LLM emits the container name directly or the Service it
wants remediated. If the LLM names the container, it must guess an
implementation identifier (Service "Plex" → `plex`? `binhex-plex`?); the
allowlist validates membership, not mapping correctness, so a wrong-but-
allowlisted target restarts silently. It also puts the LLM in the business of
implementation detail, which violates CONTEXT.md's own spine: a Service is what
the Operator cares about; a container is one implementation detail of a Service.

## Decision

The LLM reasons only in domain terms; the harness owns the translation:

1. The LLM selects a **domain-level subject + catalog intent** (e.g. "restart
   the Plex Service"), never an implementation identifier.
2. The harness **deterministically resolves subject → implementation from
   reviewed config/catalog** (the Service→container mapping is config,
   ADR-0010-style; no LLM in the resolution path). The allowlist is still
   enforced at the resolved layer — defense in depth.
3. This generalizes to every future Action: the LLM always reasons in
   CONTEXT.md language (Service / Host / Status); the harness/Actuator always
   owns translation to containers, paths, PIDs, units. The LLM never traffics
   in implementation detail.

## Consequences

- A smaller, higher-signal choice space for the LLM: less hallucination, less
  prompt-injection leverage, and Approval messages that read in Service terms
  the Operator actually cares about.
- Wrong-target mutation by guesswork is structurally impossible; the only
  mapping is deterministic, reviewed config.
- Every future catalog entry inherits the same boundary for free; "let the LLM
  name the container, it's simpler" is rejected here on purpose.
