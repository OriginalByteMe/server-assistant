# Security deliberately deferred in v1, hard-gated before M2

## Context

The operator wants security in Server Assistant but has explicitly
deprioritised it for the v1 MVP. v1 is read-only monitoring (no LLM action
harness): it observes the Unraid Host and Services and sends one-way Alerts. It
does, however, hold a long-lived credential into the Host (ADR 0001).

## Decision

v1 ships with minimal auth (single operator, private-network box, no
action-harness surface). Security hardening — dashboard authentication,
secret/credential handling, scoping of the Host credential — is deferred. This
deferral is **deliberate, not an oversight**, and is **hard-gated**: M2
introduces the LLM action harness that can mutate the Host, and M2 MUST NOT
ship until the Host-credential trust model and harness authn/authz are
designed and implemented. (The **design** half is now recorded: ADR 0022
splits the credential; ADR 0009 defines the harness authn/authz and
fail-closed model. The gate still owes **implementation** of both.)

## Consequences

- v1 is acceptable to run only on a trusted private network for a single
  operator; it must not be exposed publicly in this state.
- The Host credential exists in v1 (for Probes) but is read-only-by-convention;
  M2 turning it into write authority is the security trigger point.
