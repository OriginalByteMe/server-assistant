# Action catalog is code; config and the write credential only narrow it

Status: accepted (refines ADR 0009; interacts with CONVENTIONS rule 6 and the
deferred UI-editable-config M2 item from ADR 0006)

## Context

ADR 0009 makes the Action catalog the static bound on the harness's mutation
blast radius. CONVENTIONS rule 6 makes config the single source of truth and
forbids SQLite from holding config. M2 also ships UI-editable config (ADR
0006). The tempting move — define Actions in YAML to avoid redeploying — would
mean a YAML edit, or a UI tap on that config, could grant the LLM a brand-new
mutation capability with no code review, no ADR, no typed-param validation.
That detonates ADR 0009's "blast radius is statically known" property.

## Decision

Three layers, two of them hard ceilings:

1. **Catalog definition is code.** The set of Action *kinds*, their typed
   params, Actuator implementations, and param validation are code. A new
   *kind* of mutation is a deliberate reviewed change and an ADR-grade event —
   never YAML, never UI, never LLM-authored.
2. **The scoped write credential (ADR 0022) is the hard ceiling.** A
   misconfigured config or a code bug cannot exceed what that non-root Unraid
   user is physically permitted. Config is not trusted as the only gate.
3. **Config only activates and narrows within 1 and 2.** Which catalog kinds
   are enabled in this deployment, which containers/paths are in scope,
   cooldown/ceiling/Approval-timeout values, allowlisted Operator chat IDs.
   Config (rule 6, hot-reloadable) can tighten; it can never invent a
   capability nor widen past the credential's real permissions.

UI-editable config may therefore safely disable an Action or shrink an
allowlist; it can never create a mutation power.

## Consequences

- Adding a mutation capability always costs a code review plus an ADR; this
  friction is intentional and is the price of a statically-known blast radius.
- Config and the write credential are independent narrowing ceilings around
  code-defined capability — defense in depth, no single point of trust.
