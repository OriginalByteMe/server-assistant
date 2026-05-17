# Server Assistant

Monitoring + automation gateway for a single self-hosted Unraid box. Runs on a
separate physical server so it can observe and (later) diagnose the Unraid box
even when that box is degraded or down. v1 is the monitoring spine only;
LLM-driven actions are a later milestone.

## Language

**Service**:
A logical thing the operator cares about staying healthy (e.g. Plex,
Nextcloud), independent of how it is implemented. A Service being "healthy"
means it does its job (web UI answers), not merely that its container exists.
_Avoid_: app, container (a container is one implementation detail of a Service)

**Host**:
The single Unraid box itself, as a monitorable subject in its own right —
array status, disk health, parity, CPU/RAM. First-class because Host problems
are a common root cause of Service problems.
_Avoid_: server (ambiguous — "server" could mean the Host or the separate box
running Server Assistant)

**Operator**:
The single human who owns the Unraid box and runs Server Assistant. The "user"
who gets notified and approves actions.
_Avoid_: user, admin

**Alert**:
A one-way outbound message to the Operator (via Telegram in v1) triggered by a
**Status** change. Strictly one-way in v1. Distinct from a future **Approval**
(M2, two-way: Operator approves/denies an LLM action over the same Telegram
channel) — do not conflate the two.
_Avoid_: notification (too vague — splits into Alert vs Approval)

**Probe**:
One measurement taken against a Service or Host (e.g. is it reachable, is
latency under threshold, is the container running). Probes are the raw inputs;
they do not themselves define health.
_Avoid_: health check, monitor, ping

**Status**:
A Service's or Host's derived health, computed from its Probes. Exactly one of:
**UP** (doing its job), **DEGRADED** (reachable but slow — latency over a
per-Service threshold — or only partially working), **DOWN** (confirmed not
doing its job), **UNKNOWN** (the observer cannot determine — it is blind, not a
claim about the subject). "Slow" = DEGRADED. "Can't tell" = UNKNOWN, never DOWN.
_Avoid_: up/down (binary loses DEGRADED/UNKNOWN), state, health (too vague)

**Action** (M2):
A named, typed, parameterized operation the harness can perform against the
**Host**, drawn from a closed catalog — the LLM selects one and fills typed
params; it never authors raw shell. Distinct from a **Probe** (read-only
measurement): an **Action** mutates the **Host**.
_Avoid_: command (implies raw shell), task, operation

**Approval** (M2):
A two-way exchange in which the harness proposes an **Action** and the
**Operator** approves or denies it before it executes — request then response,
over the same Telegram channel as **Alert**s but a distinct concept and a
distinct seam. Strictly never conflated with **Alert** (one-way, informational).
_Avoid_: notification, confirmation, prompt

**Harness** (M2):
The bounded LLM subsystem that, on a committed-DOWN **Status**, runs a
read-only **Diagnosis** and may propose at most one **Action** for **Operator**
**Approval** — it never mutates the **Host** except through the
Approval→Actuator path. It is first-party orchestration driving an external
(config-chosen local or cloud) model, not a third-party agent framework.
_Avoid_: the LLM (the model is one part the Harness drives), agent framework, bot

**Diagnosis** (M2):
The **Harness**'s read-only, multi-step, LLM-driven investigation of a
DOWN subject using read-only tools bounded to the read credential — gathers
evidence to justify a proposed **Action**; never changes the **Host**.
_Avoid_: action (a Diagnosis reads, an Action mutates), probe (a Probe is one
scheduled measurement; a Diagnosis is an on-demand multi-step investigation)

## Relationships

- A **Host** runs many **Services**
- A **Service** is observed independently of the **Host**, but its **Status**
  can be caused by **Host** **Status**
- A **Service**/**Host** has many **Probes**; its **Status** is derived from
  them
- Scheduled polling is the authoritative source of **Status**; an inbound
  webhook may flip **Status** faster or add context but **Status** is never
  derived solely from the *absence* of a webhook
- A **Status** change only *commits* after N consecutive agreeing **Probes**
  (debounce); an **Alert** fires only on a committed change, including recovery
  to UP
- A **Host** reachability **Probe** gates its **Services**: if the Server
  Assistant box cannot reach the **Host**, those **Services** become UNKNOWN
  (not DOWN) and exactly one "Host unreachable" **Alert** fires — never one
  **Alert** per **Service**
- A committed-DOWN **Status** may trigger a **Harness** **Diagnosis**; a
  **Diagnosis** may produce at most one proposed **Action**
- An **Action** executes only after **Operator** **Approval** (default-deny on
  timeout); the **Harness** never mutates the **Host** any other way
- The **Harness** reads via the read credential; **Action**s mutate via the
  separate scoped write credential (ADR 0022)
- The **Harness** is itself a monitored subject: its own **Status** is derived
  from **Probe**s of its dependencies; a committed harness-DOWN fires a one-way
  **Alert**, never an **Approval** (ADR 0015)
- The **Harness** reasons only in **Service**/**Host**/**Status** terms; it
  never names a container or other implementation detail — the harness resolves
  a domain subject to its implementation deterministically from config (ADR
  0018)
- The **Operator** controls the **Harness** over the same Telegram channel as
  **Approval** (single-Operator authz); a sticky, fail-closed halt is always
  available and survives restart (ADR 0020)

## Flagged ambiguities

- "server" was used to mean both the monitored Unraid **Host** and the
  separate box running Server Assistant — resolved: **Host** = monitored box;
  "Server Assistant box" = the separate box it runs on.
- "service" — resolved: a **Service** is logical, not a container.
- "health check" / "probe" / "speed limit" / "slow" — resolved: a **Probe** is
  one raw measurement; **Status** (UP/DEGRADED/DOWN) is the derived health;
  "slow" = DEGRADED.
- "harness" / "code execution" — resolved: the **Harness** is agentic but
  *read-only* for **Diagnosis**; **Action** mutation is quarantined behind
  **Approval**→Actuator; LLM-invoked mutation is forbidden, and LLM code
  execution of *any* kind (read or write, sandboxed or not) is an explicit
  non-goal (see ADR 0009, ADR 0012).
- "harness" was also conflated with "the LLM" and "an agent framework" —
  resolved: the **Harness** is first-party orchestration driving an external
  config-chosen model; not a third-party agent framework.
- "OAuth" — resolved: a voice-transcription artifact for **Unraid**. The term
  "OAuth" carries no meaning in this project. v1 deliberately defers auth /
  security (see ADR 0003); the project is not adopting OAuth as a concept.
