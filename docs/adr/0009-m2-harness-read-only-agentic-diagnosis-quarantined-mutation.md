# M2 harness: read-only agentic Diagnosis, mutation quarantined behind Approval

Status: accepted (designs the ADR 0003 hard-gate's harness authn/authz half,
together with ADR 0022's credential split; attaches behind seams per ADR 0006)

## Context

M2 adds the LLM action harness (ADR 0003, ADR 0006). Two incompatible mental
models surfaced: a bounded "LLM picks one Action from a typed catalog"
selector, versus an agentic harness with tools, skills, and code execution.
"Code execution" / LLM-authored shell is the open-ended path explicitly
rejected when the Action catalog was chosen, and it contradicts the **Action**
glossary term ("never authors raw shell"). The harness must still be powerful
enough to actually diagnose a degraded Host, and the LLM may be local or cloud.

## Decision

The Harness **is** agentic — but agency is partitioned by read vs write:

- **Diagnosis is read-only and agentic.** On a committed-DOWN Status the
  Harness runs a multi-step, LLM-driven investigation calling only read-only
  tools (logs, re-Probe, Status history, disk/array reads) bound to the v1
  read credential. No Approval needed; reads cannot harm.
- **Mutation is forever quarantined.** It never happens as an LLM-invoked tool
  and never via code execution. The only mutation path is: Diagnosis proposes
  at most one **Action** from the closed catalog → **Approval** (default-deny,
  single-Operator chat-ID allowlist) → Actuator → separate scoped write
  credential (ADR 0022). **Code execution of any kind — mutating *or*
  read-only-sandboxed — is an explicit non-goal (ADR 0012):** the LLM never
  authors code that runs on the box, read or write. Richer Diagnosis comes only
  via typed read-only tools (reviewed, ADR-grade — same discipline as the
  Action catalog).
- **The agent loop is first-party.** Wire format is OpenAI-compatible
  chat+tool-call JSON over stdlib `net/http`, provider-agnostic; local-vs-cloud
  is a config choice (rule 6), not a code/dependency choice. The LLM sits
  behind a `Reasoner` seam (fakeable, no network in unit tests — rule 9). The
  orchestration loop is owned code, not a third-party agent framework.
- **Fail-closed throughout.** LLM unreachable/timeout/garbage/off-catalog →
  propose nothing. Status UNKNOWN → no act (actor analog of ADR 0005).
  Approval timeout → deny. Crash with an Action of unknown fate → mark
  Unresolved + one-way Alert, never auto-resume a mutation.

## Considered Options

- **Open-ended agentic harness (LLM tools include mutation / code execution,
  driven by an agent framework) — rejected:** maximal power, but unbounded
  blast radius, ungovernable authz, unreadable audit, fails the
  2am-one-person tiebreaker, contradicts the Action glossary term, and a heavy
  fast-moving framework violates CONVENTIONS rule 1.
- **Non-agentic single-shot selector — rejected:** simplest and safest, but too
  weak to actually diagnose a degraded Host; no evidence-gathering.
- **Agentic read-only Diagnosis + quarantined catalog mutation (chosen):**
  real diagnostic power with statically-bounded mutation surface; every
  fail-closed spine (ADR 0005 / ADR 0022 / Approval) stays intact.

## Consequences

- Blast radius is static and reviewable: new mutation capability = a new
  catalog entry = a deliberate code change, never an LLM improvisation.
- The harness orchestration loop is load-bearing owned code that must be
  debuggable at 2am; this is an accepted maintenance cost over a framework.
- Diagnosis is gated by the same one-in-flight + per-subject cooldown as
  Actions, has hard config ceilings (max steps, tool calls, wall-clock, token
  budget) with a `context` deadline on the loop, and is **best-effort**: its
  absence (LLM down, ceiling hit, cooldown) degrades to plain v1 monitoring —
  the committed-DOWN Alert still fires — never to silence.
- The Approval message's **facts block is harness-rendered directly from raw
  tool return values; the LLM has no write access to it** and never composes or
  re-narrates it. LLM output is confined to (a) the catalog Action selection
  and (b) a clearly-labelled, secondary "model interpretation" line. This
  closes the prompt-injection path into the evidence the Operator decides on.
  "Untrusted" covers not just the LLM response but the **content the LLM reads**
  (logs, command output) — hostile-or-accidental input, never echoed unescaped
  into Telegram, never executed (no code-exec, ADR 0012), loop/cost bounded by
  the Diagnosis ceilings. The full Diagnosis transcript lives in Store by id
  (ADR 0006), not in the Telegram message; the message restates silence = deny.
- The Approval Telegram transport is **long-poll (`getUpdates`), never
  webhook**: the SA box keeps zero inbound surface, consistent with ADR 0001's
  pull posture and ADR 0003's no-public-exposure stance. A public inbound
  endpoint on the box that holds the write credential and the approve/deny gate
  is rejected outright.
- The Action catalog's code/config/credential layering is refined in ADR 0010.
- ADR 0003's hard gate is satisfied by ADR 0022 (credential split) **plus**
  this ADR (harness authn/authz: read/write partition, Approval authz,
  fail-closed); M2 must not ship until both are implemented, not just designed.
