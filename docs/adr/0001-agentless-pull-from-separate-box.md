# Agentless pull from a separate box, agent slot reserved

## Context

Server Assistant monitors a single Unraid Host and its Services. A core
requirement is that it can still observe and diagnose the Host when the Host is
degraded or down, so it runs on a **separate physical box**, not as a container
on Unraid.

## Decision

The Server Assistant box is the authoritative observer and **pulls** Probes:
external HTTP probes for Service reachability/latency, plus SSH or the Unraid
API into the Host for array/disk/parity/container state. No software is
installed on Unraid in v1. The Probe ingestion pipeline and data model are
designed so a future push agent on Unraid can feed the *same* pipeline with no
rework. The full hybrid (pull + push agent) is the destination; v1 builds only
the pull path.

## Considered Options

- **Push agent on Unraid (rejected for v1):** richer host metrics and no
  inbound credentials into Unraid, but when Unraid dies the agent dies — you
  get silence, not a signal — which is exactly the failure mode the separate
  box exists to catch.
- **Build both pull and push in v1 (rejected):** 2–3x the v1 work and two data
  paths to reconcile before the monitoring spine has shipped.

## Consequences

- "Unraid unreachable" is itself a first-class detectable DOWN signal (no
  heartbeat-timeout inference needed in v1).
- Server Assistant needs a long-lived credential into the Host (SSH key or
  Unraid API token). This is a security surface and its handling is
  load-bearing. (**Revised by ADR 0022**: the later LLM action harness gets a
  *separate* catalog-scoped write credential; this Probe credential stays
  read-only forever and is not reused for Actions.)
