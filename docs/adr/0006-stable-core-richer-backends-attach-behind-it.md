# Stable v1 core; richer capabilities attach behind it, never reshape it

## Context

Several decisions independently landed on the same shape: build the lean path
now, design the seam so the richer path attaches later without rework — push
agent (ADR 0001), TSDB (ADR 0002), UI-editable config, and the M2 LLM action
harness. Recording the principle once so deferred capabilities are understood
as deliberate, not forgotten.

## Decision

The v1 monitoring spine — Probe ingestion pipeline, Status derivation, the
SQLite data model — is the stable core. Future capabilities attach *behind*
defined seams (ingestion accepts pushed Probes; storage can front a TSDB;
config has a single source of truth a UI can later edit; the action harness
consumes the same Status/Probe model) and must not require reshaping the core.
If a future capability would force a core reshape, that is a signal to revisit
the core deliberately, not to bolt on.

## Consequences

- The v1 build surface is bounded and explicit: Probe types = Host
  reachability, HTTP(S), container-state, Host metrics, TCP/port; one-way
  Telegram Alerts; read-only SSE dashboard. Everything else is a later
  milestone attaching behind a seam.
- The seams (ingestion input, storage interface, config source-of-truth,
  Status/Probe model) are themselves load-bearing and must be designed in v1
  even though only the lean side is built.
