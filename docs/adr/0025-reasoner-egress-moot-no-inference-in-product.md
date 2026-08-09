# Cloud-inference egress is moot: the product contains no inference

Status: accepted (amends ADR 0013 — supersedes it entirely; settled in issue
#51's charting of the Unraid-resident pivot)

## Context

ADR 0013 governed data egress for a `Reasoner` subsystem this codebase would
run itself: local model server by default, cloud opt-in, mandatory tool-output
scrubbing regardless of provider. The pivot settled in issue #51 removes that
subsystem entirely. The product bundles no local model, runs no model server,
does no hardware-aware model selection, and holds no OpenAI/Anthropic/
OpenRouter or any other provider key (explicitly out of scope in issue #51).
The user brings their own LLM and connects it to this product's MCP endpoint.
There is nothing inside this codebase that transmits Host data to an
inference provider, so ADR 0013's local-default/cloud-opt-in/scrubbing
decision has no subject left to apply to.

## Decision

ADR 0013 is superseded in full. It is not replaced by a new inference-egress
policy in this codebase, because there is no inference in this codebase to
police. What replaces it: the egress question **moves to the client side**,
entirely outside this product's control. Whichever LLM the operator connects
over MCP governs its own data handling, its own provider, and its own egress
posture — this product's remaining obligation is only what it exposes *to*
that MCP client (host/storage/container reads, proposal drafting,
approval-gated script execution), not what that client's model provider does
with it afterward. This is worth stating plainly rather than leaving silent:
it is a real reduction in this codebase's control over where Host data ends
up, traded for removing an entire subsystem (Reasoner, scrubbing, ZDR
guidance) the pivot no longer needs.

## Consequences

- CONVENTIONS rule 8 ("never log secrets") still governs everything this
  product itself logs or returns over MCP; it no longer needs the ADR 0013
  extension to a `Reasoner` seam that no longer exists.
- Tool output returned over MCP (host vitals, SMART data, container state,
  script diffs) is grounded, real data with no scrubbing layer in front of
  it — the operator's chosen MCP client, not this product, is now the last
  checkpoint before that data reaches a model provider. An operator who
  connects a cloud LLM is opting that LLM's provider into seeing whatever the
  MCP tools return; this product neither mediates nor can mediate that.
- The local-model-server deployment (Ollama, `sa-triage`) built for the
  retired Mini Lab Harness milestone (`docs/PLAN.md`, M2) is not part of the
  current product. It remains documented there as history, not as this ADR's
  replacement — see the Mini Lab still-running note in `docs/PLAN.md`.
- Reasoner-adjacent ADRs that referenced the egress gate (e.g. ADR 0007's
  Reasoner config, ADR 0009's Reasoner architecture) describe the retired
  Mini Lab Harness milestone; they are historical, not current product
  design, and are unaffected by this ADR beyond that reframing.
