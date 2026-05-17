# M2-v1 Action catalog is `restart_container` only; reboot_host is maybe-never

Status: accepted (instantiates ADR 0009/0010; scopes the ADR 0022 write
credential; follows the tracer-bullet philosophy of ADR 0006 / the v1 PLAN)

## Context

ADR 0009/0010 fix *how* Actions work and that each Action kind is an ADR-grade
code change. They do not say *which* Actions ship first. Every catalog entry
widens the ADR 0022 write Unraid user's permissions, so a bigger first catalog
is a bigger leak radius. The v1 PLAN's tracer-bullet principle (smallest slice
that proves the whole spine) applies equally here.

## Decision

The M2-v1 Action catalog is **exactly one Action**: `restart_container(name)`,
with `name` constrained to a config allowlist (per ADR 0010). This proves the
full Diagnosis → catalog → Approval → Actuator → write-credential → audit spine
end-to-end on the lowest-stakes, most-reversible, ~idempotent mutation that
exists, and keeps the write credential scoped to "restart these containers,
nothing else." Every other Action (`free_space`, `restart_service`, disk/array
ops, etc.) is a later standalone ADR that deliberately widens the write
credential.

## Considered Options

- **Richer initial catalog (rejected):** more immediately useful, but a wider
  write credential and a bigger blast radius before the spine is proven.
- **Include `reboot_host` (rejected — flagged maybe-never):** the most
  requested and the most lethal. It is non-idempotent and lethal twice (ADR
  0007/Q7 reasoning), and rebooting the Host drives the Host to UNKNOWN, which
  by the ADR 0005 actor-analog blinds the harness during the exact window it
  would most need to observe. It fails multiple spine rules simultaneously. If
  it ever ships it must never be auto-proposed and needs its own hard-gated
  ADR.
- **`restart_container` only (chosen):** smallest write-cred scope, best
  value-to-risk, tracer-bullet.

## Consequences

- The M2 write Unraid user is provisioned for container restart only; widening
  it is a visible, ADR-gated event.
- "Why can't it reboot the box?" is answered here on purpose, so nobody adds
  `reboot_host` casually.
- The narrow catalog doubles as the **prompt-injection blast-radius cap**: a
  fully hijacked LLM (via hostile content in the logs it reads during
  Diagnosis) can at most propose an allowlisted, reversible container restart —
  still gated by fail-closed Approval with a harness-rendered facts block (ADR
  0009). Injection defense is emergent from the spine, not a bolt-on.
