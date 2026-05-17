# Read-only Diagnosis vertical (real LLM, shadow, zero blast radius)

## What to build

Wire the `Reasoner` seam to a real LLM over stdlib `net/http`,
provider-agnostic, with local-vs-cloud purely a config choice (ADR 0009/0013).
Implement the agentic read-only **Diagnosis** loop with hard config ceilings
(max steps, tool calls, wall-clock, token budget) and a `context` deadline,
fail-closed on any LLM failure (ADR 0009/0012). Provide exactly one bounded
read-only tool — the minimum the `restart_container` Diagnosis needs (container
status / allowlisted-container logs) — code-defined, read-credential-ceilinged,
config-narrowed (ADR 0021). Mandatory scrubbing at the Reasoner seam,
scrub-or-don't-send (fail-closed), provider-independent; local default with
documented cloud opt-in plus zero-data-retention guidance surfaced at the
opt-in point (ADR 0013). The LLM proposes a domain subject + catalog intent
only, never an implementation identifier, and is validated against the catalog
as untrusted input (ADR 0018). Still shadow-only: the proposed **Action** is
recorded to the durable audit; no Telegram, no mutation.

Conforms to ADR 0009/0012/0013/0018/0021 and `docs/CONVENTIONS.md` (rules 1, 4,
9, 10).

## Acceptance criteria

- [ ] Reasoner reaches the LLM over stdlib `net/http`; provider local/cloud is config-only; no vendor SDK; `CGO_ENABLED=0` preserved
- [ ] Agentic loop enforces all ceilings + a context deadline; any LLM failure (unreachable/timeout/garbage/off-catalog) yields no proposed Action and degrades to plain v1 monitoring
- [ ] Exactly one bounded read-only tool: code-defined, cannot exceed the read credential, config-narrowed; no code execution of any kind
- [ ] Reasoner-seam scrubbing masks known secret patterns, is scrub-or-don't-send, provider-independent; cloud is opt-in with egress note + ZDR guidance at the opt-in
- [ ] LLM output is domain subject + catalog intent, validated as untrusted; implementation resolution is not the LLM's
- [ ] In shadow, a committed-DOWN runs a real Diagnosis and records the proposed Action to the durable audit; no Telegram, no mutation
- [ ] Reasoner faked in unit tests; no network in unit tests

## Blocked by

- 0010-harness-rails-seams-enablement-audit
