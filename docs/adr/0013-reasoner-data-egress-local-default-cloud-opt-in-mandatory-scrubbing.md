# Reasoner data egress: local default, cloud opt-in, mandatory scrubbing

Status: accepted (extends CONVENTIONS rule 8 to the `Reasoner` seam; relates to
ADR 0009 transport and ADR 0003's trust boundary)

## Context

ADR 0009 made local-vs-cloud LLM a config choice over net/http but left the
consequence open: a cloud Reasoner ships Host data — logs, command output, file
contents, container names, IPs, error strings — to a third-party provider.
Homelab output routinely carries secrets, tokens, paths, and PII. CONVENTIONS
rule 8 forbids *logging* secrets; a cloud Reasoner would *transmit* them off-box
entirely, past the ADR 0003 trust boundary. The project's whole posture (single
operator, private network, agentless pull, no public exposure, least privilege)
argues against egress-by-default.

## Decision

1. **Default Reasoner = a local model server** (separate process, ADR 0009).
   Privacy-by-default; weaker reasoning is the accepted price of zero egress.
2. **Cloud is explicit opt-in config.** Selecting it requires acknowledging a
   documented data-egress note (Host data leaves the trust boundary to a third
   party). Never a silent default.
3. **Mandatory tool-output scrubbing at the `Reasoner` seam, regardless of
   provider.** Known secret/token/key/credential patterns are masked before any
   tool output enters LLM context. This is rule 8 extended: "never *log*
   secrets" → also "never *feed* secrets to the Reasoner." Provider-independent
   so a later local→cloud flip cannot silently start exfiltrating.
4. **Operator guidance at the opt-in point.** Where cloud is selected (config
   docs / UI egress note), advise routing through a provider or proxy that
   supports zero-data-retention — e.g. OpenRouter with ZDR enabled — so opted-in
   egress is at least non-retained. Guidance, not a substitute for scrubbing.

## Considered Options

- **Cloud default (rejected):** best diagnosis, zero infra, but silent egress
  of Host data — contradicts the entire project posture.
- **Cloud only, no scrubbing (rejected):** one misconfig leaks secrets off-box.
- **Local default + cloud opt-in + provider-independent scrubbing + ZDR
  guidance (chosen):** privacy-by-default, informed opt-in, defense in depth.

## Consequences

- Out-of-the-box diagnosis quality is bounded by whatever local model the
  operator runs; this is deliberate and reversible by explicit opt-in.
- Scrubbing is a load-bearing, testable component on the Reasoner path; its
  failure mode must be fail-closed (scrub-or-don't-send), not best-effort.
- The egress note and ZDR guidance are surfaced exactly where the opt-in is
  made, not buried in a README.
